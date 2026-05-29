package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/config"
	"github.com/theomorin/dbpilot/internal/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var pitrCmd = &cobra.Command{
	Use:   "pitr <name> <enable|disable|status>",
	Short: "Manage continuous WAL streaming for PITR",
}

var flagPITRKubeconfig string

func init() {
	home, _ := os.UserHomeDir()
	defaultKube := filepath.Join(home, ".kube", "config")

	pitrCmd.PersistentFlags().StringVar(&flagPITRKubeconfig, "kubeconfig", defaultKube, "path to kubeconfig file")

	pitrCmd.AddCommand(pitrEnableCmd)
	pitrCmd.AddCommand(pitrDisableCmd)
	pitrCmd.AddCommand(pitrStatusCmd)
	pitrCmd.AddCommand(pitrBasebackupCmd)
}

var pitrEnableCmd = &cobra.Command{
	Use:          "enable <name> [job-name]",
	Short:        "Enable WAL streaming for PITR",
	SilenceUsage: true,
	Args:         cobra.RangeArgs(1, 2),
	RunE:         runPITREnable,
}

var pitrDisableCmd = &cobra.Command{
	Use:          "disable <name>",
	Short:        "Disable WAL streaming and remove WAL agent Deployment(s)",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE:         runPITRDisable,
}

var pitrBasebackupCmd = &cobra.Command{
	Use:          "basebackup <name> [job-name]",
	Short:        "Take a WAL-G basebackup (required before PITR restore)",
	SilenceUsage: true,
	Args:         cobra.RangeArgs(1, 2),
	RunE:         runPITRBasebackup,
}

var pitrStatusCmd = &cobra.Command{
	Use:          "status <name>",
	Short:        "Show WAL agent status for each namespace",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE:         runPITRStatus,
}

func runPITRBasebackup(cmd *cobra.Command, args []string) error {
	path, err := config.NamedPath(args[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	filterJob := ""
	if len(args) == 2 {
		filterJob = args[1]
	}

	for _, job := range cfg.Jobs {
		if !job.PITR.Enabled {
			continue
		}
		if filterJob != "" && job.Name != filterJob {
			continue
		}
		fmt.Printf("Taking WAL-G basebackup for job %q...\n", job.Name)
		k8sJob, err := k8s.TriggerBaseBackup(flagPITRKubeconfig, job)
		if err != nil {
			return fmt.Errorf("job %q: %w", job.Name, err)
		}
		fmt.Printf("  %s  Job started: job/%s\n", styleOK.Render("▶"), k8sJob.Name)

		k8scfg, err := clientcmd.BuildConfigFromFlags("", flagPITRKubeconfig)
		if err != nil {
			return err
		}
		client, err := kubernetes.NewForConfig(k8scfg)
		if err != nil {
			return err
		}
		status, err := waitForJobWithLogs(client, k8sJob, 30*60*1e9)
		if err != nil {
			return err
		}
		if status == "success" {
			fmt.Printf("  %s  Basebackup complete.\n", styleOK.Render("✓"))
		} else {
			fmt.Printf("  %s  Basebackup %s\n", styleErr.Render("✗"), status)
		}
	}
	return nil
}

func runPITREnable(cmd *cobra.Command, args []string) error {
	path, err := config.NamedPath(args[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	filterJob := ""
	if len(args) == 2 {
		filterJob = args[1]
	}

	// Collect jobs to enable.
	var targetJobs []int
	for i := range cfg.Jobs {
		if filterJob != "" && cfg.Jobs[i].Name != filterJob {
			continue
		}
		targetJobs = append(targetJobs, i)
	}
	if len(targetJobs) == 0 {
		return fmt.Errorf("no matching job found")
	}

	// For each job, ensure the pgpilot replication user exists and show pg_hba instructions if needed.
	for _, i := range targetJobs {
		job := cfg.Jobs[i]
		if err := ensureReplicationUser(job, flagPITRKubeconfig); err != nil {
			return err
		}
		if !job.PITR.IsCNPG {
			showVanillaPITRInstructions(job)
		} else if !k8s.CNPGHasReplicationHBA(flagPITRKubeconfig, job) {
			showCNPGPITRInstructions(job)
		}
		cfg.Jobs[i].PITR.Enabled = true
	}

	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("\nMarked %d job(s) as pitr.enabled = true\n\n", len(targetJobs))

	fmt.Println("Deploying WAL agent(s)...")
	results, err := k8s.DeployWALAgents(flagPITRKubeconfig, cfg, false)
	if err != nil {
		return err
	}
	for _, r := range results {
		fmt.Printf("  %s  namespace %s — %s (%d job(s))\n", styleOK.Render("✓"), r.Namespace, r.Action, r.JobCount)
	}
	fmt.Printf("\n%s  %d WAL agent(s) deployed. PITR is now active.\n", styleOK.Render("✓"), len(results))
	return nil
}

func showVanillaPITRInstructions(job config.JobConfig) {
	fmt.Printf("\n  %s  Job %q runs on vanilla Postgres.\n", styleErr.Render("⚠"), job.Name)
	fmt.Println("  WAL streaming requires two manual changes on your Postgres instance:\n")
	fmt.Println("  1. Allow replication connections in pg_hba.conf:")
	fmt.Printf("       host replication pgpilot all md5\n\n")
	fmt.Println("  2. Enable WAL archiving in postgresql.conf:")
	fmt.Println("       wal_level = replica\n")
	fmt.Println("  For a Kubernetes Deployment, add these args to your container:")
	fmt.Printf("       args: [\"-c\", \"wal_level=replica\"]\n")
	fmt.Println("  And mount a ConfigMap with your pg_hba.conf including the replication rule.")
	fmt.Println("  Then restart your Postgres pod.\n")
	fmt.Print("  Press Enter once done (or Enter to skip and configure later)... ")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func showCNPGPITRInstructions(job config.JobConfig) {
	clusterName := job.Credentials.DBHost
	if len(clusterName) > 3 {
		clusterName = clusterName[:len(clusterName)-3] // strip "-rw"
	}
	fmt.Printf("\n  %s  Job %q runs on CNPG.\n", styleErr.Render("⚠"), job.Name)
	fmt.Println("  Add the following to your Cluster spec to allow replication connections:\n")
	fmt.Printf("    spec:\n")
	fmt.Printf("      postgresql:\n")
	fmt.Printf("        pg_hba:\n")
	fmt.Printf("          - host replication pgpilot all md5\n\n")
	fmt.Printf("  Cluster name: %s (namespace: %s)\n\n", clusterName, job.Environment.Namespace)
	fmt.Print("  Press Enter once done (or Enter to skip and configure later)... ")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

// ensureReplicationUser creates the pgpilot replication user via a K8s Job.
// If the current DB user lacks privileges, it prints the SQL and waits for manual confirmation.
func ensureReplicationUser(job config.JobConfig, kubeconfig string) error {
	fmt.Printf("Setting up replication user for job %q...\n", job.Name)

	generatedPassword, alreadyExisted, err := k8s.EnsureReplicationUser(kubeconfig, job)

	if alreadyExisted {
		fmt.Printf("  %s  Replication user %q already exists.\n", styleOK.Render("✓"), k8s.ReplicationUser)
		return nil
	}

	if errors.Is(err, k8s.ErrInsufficientPrivilege) {
		fmt.Printf("\n  %s  DB user lacks superuser privileges to create a replication role.\n",
			styleErr.Render("⚠"))
		fmt.Printf("  Please run the following on your Postgres instance:\n\n")
		fmt.Printf("    CREATE USER %s WITH REPLICATION LOGIN PASSWORD '%s';\n\n",
			k8s.ReplicationUser, generatedPassword)
		fmt.Print("  Press Enter once done... ")
		bufio.NewReader(os.Stdin).ReadString('\n')

		if verifyErr := k8s.VerifyReplicationUser(kubeconfig, job); verifyErr != nil {
			return fmt.Errorf("replication user not found after manual setup: %w", verifyErr)
		}
		if storeErr := k8s.StoreReplicationPassword(kubeconfig, job.Environment.Namespace, generatedPassword); storeErr != nil {
			return fmt.Errorf("storing replication password: %w", storeErr)
		}
		fmt.Printf("  %s  Replication user %q verified.\n", styleOK.Render("✓"), k8s.ReplicationUser)
		return nil
	}

	if err != nil {
		return fmt.Errorf("setting up replication user: %w", err)
	}

	fmt.Printf("  %s  Replication user %q created.\n", styleOK.Render("✓"), k8s.ReplicationUser)
	return nil
}

func runPITRDisable(cmd *cobra.Command, args []string) error {
	path, err := config.NamedPath(args[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	for i := range cfg.Jobs {
		cfg.Jobs[i].PITR.Enabled = false
	}
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	results, err := k8s.RemoveWALAgents(flagPITRKubeconfig, cfg)
	if err != nil {
		return err
	}
	for _, r := range results {
		fmt.Printf("  %s  namespace %s — %s\n", styleOK.Render("✓"), r.Namespace, r.Action)
	}
	fmt.Println("\nPITR disabled.")
	return nil
}

func runPITRStatus(cmd *cobra.Command, args []string) error {
	path, err := config.NamedPath(args[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	statuses, err := k8s.GetWALAgentStatus(flagPITRKubeconfig, cfg)
	if err != nil {
		return err
	}

	if len(statuses) == 0 {
		fmt.Println("No PITR-enabled jobs found.")
		return nil
	}

	fmt.Printf("%-30s %-10s %s\n", "NAMESPACE", "READY", "STATUS")
	fmt.Println(string(make([]byte, 55)))
	for _, s := range statuses {
		if s.Message != "" {
			fmt.Printf("%-30s %-10s %s\n", s.Namespace, "-", s.Message)
		} else {
			status := "running"
			if s.Ready == 0 {
				status = styleErr.Render("not ready")
			}
			fmt.Printf("%-30s %-10s %s\n", s.Namespace, fmt.Sprintf("%d/%d", s.Ready, s.Desired), status)
		}
	}
	return nil
}

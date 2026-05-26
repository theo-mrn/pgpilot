package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/config"
	"github.com/theomorin/dbpilot/internal/k8s"
)

var deployCmd = &cobra.Command{
	Use:          "deploy",
	Short:        "Deploy backup CronJobs to Kubernetes from backup.yaml",
	SilenceUsage: true,
	RunE:         runDeploy,
}

var flagDeployKubeconfig string
var flagDeployConfig string
var flagDryRun bool

func init() {
	home, _ := os.UserHomeDir()
	defaultKubeconfig := filepath.Join(home, ".kube", "config")
	defaultConfig, _ := config.DefaultPath()

	deployCmd.Flags().StringVar(&flagDeployKubeconfig, "kubeconfig", defaultKubeconfig, "path to kubeconfig file")
	deployCmd.Flags().StringVarP(&flagDeployConfig, "config", "c", defaultConfig, "path to backup.yaml")
	deployCmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "show what would be deployed without applying")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(flagDeployConfig)
	if err != nil {
		return err
	}

	errs := config.Validate(cfg)
	if len(errs) > 0 {
		fmt.Println("backup.yaml is invalid — run 'dbpilot validate' for details.")
		for _, e := range errs {
			fmt.Printf("  %s\n", styleErr.Render("✗ "+e.Error()))
		}
		return fmt.Errorf("aborting deploy")
	}

	if flagDryRun {
		fmt.Println("Dry run — no changes will be applied.\n")
	}

	// Ensure s3-credentials is available in each job's namespace
	namespaces := make(map[string]bool)
	for _, job := range cfg.Jobs {
		namespaces[job.Environment.Namespace] = true
	}
	for ns := range namespaces {
		if err := k8s.CopySecretToNamespace(flagDeployKubeconfig, k8s.S3SecretName, ns); err != nil {
			return fmt.Errorf("propagating s3-credentials to %s: %w", ns, err)
		}
	}

	fmt.Printf("Deploying %d job(s)...\n\n", len(cfg.Jobs))

	results, err := k8s.DeployBackupJobs(flagDeployKubeconfig, cfg, flagDryRun)
	if err != nil {
		return err
	}

	for _, r := range results {
		fmt.Printf("  %s  %s  (%s/%s)\n",
			styleOK.Render("✓"),
			r.JobName,
			r.Namespace,
			r.Action,
		)
	}

	fmt.Printf("\n%d CronJob(s) deployed.\n", len(results))
	return nil
}

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/config"
	"github.com/theomorin/dbpilot/internal/k8s"
	"github.com/theomorin/dbpilot/internal/storage"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// restoreCmd is the top-level "restore" group.
var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore databases from backups",
}

func newRestoreNameCmd(cfgName string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   cfgName,
		Short: fmt.Sprintf("Restore commands for config %q", cfgName),
	}

	runCmd := &cobra.Command{
		Use:          "run [job-name]",
		Short:        "Restore a database from a backup",
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			kubeconfig, _ := c.Flags().GetString("kubeconfig")
			jobName := ""
			if len(args) > 0 {
				jobName = args[0]
			}
			return runRestoreRun(cfgName, jobName, kubeconfig)
		},
	}
	home, _ := os.UserHomeDir()
	runCmd.Flags().String("kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")

	cmd.AddCommand(runCmd)
	return cmd
}

func runRestoreRun(cfgName, jobName, kubeconfig string) error {
	cfgPath, err := config.NamedPath(cfgName)
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if len(cfg.Jobs) == 0 {
		return fmt.Errorf("config %q has no jobs", cfgName)
	}

	// Determine which jobs to restore
	var jobs []config.JobConfig
	if jobName != "" {
		for _, j := range cfg.Jobs {
			if j.Name == jobName {
				jobs = append(jobs, j)
				break
			}
		}
		if len(jobs) == 0 {
			return fmt.Errorf("job %q not found in config %q", jobName, cfgName)
		}
	} else {
		// Multi-select
		selected, err := runJobSelector(cfg.Jobs)
		if err != nil {
			return err
		}
		if len(selected) == 0 {
			fmt.Println("No jobs selected.")
			return nil
		}
		selectedSet := make(map[string]bool)
		for _, n := range selected {
			selectedSet[n] = true
		}
		for _, j := range cfg.Jobs {
			if selectedSet[j.Name] {
				jobs = append(jobs, j)
			}
		}
	}

	// Restore each selected job
	for _, job := range jobs {
		if err := restoreJob(job, cfgName, kubeconfig); err != nil {
			fmt.Printf("%s  %s: %v\n", styleErr.Render("✗"), job.Name, err)
		}
	}
	return nil
}

func restoreJob(job config.JobConfig, cfgName, kubeconfig string) error {
	if len(job.Destinations) == 0 {
		return fmt.Errorf("no destinations configured")
	}
	dest := job.Destinations[0]

	accessKey, err := k8s.ReadSecret(kubeconfig, dest.S3AccessKey.From)
	if err != nil {
		return fmt.Errorf("reading S3 access key: %w", err)
	}
	secretKey, err := k8s.ReadSecret(kubeconfig, dest.S3SecretKey.From)
	if err != nil {
		return fmt.Errorf("reading S3 secret key: %w", err)
	}

	fmt.Printf("\nListing backups for %s (s3://%s/%s)...\n\n", job.Name, dest.Bucket, dest.Prefix)
	objects, err := storage.ListBackups(dest.Bucket, dest.Prefix, accessKey, secretKey, dest.Region, dest.Endpoint)
	if err != nil {
		return fmt.Errorf("listing backups: %w", err)
	}
	if len(objects) == 0 {
		return fmt.Errorf("no backups found in s3://%s/%s", dest.Bucket, dest.Prefix)
	}

	selectedKey, err := runBackupSelector(objects)
	if err != nil {
		return err
	}
	if selectedKey == "" {
		fmt.Println("  Skipped.")
		return nil
	}

	s3URL := fmt.Sprintf("s3://%s/%s", dest.Bucket, selectedKey)
	fmt.Printf("\nRestoring %s into %q...\n", s3URL, job.Name)
	fmt.Print(styleErr.Render("  ⚠  This will overwrite the current database. Continue? [y/N] "))
	var ans string
	fmt.Scanln(&ans)
	if strings.ToLower(ans) != "y" {
		fmt.Println("  Skipped.")
		return nil
	}

	k8sJob, err := k8s.TriggerRestore(kubeconfig, job, s3URL, 0)
	if err != nil {
		return fmt.Errorf("triggering restore: %w", err)
	}
	fmt.Printf("  %s  Restore job started: job/%s\n\n", styleOK.Render("▶"), k8sJob.Name)

	k8scfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
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
		fmt.Printf("%s  Restore complete.\n", styleOK.Render("✓"))
	} else {
		fmt.Printf("%s  Restore %s\n", styleErr.Render("✗"), status)
	}
	return nil
}

// backupSelectorModel lets the user pick one backup from a list.
type backupSelectorModel struct {
	items  []storage.BackupObject
	cursor int
	chosen string
}

func (m backupSelectorModel) Init() tea.Cmd { return nil }

func (m backupSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			m.chosen = m.items[m.cursor].Key
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m backupSelectorModel) View() string {
	var b strings.Builder
	b.WriteString("Select a backup to restore:\n")
	b.WriteString(styleSubtext.Render("  ↑/↓ navigate   enter confirm   q cancel") + "\n\n")
	for i, obj := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = styleCursor.Render("> ")
		}
		parts := strings.Split(obj.Key, "/")
		filename := parts[len(parts)-1]
		line := fmt.Sprintf("%-40s  %s  %s", filename, obj.LastModified, formatSize(obj.Size))
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, line))
	}
	return b.String()
}

func runBackupSelector(objects []storage.BackupObject) (string, error) {
	// Most recent first
	reversed := make([]storage.BackupObject, len(objects))
	for i, o := range objects {
		reversed[len(objects)-1-i] = o
	}
	result, err := tea.NewProgram(backupSelectorModel{items: reversed}).Run()
	if err != nil {
		return "", err
	}
	return result.(backupSelectorModel).chosen, nil
}

func init() {
	// Register known configs as sub-groups at startup
	if dir, err := config.ConfigDir(); err == nil {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
					n := strings.TrimSuffix(e.Name(), ".yaml")
					restoreCmd.AddCommand(newRestoreNameCmd(n))
				}
			}
		}
	}
}

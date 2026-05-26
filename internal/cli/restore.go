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

var restoreCmd = &cobra.Command{
	Use:          "restore <config> [job-name]",
	Short:        "Restore a database from a backup",
	SilenceUsage: true,
	Args:         cobra.RangeArgs(1, 2),
	RunE:         runRestore,
}

var flagRestoreKubeconfig string
func init() {
	home, _ := os.UserHomeDir()
	restoreCmd.Flags().StringVar(&flagRestoreKubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")
}

func runRestore(cmd *cobra.Command, args []string) error {
	cfgPath, err := config.NamedPath(args[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	// Find the job — prompt if not specified
	var job *config.JobConfig
	if len(args) == 2 {
		for i := range cfg.Jobs {
			if cfg.Jobs[i].Name == args[1] {
				job = &cfg.Jobs[i]
				break
			}
		}
		if job == nil {
			return fmt.Errorf("job %q not found in config %q", args[1], args[0])
		}
	} else {
		selectedName, err := runSingleJobSelector(cfg.Jobs)
		if err != nil {
			return err
		}
		if selectedName == "" {
			fmt.Println("No job selected.")
			return nil
		}
		for i := range cfg.Jobs {
			if cfg.Jobs[i].Name == selectedName {
				job = &cfg.Jobs[i]
				break
			}
		}
	}
	if len(job.Destinations) == 0 {
		return fmt.Errorf("job %q has no destinations", args[1])
	}

	// Use first destination
	dest := job.Destinations[0]

	// Read S3 credentials from K8s secret
	accessKey, err := k8s.ReadSecret(flagRestoreKubeconfig, dest.S3AccessKey.From)
	if err != nil {
		return fmt.Errorf("reading S3 access key: %w", err)
	}
	secretKey, err := k8s.ReadSecret(flagRestoreKubeconfig, dest.S3SecretKey.From)
	if err != nil {
		return fmt.Errorf("reading S3 secret key: %w", err)
	}

	fmt.Printf("Listing backups in s3://%s/%s...\n\n", dest.Bucket, dest.Prefix)
	objects, err := storage.ListBackups(dest.Bucket, dest.Prefix, accessKey, secretKey, dest.Region, dest.Endpoint)
	if err != nil {
		return fmt.Errorf("listing backups: %w", err)
	}
	if len(objects) == 0 {
		return fmt.Errorf("no backups found in s3://%s/%s", dest.Bucket, dest.Prefix)
	}

	// Interactive selector
	selected, err := runBackupSelector(objects)
	if err != nil {
		return err
	}
	if selected == "" {
		fmt.Println("No backup selected.")
		return nil
	}

	s3URL := fmt.Sprintf("s3://%s/%s", dest.Bucket, selected)
	fmt.Printf("\nRestoring %s into job %q...\n\n", s3URL, job.Name)
	fmt.Println(styleErr.Render("  ⚠  This will overwrite the current database. Continue? [y/N] "))
	var ans string
	fmt.Scanln(&ans)
	if strings.ToLower(ans) != "y" {
		fmt.Println("Aborted.")
		return nil
	}

	k8sJob, err := k8s.TriggerRestore(flagRestoreKubeconfig, *job, s3URL, 0)
	if err != nil {
		return fmt.Errorf("triggering restore: %w", err)
	}
	fmt.Printf("  %s  Restore job started: job/%s\n\n", styleOK.Render("▶"), k8sJob.Name)

	k8scfg, err := clientcmd.BuildConfigFromFlags("", flagRestoreKubeconfig)
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

// singleJobSelectorModel lets the user pick one job from a list.
type singleJobSelectorModel struct {
	jobs   []string
	cursor int
	chosen string
}

func (m singleJobSelectorModel) Init() tea.Cmd { return nil }

func (m singleJobSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
			}
		case "enter":
			m.chosen = m.jobs[m.cursor]
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m singleJobSelectorModel) View() string {
	var b strings.Builder
	b.WriteString("Select a job to restore:\n")
	b.WriteString(styleSubtext.Render("  ↑/↓ navigate   enter confirm   q cancel") + "\n\n")
	for i, name := range m.jobs {
		cursor := "  "
		if i == m.cursor {
			cursor = styleCursor.Render("> ")
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, name))
	}
	return b.String()
}

func runSingleJobSelector(jobs []config.JobConfig) (string, error) {
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	result, err := tea.NewProgram(singleJobSelectorModel{jobs: names}).Run()
	if err != nil {
		return "", err
	}
	return result.(singleJobSelectorModel).chosen, nil
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
		// Show just the filename + date + size
		parts := strings.Split(obj.Key, "/")
		filename := parts[len(parts)-1]
		size := formatSize(obj.Size)
		line := fmt.Sprintf("%-40s  %s  %s", filename, obj.LastModified, size)
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, line))
	}
	return b.String()
}

func runBackupSelector(objects []storage.BackupObject) (string, error) {
	// Show most recent first
	reversed := make([]storage.BackupObject, len(objects))
	for i, o := range objects {
		reversed[len(objects)-1-i] = o
	}
	m := backupSelectorModel{items: reversed}
	result, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	return result.(backupSelectorModel).chosen, nil
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

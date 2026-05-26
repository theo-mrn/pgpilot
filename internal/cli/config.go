package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/config"
	"github.com/theomorin/dbpilot/internal/detect"
	"github.com/theomorin/dbpilot/internal/k8s"
	"github.com/theomorin/dbpilot/internal/storage"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage backup configurations",
}

// ---------- config list ----------

var configListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List all configurations",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := config.ConfigDir()
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		var found []os.DirEntry
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
				found = append(found, e)
			}
		}
		if len(found) == 0 {
			fmt.Println("No configurations found. Run 'dbpilot config create <name>' to get started.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tJOBS\tPATH")
		for _, e := range found {
			name := strings.TrimSuffix(e.Name(), ".yaml")
			path := filepath.Join(dir, e.Name())
			jobs := "—"
			if cfg, err := config.Load(path); err == nil {
				jobs = fmt.Sprintf("%d", len(cfg.Jobs))
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", name, jobs, path)
		}
		w.Flush()
		return nil
	},
}

// ---------- config delete ----------

var configDeleteCmd = &cobra.Command{
	Use:          "delete <name>",
	Short:        "Delete a configuration",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := config.NamedPath(args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("config %q not found", args[0])
		}
		fmt.Printf("Delete config %q (%s)? [y/N] ", args[0], path)
		var ans string
		fmt.Scanln(&ans)
		if strings.ToLower(ans) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Printf("%s  Config %q deleted.\n", styleOK.Render("✓"), args[0])
		return nil
	},
}

// ---------- config storage ----------

var flagStorageKubeconfig string

var configStorageCmd = &cobra.Command{
	Use:          "storage <name>",
	Short:        "Reconfigure S3 storage for a config",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE:         runConfigStorage,
}

func runConfigStorage(cmd *cobra.Command, args []string) error {
	name := args[0]
	path, err := config.NamedPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config %q not found — run 'dbpilot config create %s' first", name, name)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if len(cfg.Jobs) == 0 {
		return fmt.Errorf("config %q has no jobs", name)
	}

	// Select which jobs to reconfigure
	selected, err := runJobSelector(cfg.Jobs)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		fmt.Println("No jobs selected.")
		return nil
	}
	fmt.Printf("\nConfiguring storage for %d job(s):\n", len(selected))
	for _, n := range selected {
		fmt.Printf("  • %s\n", n)
	}
	fmt.Println()

	setup, err := AskDestination()
	if err != nil {
		return fmt.Errorf("destination setup: %w", err)
	}

	fmt.Print("  Checking bucket access... ")
	if err := storage.CheckBucket(setup.Destination.Bucket, setup.AccessKey, setup.SecretKey, setup.Destination.Region, setup.Destination.Endpoint); err != nil {
		fmt.Println(styleErr.Render("✗"))
		return err
	}
	fmt.Println(styleOK.Render("✓"))

	secretName := "s3-creds-" + sanitizeName(setup.Destination.Bucket)
	fmt.Printf("  Storing S3 credentials in K8s Secret %q... ", secretName)
	if err := k8s.StoreS3CredentialsNamed(flagStorageKubeconfig, secretName, setup.AccessKey, setup.SecretKey); err != nil {
		return fmt.Errorf("storing S3 credentials: %w", err)
	}
	fmt.Println(styleOK.Render("✓"))

	selectedSet := make(map[string]bool, len(selected))
	for _, n := range selected {
		selectedSet[n] = true
	}
	for i := range cfg.Jobs {
		if !selectedSet[cfg.Jobs[i].Name] {
			continue
		}
		newDest := config.DestinationConfig{
			Name:        "primary",
			Type:        "s3",
			Bucket:      setup.Destination.Bucket,
			Endpoint:    setup.Destination.Endpoint,
			Region:      setup.Destination.Region,
			S3AccessKey: config.SecretRef{From: fmt.Sprintf("k8s-secret://dbpilot/%s#access_key", secretName)},
			S3SecretKey: config.SecretRef{From: fmt.Sprintf("k8s-secret://dbpilot/%s#secret_key", secretName)},
		}
		updated := false
		for j, dest := range cfg.Jobs[i].Destinations {
			if dest.Name == "primary" || j == 0 {
				cfg.Jobs[i].Destinations[j] = newDest
				updated = true
				break
			}
		}
		if !updated {
			cfg.Jobs[i].Destinations = append(cfg.Jobs[i].Destinations, newDest)
		}
	}

	if err := config.Write(cfg, path, true); err != nil {
		return err
	}
	fmt.Printf("\n%s  Storage updated. Run 'dbpilot deploy %s' to apply.\n", styleOK.Render("✓"), name)
	return nil
}

// ---------- config create ----------

var flagCreateKubeconfig string
var flagCreateVerbose bool
var flagCreateForce bool

var configCreateCmd = &cobra.Command{
	Use:          "create <name>",
	Short:        "Create a new backup configuration",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE:         runConfigCreate,
}

func runConfigCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	outPath, err := config.NamedPath(name)
	if err != nil {
		return err
	}
	if !flagCreateForce {
		if _, err := os.Stat(outPath); err == nil {
			return fmt.Errorf("config %q already exists\nRun with -f to overwrite.", name)
		}
	}

	// 1. Scan Kubernetes
	fmt.Println("Scanning Kubernetes...")
	instances, err := detect.ScanKubernetes(flagCreateKubeconfig, flagCreateVerbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  kubernetes: %v\n", err)
	}
	if len(instances) == 0 {
		return fmt.Errorf("no database instances detected")
	}

	// 2. Select instances
	selected, err := RunSelector(instances)
	if err != nil {
		return fmt.Errorf("selection: %w", err)
	}
	if len(selected) == 0 {
		fmt.Println("No instances selected.")
		return nil
	}
	fmt.Printf("\n%d instance(s) selected:\n", len(selected))
	for _, inst := range selected {
		fmt.Printf("  %s %s (%s)\n", styleOK.Render("✓"), inst.DisplayName, inst.Engine)
	}
	fmt.Println()

	// 3. Configure storage
	setup, err := AskDestination()
	if err != nil {
		return fmt.Errorf("destination setup: %w", err)
	}
	fmt.Print("  Checking bucket access... ")
	if err := storage.CheckBucket(setup.Destination.Bucket, setup.AccessKey, setup.SecretKey, setup.Destination.Region, setup.Destination.Endpoint); err != nil {
		fmt.Println(styleErr.Render("✗"))
		return err
	}
	fmt.Println(styleOK.Render("✓"))

	fmt.Print("  Storing S3 credentials in K8s Secret... ")
	if err := k8s.StoreS3Credentials(flagCreateKubeconfig, setup.AccessKey, setup.SecretKey); err != nil {
		return fmt.Errorf("storing S3 credentials: %w", err)
	}
	fmt.Println(styleOK.Render("✓"))

	// 4. Encryption key
	keyResult, err := GenerateAndConfirmKey()
	if err != nil {
		return err
	}
	fmt.Print("  Storing private key in K8s Secret... ")
	if err := k8s.StoreAgePrivateKey(flagCreateKubeconfig, keyResult.PrivateKey); err != nil {
		return fmt.Errorf("storing age key: %w", err)
	}
	fmt.Println(styleOK.Render("✓"))

	// 5. Generate config
	cfg := config.Generate(selected, keyResult.PublicKey, setup.Destination,
		"k8s-secret://dbpilot/s3-credentials#access_key",
		"k8s-secret://dbpilot/s3-credentials#secret_key",
	)
	credRefs, err := ResolveDBCredentials(flagCreateKubeconfig, selected)
	if err != nil {
		return err
	}
	ApplyDBCredentials(&cfg, credRefs)

	if err := config.Write(cfg, outPath, flagCreateForce); err != nil {
		return err
	}
	fmt.Printf("\n%s  Config %q created.\n", styleOK.Render("✓"), name)
	fmt.Println("  Deploy when ready: dbpilot deploy " + name)
	return nil
}

// ---------- config jobs ----------

var configJobsCmd = &cobra.Command{
	Use:          "jobs <name>",
	Short:        "List jobs in a configuration",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := config.NamedPath(args[0])
		if err != nil {
			return err
		}
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		if len(cfg.Jobs) == 0 {
			fmt.Printf("Config %q has no jobs.\n", args[0])
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "JOB\tNAMESPACE\tSCHEDULE\tDESTINATIONS")
		for _, j := range cfg.Jobs {
			dests := fmt.Sprintf("%d", len(j.Destinations))
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", j.Name, j.Environment.Namespace, j.Schedule, dests)
		}
		w.Flush()
		return nil
	},
}

// ---------- helpers ----------

func sanitizeName(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func runJobSelector(jobs []config.JobConfig) ([]string, error) {
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	m := jobSelectorModel{
		jobs:    names,
		checked: make([]bool, len(names)),
	}
	for i := range m.checked {
		m.checked[i] = true
	}
	result, err := tea.NewProgram(m).Run()
	if err != nil {
		return nil, err
	}
	final := result.(jobSelectorModel)
	var selected []string
	for i, name := range final.jobs {
		if final.checked[i] {
			selected = append(selected, name)
		}
	}
	return selected, nil
}

// jobSelectorModel is a multi-select list of job names.
type jobSelectorModel struct {
	jobs    []string
	checked []bool
	cursor  int
}

func (m jobSelectorModel) Init() tea.Cmd { return nil }

func (m jobSelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			for i := range m.checked {
				m.checked[i] = false
			}
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
			}
		case " ":
			m.checked[m.cursor] = !m.checked[m.cursor]
		case "enter":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m jobSelectorModel) View() string {
	var b strings.Builder
	b.WriteString("Select jobs to configure storage for:\n")
	b.WriteString(styleSubtext.Render("  ↑/↓ navigate   space toggle   enter confirm") + "\n\n")
	for i, name := range m.jobs {
		cursor := "  "
		if i == m.cursor {
			cursor = styleCursor.Render("> ")
		}
		checkbox := "[ ]"
		if m.checked[i] {
			checkbox = styleSelected.Render("[✓]")
		}
		b.WriteString(fmt.Sprintf("%s%s  %s\n", cursor, checkbox, name))
	}
	return b.String()
}

func init() {
	home, _ := os.UserHomeDir()
	configCreateCmd.Flags().StringVar(&flagCreateKubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")
	configCreateCmd.Flags().BoolVarP(&flagCreateVerbose, "verbose", "v", false, "show low confidence candidates")
	configCreateCmd.Flags().BoolVarP(&flagCreateForce, "force", "f", false, "overwrite if config already exists")
	configStorageCmd.Flags().StringVar(&flagStorageKubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")

	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configDeleteCmd)
	configCmd.AddCommand(configStorageCmd)
	configCmd.AddCommand(configCreateCmd)
	configCmd.AddCommand(configJobsCmd)
}

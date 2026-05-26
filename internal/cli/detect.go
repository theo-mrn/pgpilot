package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/config"
	"github.com/theomorin/dbpilot/internal/detect"
	"github.com/theomorin/dbpilot/internal/k8s"
	"github.com/theomorin/dbpilot/internal/storage"
)

var detectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Scan the environment and detect running database instances",
	Long: `Scans Kubernetes (current kubeconfig context), Docker, and systemd
to find running database instances that dbpilot can back up.

By default, only high and medium confidence instances are shown.
Use --verbose to also display low confidence candidates.`,
	RunE:          runDetect,
	SilenceUsage:  true,
}

var flagKubeconfig string
var flagVerbose bool
var flagForce bool

func init() {
	home, _ := os.UserHomeDir()
	defaultKubeconfig := filepath.Join(home, ".kube", "config")
	detectCmd.Flags().StringVar(&flagKubeconfig, "kubeconfig", defaultKubeconfig, "path to kubeconfig file")
	detectCmd.Flags().BoolVarP(&flagVerbose, "verbose", "v", false, "show low confidence candidates")
	detectCmd.Flags().BoolVarP(&flagForce, "force", "f", false, "overwrite existing backup.yaml")
}

func runDetect(cmd *cobra.Command, args []string) error {
	fmt.Println("Scanning Kubernetes...")
	instances, err := detect.ScanKubernetes(flagKubeconfig, flagVerbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  kubernetes: %v\n", err)
	}

	if len(instances) == 0 {
		fmt.Println("No database instances detected.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "\tINSTANCE\tENGINE\tSCORE")
	fmt.Fprintln(w, "\t--------\t------\t-----")
	for _, inst := range instances {
		supported := inst.Engine == detect.EnginePostgres
		marker := "✓"
		if !supported {
			marker = "✗"
		} else if inst.Confidence == detect.ConfidenceMedium {
			marker = "⚠"
		} else if inst.Confidence == detect.ConfidenceLow {
			marker = "~"
		}
		engineLabel := string(inst.Engine)
		if !supported {
			engineLabel += " (not supported yet)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d (%s)\n",
			marker,
			inst.DisplayName,
			engineLabel,
			inst.Score,
			inst.Confidence,
		)
		signals := strings.Join(inst.DetectionSignals, ", ")
		if len(signals) > 80 {
			signals = signals[:77] + "..."
		}
		fmt.Fprintf(w, "\t\t\t  signals: %s\n", signals)
	}
	w.Flush()

	fmt.Printf("\n%d instance(s) detected.\n\n", len(instances))

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
		fmt.Printf("  ✓ %s (%s)\n", inst.DisplayName, inst.Engine)
	}

	// Check output path before generating keys — avoid generating a new key
	// if the file already exists and --force was not passed.
	outPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	if !flagForce {
		if _, err := os.Stat(outPath); err == nil {
			fmt.Printf("\n%s already exists.\nRun with -f to overwrite.\n", outPath)
			return nil
		}
	}

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
	if err := k8s.StoreS3Credentials(flagKubeconfig, setup.AccessKey, setup.SecretKey); err != nil {
		return fmt.Errorf("storing S3 credentials: %w", err)
	}
	fmt.Println("✓")

	keyResult, err := GenerateAndConfirmKey()
	if err != nil {
		return err
	}

	fmt.Print("  Storing private key in K8s Secret... ")
	if err := k8s.StoreAgePrivateKey(flagKubeconfig, keyResult.PrivateKey); err != nil {
		return fmt.Errorf("storing age key: %w", err)
	}
	fmt.Println("✓")

	cfg := config.Generate(selected, keyResult.PublicKey, setup.Destination)

	credRefs, err := ResolveDBCredentials(flagKubeconfig, selected)
	if err != nil {
		return err
	}
	ApplyDBCredentials(&cfg, credRefs)

	if err := config.Write(cfg, outPath, flagForce); err != nil {
		return err
	}
	fmt.Printf("\n✓ Written to %s — review and edit before use.\n", outPath)
	return nil
}

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
	Use:          "deploy <name>",
	Short:        "Deploy backup CronJobs to Kubernetes",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE:         runDeploy,
}

var flagDeployKubeconfig string
var flagDryRun bool

func init() {
	home, _ := os.UserHomeDir()
	deployCmd.Flags().StringVar(&flagDeployKubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "path to kubeconfig file")
	deployCmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "show what would be deployed without applying")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	path, err := config.NamedPath(args[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	errs := config.Validate(cfg)
	if len(errs) > 0 {
		fmt.Printf("Config %q is invalid:\n", args[0])
		for _, e := range errs {
			fmt.Printf("  %s  %s\n", styleErr.Render("✗"), e.Error())
		}
		return fmt.Errorf("aborting deploy")
	}

	if flagDryRun {
		fmt.Println("Dry run — no changes will be applied.\n")
	}

	fmt.Printf("Deploying %d job(s) from config %q...\n\n", len(cfg.Jobs), args[0])

	results, err := k8s.DeployBackupJobs(flagDeployKubeconfig, cfg, flagDryRun)
	if err != nil {
		return err
	}

	for _, r := range results {
		fmt.Printf("  %s  %s  (%s/%s)\n", styleOK.Render("✓"), r.JobName, r.Namespace, r.Action)
	}

	fmt.Printf("\n%d CronJob(s) deployed.\n", len(results))
	return nil
}

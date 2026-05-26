package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/k8s"
)

var version = "dev"

func SetVersion(v string) { version = v }

var rootCmd = &cobra.Command{
	Use:   "dbpilot",
	Short: "PostgreSQL backup orchestrator for Kubernetes",
	Long: `dbpilot automates PostgreSQL backups to S3-compatible storage via Kubernetes CronJobs.
Zero infrastructure changes — no sidecars, no pod mutations, no GitOps conflicts.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// k8sReadSecretInternal is used by backup.go to read S3 credentials from K8s.
func k8sReadSecretInternal(kubeconfig, ref string) (string, error) {
	return k8s.ReadSecret(kubeconfig, ref)
}

func init() {
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(backupCmd)
	rootCmd.AddCommand(restoreCmd)
	rootCmd.AddCommand(deployCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run:   func(cmd *cobra.Command, args []string) { fmt.Println(version) },
	})
}

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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

func init() {
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(deployCmd)
	rootCmd.AddCommand(restoreCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(backupCmd)
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run:   func(cmd *cobra.Command, args []string) { fmt.Println(version) },
	})
}

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "dbpilot",
	Short: "Database backup orchestrator for heterogeneous environments",
	Long: `dbpilot automates database backups across Kubernetes, Docker, and systemd environments.
It uses WAL-G as its primary backup engine and supports PostgreSQL, MySQL, and MongoDB.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(detectCmd)
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(deployCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(backupCmd)
}

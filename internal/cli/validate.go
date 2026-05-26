package cli

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/config"
)

var styleOK = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
var styleErr = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)

var validateCmd = &cobra.Command{
	Use:          "validate",
	Short:        "Validate backup.yaml configuration",
	SilenceUsage: true,
	RunE:         runValidate,
}

var flagValidatePath string

func init() {
	defaultPath, _ := config.DefaultPath()
	validateCmd.Flags().StringVarP(&flagValidatePath, "config", "c", defaultPath, "path to backup.yaml")
}

func runValidate(cmd *cobra.Command, args []string) error {
	fmt.Printf("Validating %s...\n\n", flagValidatePath)

	cfg, err := config.Load(flagValidatePath)
	if err != nil {
		fmt.Println(styleErr.Render("✗ " + err.Error()))
		return err
	}

	errs := config.Validate(cfg)
	if len(errs) == 0 {
		fmt.Printf("%s  %d job(s) — all valid\n", styleOK.Render("✓"), len(cfg.Jobs))
		return nil
	}

	for _, e := range errs {
		fmt.Printf("  %s  %s\n", styleErr.Render("✗"), e.Error())
	}
	fmt.Printf("\n%d error(s) found.\n", len(errs))
	return fmt.Errorf("invalid configuration")
}

package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/theomorin/dbpilot/internal/config"
)

var validateCmd = &cobra.Command{
	Use:          "validate <name>",
	Short:        "Validate a backup configuration",
	SilenceUsage: true,
	Args:         cobra.ExactArgs(1),
	RunE:         runValidate,
}

func runValidate(cmd *cobra.Command, args []string) error {
	path, err := config.NamedPath(args[0])
	if err != nil {
		return err
	}

	fmt.Printf("Validating config %q...\n\n", args[0])

	cfg, err := config.Load(path)
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

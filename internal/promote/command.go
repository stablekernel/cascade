package promote

import (
	"github.com/spf13/cobra"
	"github.com/stablekernel/cascade/internal/config"
)

// Shared flags across subcommands
var (
	configPath    string
	dryRun        bool
	jsonOutput    bool
	ghaOutput     bool
	actor         string
	componentName string
)

// NewCommand creates the promote parent command with subcommands.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Environment promotion commands",
		Long: `Commands for managing environment promotions.

Subcommands:
  preflight  - Validate and plan promotion (outputs to $GITHUB_OUTPUT)
  finalize   - Update state after deploys complete

The preflight and finalize subcommands are designed for GitHub Actions workflows.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Auto-detect config file if not specified
			if configPath == "" {
				configPath = config.FindConfigFile("")
			}
			return nil
		},
	}

	// Shared flags on parent command
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Preview changes without modifying state")
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output result as JSON")
	cmd.PersistentFlags().BoolVar(&ghaOutput, "gha-output", false, "Write to $GITHUB_OUTPUT")
	cmd.PersistentFlags().StringVar(&actor, "actor", "", "Actor performing action (default: from GITHUB_ACTOR)")
	cmd.PersistentFlags().StringVar(&componentName, "component", "", "Declared component to scope promotion state to (default: single-component)")

	// Add subcommands
	cmd.AddCommand(newPreflightCommand())
	cmd.AddCommand(newFinalizeCommand())

	return cmd
}

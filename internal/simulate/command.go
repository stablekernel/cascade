package simulate

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/promote"
)

// flags shared across the simulate subcommands.
var (
	flagConfig string
	flagJSON   bool
	flagActor  string
)

// promote subcommand flags.
var (
	flagMode   string
	flagTarget string
)

const simulateLong = `Run a hypothetical action against a clone of your manifest and print what
would happen, without changing anything.

The engine replays the real orchestration logic (the same state transitions
cascade uses to promote environments) in record-only mode. It validates
ORCHESTRATION, meaning the state transitions, not your real deploy scripts.
It touches no GitHub and no containers, and it mutates no on-disk state: the
manifest is copied to a temp file, the transition is computed against that
copy, and the copy is discarded.

The output has two parts: a before and after state diff, and an ordered
effect sequence describing each step the orchestration would take.`

const promoteLong = `Simulate a promotion against a clone of your manifest.

This replays the real promotion state-machine in record-only mode and prints
the resulting state diff plus an ordered effect sequence. It validates the
orchestration transitions, not your deploy scripts, and touches no GitHub and
no containers. No on-disk state is changed.`

// NewCommand builds the simulate parent command and its subcommands.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "simulate",
		Short: "Preview a hypothetical action without changing anything",
		Long:  simulateLong,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if flagConfig == "" {
				flagConfig = config.FindConfigFile("")
			}
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&flagConfig, "config", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "Output result as JSON")
	cmd.PersistentFlags().StringVar(&flagActor, "actor", "", "Actor performing the hypothetical action")

	cmd.AddCommand(newPromoteCommand())

	return cmd
}

// newPromoteCommand builds the `simulate promote` subcommand.
func newPromoteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Simulate a promotion",
		Long:  promoteLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := parseMode(flagMode)
			if err != nil {
				return err
			}

			engine, err := NewEngine(flagConfig, WithActor(flagActor))
			if err != nil {
				return err
			}

			result, err := engine.Simulate(NewPromoteAction(mode, flagTarget))
			if err != nil {
				return err
			}

			if flagJSON {
				return result.RenderJSON(os.Stdout)
			}
			return result.RenderHuman(os.Stdout)
		},
	}

	cmd.Flags().StringVar(&flagMode, "mode", "default", "Promotion mode: default or cascade")
	cmd.Flags().StringVar(&flagTarget, "target", "", "Cascade target (for example dev-to-prod)")

	return cmd
}

// parseMode maps the flag string to a promote.PromotionMode.
func parseMode(s string) (promote.PromotionMode, error) {
	switch s {
	case string(promote.ModeDefault):
		return promote.ModeDefault, nil
	case string(promote.ModeCascade):
		return promote.ModeCascade, nil
	default:
		return "", fmt.Errorf("invalid mode %q: want default or cascade", s)
	}
}

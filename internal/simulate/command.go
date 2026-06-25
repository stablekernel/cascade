package simulate

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/promote"
)

// commonFlags holds the flags shared by every simulate subcommand. They are
// bound per NewCommand invocation rather than as package globals so concurrent
// command construction (for example parallel tests) never races on shared state.
type commonFlags struct {
	config        string
	json          bool
	actor         string
	deployResults []string
}

// engineOptions builds the engine options shared by every subcommand from the
// common flags, parsing the repeatable --deploy-result pairs.
func (cf *commonFlags) engineOptions() ([]Option, error) {
	opts := []Option{WithActor(cf.actor)}
	outcomes, err := ParseDeployResults(cf.deployResults)
	if err != nil {
		return nil, err
	}
	if len(outcomes) > 0 {
		opts = append(opts, WithDeployResults(outcomes))
	}
	return opts, nil
}

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
	cf := &commonFlags{}

	cmd := &cobra.Command{
		Use:   "simulate",
		Short: "Preview a hypothetical action without changing anything",
		Long:  simulateLong,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cf.config == "" {
				cf.config = config.FindConfigFile("")
			}
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&cf.config, "config", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.PersistentFlags().BoolVar(&cf.json, "json", false, "Output result as JSON")
	cmd.PersistentFlags().StringVar(&cf.actor, "actor", "", "Actor performing the hypothetical action")
	cmd.PersistentFlags().StringArrayVar(&cf.deployResults, "deploy-result", nil, "Simulated outcome for a build or deploy callback, name=success|failure|skipped (repeatable)")

	cmd.AddCommand(newPromoteCommand(cf))
	cmd.AddCommand(newRollbackCommand(cf))
	cmd.AddCommand(newReleaseCommand(cf))
	cmd.AddCommand(newHotfixCommand(cf))

	return cmd
}

// runSimulation builds the engine and renders the action result, shared by every
// subcommand so output formatting stays identical across actions.
func runSimulation(cf *commonFlags, a Action) error {
	opts, err := cf.engineOptions()
	if err != nil {
		return err
	}
	engine, err := NewEngine(cf.config, opts...)
	if err != nil {
		return err
	}

	result, err := engine.Simulate(a)
	if err != nil {
		return err
	}

	if cf.json {
		return result.RenderJSON(os.Stdout)
	}
	return result.RenderHuman(os.Stdout)
}

// newPromoteCommand builds the `simulate promote` subcommand.
func newPromoteCommand(cf *commonFlags) *cobra.Command {
	var (
		mode   string
		target string
	)

	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Simulate a promotion",
		Long:  promoteLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := parseMode(mode)
			if err != nil {
				return err
			}
			return runSimulation(cf, NewPromoteAction(m, target))
		},
	}

	cmd.Flags().StringVar(&mode, "mode", "default", "Promotion mode: default or cascade")
	cmd.Flags().StringVar(&target, "target", "", "Cascade target (for example dev-to-prod)")

	return cmd
}

const rollbackLong = `Simulate a rollback against a clone of your manifest.

This replays the real rollback target resolution and state revert in record-only
mode and prints the resulting state diff plus an ordered effect sequence. Target
resolution is pinned to the in-state deploy-history ring. It validates the
orchestration transitions, not your deploy scripts.
It touches no GitHub and no containers. No on-disk state is changed.`

const releaseLong = `Simulate a release crossing against a clone of your manifest.

This replays the real promotion state-machine across the release boundary in
record-only mode and prints the state diff plus an ordered effect sequence,
including the prerelease or publish marker the orchestration would emit. It
validates the orchestration transitions and the release decision, not the GitHub
release API calls, and touches no GitHub and no containers. No on-disk state is
changed.`

const hotfixLong = `Simulate a hotfix against a clone of your manifest.

This replays the real hotfix finalize state-machine in record-only mode: it
allocates the next hotfix version, snapshots the prior state, and writes the
divergence fields, then prints the resulting state diff plus an ordered effect
sequence. It validates the orchestration transitions, not your deploy scripts,
and touches no GitHub and no containers. No on-disk state is changed.`

// newRollbackCommand builds the `simulate rollback` subcommand.
func newRollbackCommand(cf *commonFlags) *cobra.Command {
	var (
		env        string
		to         string
		deployable string
	)

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Simulate a rollback",
		Long:  rollbackLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			if env == "" {
				return fmt.Errorf("rollback requires --env")
			}
			return runSimulation(cf, NewRollbackAction(env, to, deployable))
		},
	}

	cmd.Flags().StringVar(&env, "env", "", "Environment to roll back")
	cmd.Flags().StringVar(&to, "to", "", "Target SHA or version (default: previous distinct state)")
	cmd.Flags().StringVar(&deployable, "deployable", "", "Scope the rollback to a single deployable")

	return cmd
}

// newReleaseCommand builds the `simulate release` subcommand.
func newReleaseCommand(cf *commonFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "release",
		Short: "Simulate a release crossing",
		Long:  releaseLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSimulation(cf, NewReleaseAction())
		},
	}
}

// newHotfixCommand builds the `simulate hotfix` subcommand.
func newHotfixCommand(cf *commonFlags) *cobra.Command {
	var (
		env      string
		fix      string
		mergeSHA string
	)

	cmd := &cobra.Command{
		Use:   "hotfix",
		Short: "Simulate a hotfix",
		Long:  hotfixLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			if env == "" {
				return fmt.Errorf("hotfix requires --env")
			}
			fixSHAs, err := parseCommaList(fix)
			if err != nil {
				return fmt.Errorf("invalid --fix: %w", err)
			}
			return runSimulation(cf, NewHotfixAction(env, fixSHAs, mergeSHA))
		},
	}

	cmd.Flags().StringVar(&env, "env", "", "Environment to hotfix")
	cmd.Flags().StringVar(&fix, "fix", "", "Comma-separated trunk commit SHAs the hotfix carries")
	cmd.Flags().StringVar(&mergeSHA, "merge-sha", "", "Resolution-branch tip (default: first fix commit)")

	return cmd
}

// parseCommaList splits a comma-separated input into a trimmed, non-empty slice.
// It rejects empty input and any blank entry so a hotfix simulation always names
// at least one concrete commit.
func parseCommaList(input string) ([]string, error) {
	if strings.TrimSpace(input) == "" {
		return nil, fmt.Errorf("no commit refs given")
	}
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, raw := range parts {
		ref := strings.TrimSpace(raw)
		if ref == "" {
			return nil, fmt.Errorf("empty commit ref in %q", input)
		}
		out = append(out, ref)
	}
	return out, nil
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

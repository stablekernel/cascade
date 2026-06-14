package rollback

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
)

// NewCommand creates the `cascade rollback` command.
func NewCommand() *cobra.Command {
	var (
		configPath  string
		manifestKey string
		env         string
		to          string
		deployable  string
		actor       string
		dryRun      bool
		jsonOutput  bool
	)

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Re-promote a prior version or SHA to an environment",
		Long: `Re-promote a prior known-good version or SHA to a target environment.

Rollback resolves the target from existing deployment state and, when needed,
the git history of the manifest. It then re-applies that SHA/version to the
environment using the same state-write path as a normal promotion. There is no
separate deploy code path.

Resolution order for --to <version|sha>:
  1. The environment's current recorded state (and per-deployable state).
  2. The environment's deploy-history ring, newest first.
  3. The manifest's git history, newest first (recovers a deployment the
     manifest has already moved past).

When --to is omitted, rollback resolves the previous version: the newest
deploy-history ring entry that differs from the current state, falling back to
the newest distinct prior state from manifest history.

A SHA may be given in full or as a short (>=7 char) prefix. Use --deployable to
scope the rollback to a single deployable's recorded version (requires
per-deployable version tracking in state).

Rollback is read-only until it executes: without --dry-run it writes the
re-promotion to the manifest; with --dry-run it prints the resolved plan and
makes no changes.

Examples:
  cascade rollback --env prod --to v1.2.2
  cascade rollback --env prod --to 111aaa --dry-run
  cascade rollback --env prod --to v1.2.2 --deployable services`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(runOptions{
				configPath:  configPath,
				manifestKey: manifestKey,
				env:         env,
				to:          to,
				deployable:  deployable,
				actor:       actor,
				dryRun:      dryRun,
				jsonOutput:  jsonOutput,
			})
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to manifest file (default: .github/manifest.yaml)")
	cmd.Flags().StringVar(&manifestKey, "key", config.DefaultManifestKey, "Top-level manifest key")
	cmd.Flags().StringVar(&env, "env", "", "Target environment to roll back (required)")
	cmd.Flags().StringVar(&to, "to", "", "Prior version or SHA to re-promote (optional; defaults to the previous version)")
	cmd.Flags().StringVar(&deployable, "deployable", "", "Scope the rollback to a single deployable")
	cmd.Flags().StringVar(&actor, "actor", "", "Actor recorded on the rollback (default: $GITHUB_ACTOR)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Resolve and print the plan without modifying the manifest")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the resolved plan as JSON")

	_ = cmd.MarkFlagRequired("env")

	// The flat root run remains the standalone operator path. The workflow the
	// generator emits drives rollback through these two subcommands instead: a
	// read-only preflight that resolves the target and a finalize that applies the
	// state write and pushes it back to trunk.
	cmd.AddCommand(newPreflightCommand())
	cmd.AddCommand(newFinalizeCommand())

	return cmd
}

type runOptions struct {
	configPath  string
	manifestKey string
	env         string
	to          string
	deployable  string
	actor       string
	dryRun      bool
	jsonOutput  bool
}

func run(opts runOptions) error {
	rb, err := New(Options{
		ConfigPath:  opts.configPath,
		ManifestKey: opts.manifestKey,
		Actor:       opts.actor,
	})
	if err != nil {
		return err
	}

	plan, err := rb.Plan(opts.env, opts.to, opts.deployable)
	if err != nil {
		return err
	}

	if opts.dryRun {
		return report(plan, opts.jsonOutput, true)
	}

	if err := rb.Apply(plan); err != nil {
		return fmt.Errorf("applying rollback: %w", err)
	}
	return report(plan, opts.jsonOutput, false)
}

func report(plan *Plan, jsonOutput, dryRun bool) error {
	if jsonOutput {
		type out struct {
			*Plan
			DryRun bool `json:"dry_run"`
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out{Plan: plan, DryRun: dryRun})
	}

	scope := fmt.Sprintf("environment %s", plan.Environment)
	if plan.Deployable != "" {
		scope = fmt.Sprintf("deployable %s in %s", plan.Deployable, plan.Environment)
	}

	if plan.NoOp {
		fmt.Printf("%s is already at %s (%s); nothing to roll back\n",
			scope, orDash(plan.Target.Version), truncate(plan.Target.SHA))
		return nil
	}

	verb := "rolled back"
	if dryRun {
		verb = "would roll back"
	}
	fmt.Printf("%s %s\n", scope, verb)
	fmt.Printf("  from: %s (%s)\n", orDash(plan.CurrentVersion), truncate(plan.CurrentSHA))
	fmt.Printf("  to:   %s (%s) [resolved from %s]\n",
		orDash(plan.Target.Version), truncate(plan.Target.SHA), plan.Target.Source)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	if sha == "" {
		return "-"
	}
	return sha
}

package status

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/hotfix"
)

// branchLister returns the branch names to check for orphans. The default lists
// the chosen remote's branches; tests inject a fixed set so the consistency
// check is exercised without a real repository.
type branchLister func() ([]string, error)

// branchDeleter removes branch on remote. The default is git.DeleteRemoteBranch,
// whose delete of an absent branch is a no-op so --fix stays idempotent; tests
// inject a recording stub so deletions are asserted without a real repository.
type branchDeleter func(remote, branch string) error

// remoteBranchLister lists the named remote's branches.
func remoteBranchLister(remote string) branchLister {
	return func() ([]string, error) {
		return git.ListRemoteBranches(remote)
	}
}

// consistencyOptions carries the inputs of the consistency check so the flag
// wiring and the testable core agree on one shape as the surface grows.
type consistencyOptions struct {
	configPath string
	key        string
	jsonOutput bool
	fix        bool
	remote     string
	lister     branchLister
	deleter    branchDeleter
}

// newConsistencyCommand creates the 'status consistency' subcommand. It reports
// env/* integration branches that have no matching divergence in the manifest:
// a hotfix leaves an env/<name> branch only while state[<name>] stays diverged,
// so a branch with no diverged env behind it is an orphan from an interrupted
// hotfix or manual meddling and is surfaced here for cleanup. With --fix it also
// deletes those orphans on the remote so the operator can self-serve the cleanup
// the next hotfix preflight would otherwise demand by hand.
func newConsistencyCommand(configPath, key *string, jsonOutput *bool) *cobra.Command {
	var fix bool
	var remote string

	cmd := &cobra.Command{
		Use:   "consistency",
		Short: "Flag (and with --fix, delete) env/* branches with no matching manifest divergence",
		Long: `Check for orphan integration branches.

A hotfix creates an env/<name> branch that exists only while the environment is
diverged. When the environment rejoins trunk the branch is deleted. This command
flags any env/<name> branch that has no matching divergence in the manifest, which
indicates an interrupted hotfix or manual branch creation that should be cleaned up.

By default it only reports. With --fix it deletes each flagged orphan branch on the
remote (default origin). A branch that backs a diverged environment is never touched.
Deleting an already-absent branch is a no-op, so --fix is safe to re-run.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsistency(consistencyOptions{
				configPath: *configPath,
				key:        *key,
				jsonOutput: *jsonOutput,
				fix:        fix,
				remote:     remote,
				lister:     remoteBranchLister(remote),
				deleter:    git.DeleteRemoteBranch,
			})
		},
	}

	cmd.Flags().BoolVar(&fix, "fix", false, "Delete the flagged orphan env/* branches on the remote")
	cmd.Flags().StringVar(&remote, "remote", "origin", "Remote to inspect and, with --fix, delete orphan branches on")

	return cmd
}

// consistencyOutput is the JSON shape for the consistency command. HealedEnvBranches
// is omitted in report-only runs so the default output is unchanged; with --fix it
// lists the orphans deleted so automation can consume what was cleaned up.
type consistencyOutput struct {
	OrphanEnvBranches []string `json:"orphan_env_branches"`
	HealedEnvBranches []string `json:"healed_env_branches,omitempty"`
}

// runConsistency loads the manifest, lists branches via opts.lister, and reports
// the orphan env/* branches. With opts.fix it deletes those orphans on opts.remote
// through opts.deleter and reports what was healed. It is the testable core of the
// consistency subcommand; the lister and deleter are injected so the check runs
// without a repository in tests.
func runConsistency(opts consistencyOptions) error {
	file, err := loadManifest(opts.configPath, opts.key)
	if err != nil {
		return err
	}

	branches, err := opts.lister()
	if err != nil {
		return fmt.Errorf("listing branches: %w", err)
	}

	// The default (empty) component covers the single-component manifest, whose
	// env-keyed state is exactly the default component's env subtree. Per-component
	// consistency scoping arrives with the component-aware finalize path.
	orphans := hotfix.OrphanEnvBranches("", branches, file.State)

	var healed []string
	if opts.fix {
		healed, err = hotfix.HealOrphanEnvBranches("", branches, file.State, opts.remote, opts.deleter)
		if err != nil {
			return fmt.Errorf("healing orphan branches: %w", err)
		}
	}

	if opts.jsonOutput {
		return printJSON(consistencyOutput{OrphanEnvBranches: orphans, HealedEnvBranches: healed})
	}

	if len(orphans) == 0 {
		fmt.Println("no orphan env/* branches found")
		return nil
	}

	fmt.Println("orphan env/* branches (no matching manifest divergence):")
	for _, b := range orphans {
		fmt.Printf("  %s\n", b)
	}

	if opts.fix {
		fmt.Printf("healed env/* branches (deleted on %s):\n", opts.remote)
		for _, b := range healed {
			fmt.Printf("  %s\n", b)
		}
	}
	return nil
}

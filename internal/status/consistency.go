package status

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/hotfix"
)

// branchLister returns the branch names to check for orphans. The default lists
// the origin remote's branches; tests inject a fixed set so the consistency
// check is exercised without a real repository.
type branchLister func() ([]string, error)

// defaultBranchLister lists the origin remote's branches.
func defaultBranchLister() ([]string, error) {
	return git.ListRemoteBranches("origin")
}

// newConsistencyCommand creates the 'status consistency' subcommand. It reports
// env/* integration branches that have no matching divergence in the manifest:
// a hotfix leaves an env/<name> branch only while state[<name>] stays diverged,
// so a branch with no diverged env behind it is an orphan from an interrupted
// hotfix or manual meddling and is surfaced here for cleanup.
func newConsistencyCommand(configPath, key *string, jsonOutput *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consistency",
		Short: "Flag env/* branches with no matching manifest divergence",
		Long: `Check for orphan integration branches.

A hotfix creates an env/<name> branch that exists only while the environment is
diverged. When the environment rejoins trunk the branch is deleted. This command
flags any env/<name> branch that has no matching divergence in the manifest, which
indicates an interrupted hotfix or manual branch creation that should be cleaned up.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsistency(*configPath, *key, *jsonOutput, defaultBranchLister)
		},
	}
	return cmd
}

// consistencyOutput is the JSON shape for the consistency command.
type consistencyOutput struct {
	OrphanEnvBranches []string `json:"orphan_env_branches"`
}

// runConsistency loads the manifest, lists branches via lister, and reports the
// orphan env/* branches. It is the testable core of the consistency subcommand;
// the branch lister is injected so the check runs without a repository in tests.
func runConsistency(configPath, key string, jsonOutput bool, lister branchLister) error {
	file, err := loadManifest(configPath, key)
	if err != nil {
		return err
	}

	branches, err := lister()
	if err != nil {
		return fmt.Errorf("listing branches: %w", err)
	}

	orphans := hotfix.OrphanEnvBranches(branches, file.State)

	if jsonOutput {
		return printJSON(consistencyOutput{OrphanEnvBranches: orphans})
	}

	if len(orphans) == 0 {
		fmt.Println("no orphan env/* branches found")
		return nil
	}

	fmt.Println("orphan env/* branches (no matching manifest divergence):")
	for _, b := range orphans {
		fmt.Printf("  %s\n", b)
	}
	return nil
}

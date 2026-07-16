package promote

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stablekernel/cascade/internal/github"
	"github.com/stablekernel/cascade/internal/globals"
)

var (
	promotionResultJSON string
	repo                string
	runID               string
	commitPush          bool
)

// newFinalizeCommand creates the 'promote finalize' subcommand.
func newFinalizeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "finalize",
		Short: "Update state after promotion deployment",
		Long: `Finalize promotion after deploys complete.

This command:
  1. Queries GitHub Actions job results to determine deploy status
  2. Updates manifest state for successful deploys only
  3. Updates release state if publishing
  4. Commits and pushes state changes

With --gha-output, can write additional outputs to $GITHUB_OUTPUT.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFinalize()
		},
	}

	cmd.Flags().StringVar(&promotionResultJSON, "promotion-result", "", "JSON from preflight output (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository (owner/name) for job query")
	cmd.Flags().StringVar(&runID, "run-id", "", "GitHub Actions run ID for job query")
	cmd.Flags().BoolVar(&commitPush, "commit-push", false, "Commit and push state changes")

	// Mark required flag (error is handled by cobra during Execute())
	_ = cmd.MarkFlagRequired("promotion-result")

	return cmd
}

func runFinalize() error {
	// Parse promotion result
	var promotionResult PromotionResult
	if err := json.Unmarshal([]byte(promotionResultJSON), &promotionResult); err != nil {
		return fmt.Errorf("failed to parse promotion result: %w", err)
	}

	// Determine target environment
	targetEnv := promotionResult.FinalEnv
	if targetEnv == "" && len(promotionResult.Promotions) > 0 {
		// Use the last promotion's environment as target
		targetEnv = promotionResult.Promotions[len(promotionResult.Promotions)-1].Environment
	}

	// Create finalizer. When the workflow committed state back to trunk (a real
	// promotion run, not a dry run), wire the divergence-end lifecycle cleanup so
	// a promotion that rejoins a diverged env removes its integration branch,
	// hotfix tags, and drafts. Without GitHub context the no-op default is kept,
	// so non-diverged promotions and unit-test runs are unaffected.
	var finalizeOpts []FinalizeOption
	if componentName != "" {
		finalizeOpts = append(finalizeOpts, WithComponent(componentName))
	}
	if commitPush {
		if cleaner := newFinalizeCleaner(); cleaner != nil {
			finalizeOpts = append(finalizeOpts, WithLifecycleCleaner(cleaner))
		}
	}

	fin, err := NewFinalizer(configPath, targetEnv, finalizeOpts...)
	if err != nil {
		return err
	}

	// Set actor if provided
	if actor != "" {
		fin.SetActor(actor)
	}

	// Determine deploy job results.
	//
	// Preferred path: read DEPLOY_RESULT_<NAME> env vars set by the workflow
	// generator before invoking finalize. The workflow already knows each
	// deploy job's conclusion via `needs.<job>.result`, so passing it directly
	// is more reliable than the API path and works in any runner (real GH,
	// act+Gitea, forgejo, etc.).
	//
	// Fallback path: shell out to `gh api repos/.../actions/runs/.../jobs`
	// when env vars aren't provided and a repo+runID pair is. Preserves
	// behavior on workflows generated before the env-var path existed.
	var deployNames []string
	if fin.cicdFile.Config != nil {
		for _, deploy := range fin.cicdFile.Config.Deploys {
			deployNames = append(deployNames, deploy.Name)
		}
	}

	if len(deployNames) > 0 {
		envResults := readDeployResultsFromEnv(deployNames)
		switch {
		case len(envResults) > 0:
			for name, conclusion := range envResults {
				fin.SetDeployResult(name, conclusion)
				if !dryRun && jsonOutput {
					fmt.Printf("Deploy %s: %s (from env)\n", name, conclusion)
				}
			}
		case repo != "" && runID != "":
			jobResults, err := github.QueryJobResults(repo, runID, deployNames)
			if err != nil {
				if !dryRun {
					fmt.Printf("Warning: failed to query job results: %v\n", err)
					fmt.Printf("Continuing with default status (skipped) for all deploys\n")
				}
				for _, name := range deployNames {
					fin.SetDeployResult(name, "skipped")
				}
			} else {
				for name, conclusion := range jobResults {
					fin.SetDeployResult(name, conclusion)
					if !dryRun && jsonOutput {
						fmt.Printf("Deploy %s: %s\n", name, conclusion)
					}
				}
			}
		}
	}

	// Set promotion result
	fin.SetPromotionResult(&promotionResult)

	// When any callback declared auto_commits: true the workflow sets
	// AUTO_COMMITS_HEAD_SHA to the post-callback HEAD before invoking finalize.
	// Pass it through so state.<env>.sha records the actual built/deployed commit
	// rather than the triggering SHA.
	if autoSHA := os.Getenv("AUTO_COMMITS_HEAD_SHA"); autoSHA != "" {
		fin.SetHeadSHA(autoSHA)
	}

	// Run finalization. The mutation gate reads globals.DryRun(), the canonical
	// process-wide flag state, so it holds even if the promote tree's local
	// flag binding is ever removed; the local var above only shapes log output.
	if dryRun || globals.DryRun() {
		fmt.Println("Dry run - state would be updated but not written to disk")
		return nil
	}

	if err := persistAndCleanup(fin, commitPush); err != nil {
		return err
	}

	if commitPush {
		fmt.Printf("State updated and committed for %s\n", targetEnv)
	} else {
		fmt.Printf("State updated for %s (not committed)\n", targetEnv)
	}

	return nil
}

// persistAndCleanup runs the finalizer's state update and local manifest write,
// commits the state back to trunk when requested, and only then performs the
// divergence-end lifecycle cleanup. The ordering is load-bearing: cleanup
// deletes the rejoined env's integration branch, hotfix tags, and drafts, and
// the state write that authorizes those deletions must be durable first. If the
// trunk commit fails, no artifact is removed, so trunk never records an env as
// diverged while pointing at an already-deleted integration branch; a later
// successful rerun performs the cleanup. Without commitPush the local write is
// the caller's durable record and cleanup follows it, as before.
func persistAndCleanup(fin *Finalizer, commitPush bool) error {
	if err := fin.Run(); err != nil {
		return err
	}
	if commitPush {
		if err := fin.CommitAndPush(); err != nil {
			return fmt.Errorf("failed to commit and push: %w", err)
		}
	}
	return fin.runLifecycleCleanup()
}

// readDeployResultsFromEnv reads DEPLOY_RESULT_<NAME> env vars and returns a
// map of deploy name → conclusion for each deploy with a non-empty value.
// Hyphens in deploy names are converted to underscores for the env var key,
// matching the existing DEPLOY_<NAME> pattern used by preflight.
func readDeployResultsFromEnv(deployNames []string) map[string]string {
	results := make(map[string]string)
	for _, name := range deployNames {
		key := "DEPLOY_RESULT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if val := os.Getenv(key); val != "" {
			results[name] = val
		}
	}
	return results
}

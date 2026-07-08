package harness

import (
	"context"
	"fmt"
	"strings"
)

// rollbackWorkflowPath returns the generated rollback workflow's path inside the
// repo for a component. An empty component selects the repo-wide
// cascade-rollback.yaml (byte-identical single-component behavior); a named
// component selects the fanned-out cascade-rollback-<name>.yaml the generator
// emits for a manifest with a components: block. It mirrors promoteWorkflowPath.
func rollbackWorkflowPath(component string) string {
	if component == "" {
		return ".github/workflows/cascade-rollback.yaml"
	}
	return ".github/workflows/cascade-rollback-" + component + ".yaml"
}

// executeRollback dispatches the cascade-rollback workflow for an environment.
// It mirrors executePromote: it builds the workflow_dispatch inputs (omitting
// empty optional values the same way promote does), runs the workflow via
// ActRunner, honors ExpectFailure, and syncs the resulting manifest state from
// Gitea so the step's state/divergence assertions observe the rolled-back env.
//
// The rollback workflow re-points the environment at a prior version or SHA
// (resolved by the preflight job from the live state, the previous-deploy ring,
// or manifest history), re-runs the env's deploy jobs at that target SHA, then
// marks the environment diverged (Ref="rollback/<env>") until a forward
// promotion rejoins it.
func (r *Runner) executeRollback(ctx context.Context, rollback *RollbackStep, config Config) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Log("  Would execute rollback workflow (no harness)")
		return nil
	}

	dryRun := "false"
	if rollback.DryRun {
		dryRun = "true"
	}
	r.t.Logf("  Rollback: running %s (env=%s, target=%s, deployable=%s, dry_run=%s)",
		rollbackWorkflowPath(rollback.Component), rollback.Environment, rollback.Target, rollback.Deployable, dryRun)

	// Sync the repo to act container before running workflow.
	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	// Build workflow_dispatch inputs. environment is required; target and
	// deployable are optional and omitted when empty so the workflow falls back
	// to its defaults (target -> previous version, deployable -> all deploys),
	// mirroring how executePromote omits empty optional inputs.
	inputs := map[string]string{
		"environment": rollback.Environment,
		"dry_run":     dryRun,
	}
	if rollback.Target != "" {
		// Target may be a commit reference recorded in earlier steps; resolve to
		// a literal SHA when possible, falling back to the literal (e.g. a version
		// string like "v0.1.0") otherwise.
		inputs["target"] = r.resolveCommit(rollback.Target)
	}
	if rollback.Deployable != "" {
		inputs["deployable"] = rollback.Deployable
	}

	branch := config.TrunkBranch
	if branch == "" {
		branch = "main"
	}

	result, err := r.harness.act.RunWorkflowFromRepo(ctx, RunOpts{
		WorkflowPath: rollbackWorkflowPath(rollback.Component),
		Event:        "workflow_dispatch",
		Inputs:       inputs,
		Env: map[string]string{
			"GITHUB_REF":        fmt.Sprintf("refs/heads/%s", branch),
			"GITHUB_REPOSITORY": fmt.Sprintf("%s/%s", AdminUsername, r.harness.repo.Name),
			// Mark this run as a rollback so a deploy callback can distinguish the
			// rollback re-deploy from a setup promote. Both are dispatched via
			// workflow_dispatch and a reusable callback sees its own name in
			// $GITHUB_WORKFLOW (the callee's, not the caller's), so the caller's
			// workflow name is not a usable signal. act passes top-level --env into
			// every job, including reusable callees, so this is observable in the
			// deploy step. Only the rollback path sets it; promote never does.
			"CASCADE_E2E_ROLLBACK": "1",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to run rollback workflow: %w", err)
	}

	r.lastWorkflowResult = result

	// Handle expected failures (mirrors executePromote's ExpectFailure path).
	if rollback.ExpectFailure {
		if result.Conclusion == "failure" {
			// When ExpectLog is set, assert the failure logs carry the expected
			// marker so the scenario proves the run failed for the intended reason
			// (for example the first-environment guard) and not an unrelated fault.
			if rollback.ExpectLog != "" && !strings.Contains(result.Logs, rollback.ExpectLog) {
				r.t.Logf("  Rollback workflow logs:\n%s", result.Logs)
				return fmt.Errorf("rollback failed as expected but logs did not contain %q", rollback.ExpectLog)
			}
			r.t.Log("  Rollback: workflow failed as expected")
			return nil
		}
		return fmt.Errorf("expected rollback to fail but it succeeded")
	}

	if result.Conclusion != "success" {
		r.t.Logf("  Rollback workflow logs:\n%s", result.Logs)
		return workflowFailureError("rollback", result)
	}

	// Sync state from Gitea so divergence/sha/version assertions see the
	// rolled-back env (the finalize job wrote Ref="rollback/<env>").
	if err := r.syncStateFromGitea(ctx, config); err != nil {
		r.t.Logf("  Warning: failed to sync state from Gitea: %v", err)
	}

	r.t.Logf("  Rollback: workflow completed successfully")

	if rollback.ExpectSource != "" {
		if err := assertRollbackSource(r.t, result.Logs, rollback.ExpectSource); err != nil {
			return err
		}
	}

	return nil
}

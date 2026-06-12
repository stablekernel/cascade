package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// hotfixWorkflowPath is the generated hotfix workflow's path inside the repo.
const hotfixWorkflowPath = ".github/workflows/cascade-hotfix.yaml"

// resolveCommit resolves a commit reference to its SHA via the execution
// context, falling back to treating the reference as a literal SHA.
func (r *Runner) resolveCommit(ref string) string {
	if sha := r.ctx.ResolveSHA(ref); sha != "" {
		return sha
	}
	return ref
}

// shortSHA returns the first 8 characters of a commit SHA, mirroring the hotfix
// workflow's SHORT_SHA computation (internal/generate/hotfix.go).
func shortSHA(sha string) string {
	if len(sha) >= 8 {
		return sha[:8]
	}
	return sha
}

// execInRepo runs a bash script inside /tmp/repo in the act container and
// returns the exit code and combined output. A nil harness/act yields (0, "")
// so callers in unit-test mode are unaffected.
func (r *Runner) execInRepo(ctx context.Context, script string) (int, string, error) {
	exitCode, reader, err := r.harness.act.Container().Exec(ctx,
		[]string{"bash", "-c", "cd /tmp/repo && " + script})
	if err != nil {
		return exitCode, "", err
	}
	var out bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&out, reader)
	}
	return exitCode, out.String(), nil
}

// repoEnv builds the standard workflow environment (GITHUB_REPOSITORY) shared by
// the orchestrate/promote runners.
func (r *Runner) repoEnv() map[string]string {
	return map[string]string{
		"GITHUB_REPOSITORY": fmt.Sprintf("%s/%s", AdminUsername, r.harness.repo.Name),
	}
}

// executeHotfixPlan dispatches the hotfix workflow's plan job for a trunk commit
// and target environment. Plan output parsing is intentionally lenient: the
// branch/base_sha/version-candidate outputs are visible in the run logs, and
// Wave-D scenarios assert observable state rather than scraped step outputs.
func (r *Runner) executeHotfixPlan(ctx context.Context, step *HotfixPlanStep) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Log("  Would execute hotfix plan (no harness)")
		return nil
	}

	sha := r.resolveCommit(step.CommitRef)
	dryRun := "false"
	if step.DryRun {
		dryRun = "true"
	}
	r.t.Logf("  HotfixPlan: commit=%s target=%s dry_run=%s", truncateSHA(sha), step.TargetEnv, dryRun)

	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	result, err := r.harness.act.RunWorkflowFromRepo(ctx, RunOpts{
		WorkflowPath: hotfixWorkflowPath,
		Event:        "workflow_dispatch",
		Inputs: map[string]string{
			"commit":     sha,
			"target_env": step.TargetEnv,
			"dry_run":    dryRun,
		},
		Env: r.repoEnv(),
	})
	if err != nil {
		return fmt.Errorf("failed to run hotfix plan workflow: %w", err)
	}
	r.lastWorkflowResult = result

	if step.ExpectFailure {
		if result.Conclusion == "failure" {
			r.t.Log("  HotfixPlan: workflow failed as expected")
			return nil
		}
		return fmt.Errorf("expected hotfix plan to fail but it succeeded")
	}

	if result.Conclusion != "success" {
		r.t.Logf("  HotfixPlan workflow logs:\n%s", result.Logs)
		return workflowFailureError("hotfix plan", result)
	}

	r.t.Logf("  HotfixPlan: parsed %d jobs", len(result.Jobs))
	for name, job := range result.Jobs {
		r.t.Logf("    - Job '%s': conclusion=%s", name, job.Conclusion)
	}
	return nil
}

// executeHotfixApply performs a harness-driven cherry-pick of a trunk commit onto
// env/<target>, pushing a hotfix branch and opening a labeled PR. It mirrors the
// product workflow's apply recipe (internal/generate/hotfix.go) but runs the git
// mechanics directly so scenarios can exercise both clean and conflict paths.
func (r *Runner) executeHotfixApply(ctx context.Context, step *HotfixApplyStep) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Log("  Would execute hotfix apply (no harness)")
		return nil
	}

	commit := r.resolveCommit(step.CommitRef)
	env := step.TargetEnv
	envBranch := "env/" + env
	short := shortSHA(commit)
	hotfixBranch := "hotfix/" + env + "/" + short
	r.t.Logf("  HotfixApply: commit=%s env=%s branch=%s", truncateSHA(commit), env, hotfixBranch)

	// Ensure env/<env> exists, anchored at the env's recorded state SHA (or HEAD).
	branches, err := r.harness.gitea.ListBranches(ctx, r.harness.repo)
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}
	if !containsString(branches, envBranch) {
		anchor := r.ctx.GetState(env).SHA
		if anchor == "" {
			anchor, err = r.harness.gitea.getHeadSHA(ctx, r.harness.repo)
			if err != nil {
				return fmt.Errorf("get HEAD SHA for env branch anchor: %w", err)
			}
		}
		if err := r.harness.gitea.CreateBranch(ctx, r.harness.repo, envBranch, anchor); err != nil {
			return fmt.Errorf("create env branch %s: %w", envBranch, err)
		}
		r.t.Logf("  HotfixApply: created %s at %s", envBranch, truncateSHA(anchor))
		// Gitea's branch-list endpoint lags a create: wait until the new branch
		// is listed so a later branches.exist assertion (which lists branches)
		// observes it rather than racing the create.
		if err := r.waitForBranchListed(ctx, envBranch, 30*time.Second); err != nil {
			return fmt.Errorf("waiting for env branch %s to be listed: %w", envBranch, err)
		}
	}

	baseSHA, err := r.harness.gitea.GetBranchSHA(ctx, r.harness.repo, envBranch)
	if err != nil {
		return fmt.Errorf("get base SHA for %s: %w", envBranch, err)
	}

	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	// Drive the cherry-pick in the act container. A CONFLICT_FILES sentinel line
	// reports any conflicting paths so we can classify the outcome and build the
	// conflict PR body. The push uses the admin-credentialed origin URL.
	pushURL := r.authedRepoURL()
	short8 := short
	script := strings.Join([]string{
		"set +e",
		// Abort any half-finished cherry-pick left in the shared /tmp/repo by a
		// prior apply in this scenario, then drop any stale local hotfix branch so
		// the re-create below always anchors on the freshly-fetched remote tip.
		"git cherry-pick --abort >/dev/null 2>&1 || true",
		// Force-update the env tracking ref so a second apply onto an env branch
		// the first finalize already advanced (squash-merge) anchors on the
		// current tip rather than a stale ref, otherwise the cherry-pick replays
		// an already-merged change and the push is rejected non-fast-forward.
		fmt.Sprintf("git fetch origin %q --tags >/dev/null 2>&1", "+refs/heads/"+envBranch+":refs/remotes/origin/"+envBranch),
		"git fetch origin '+refs/heads/*:refs/remotes/origin/*' --tags >/dev/null 2>&1",
		fmt.Sprintf("git branch -D %q >/dev/null 2>&1 || true", hotfixBranch),
		fmt.Sprintf("git switch -c %q %q", hotfixBranch, "origin/"+envBranch),
		fmt.Sprintf("git cherry-pick -x %q", commit),
		"CP_EXIT=$?",
		"if [ \"$CP_EXIT\" -ne 0 ]; then",
		"  CONFLICTS=$(git diff --name-only --diff-filter=U | tr '\\n' ' ')",
		"  echo \"CONFLICT_FILES=$CONFLICTS\"",
		"  git add -A",
		fmt.Sprintf("  git -c core.editor=true cherry-pick --continue || git commit -m %q", "hotfix: cherry-pick "+short8+" with conflicts"),
		"else",
		"  echo \"CONFLICT_FILES=\"",
		"fi",
		// Force-push the uniquely-named, per-apply throwaway hotfix branch. The
		// branch name embeds the source short SHA, so a force-push only ever
		// overwrites this apply's own prior attempt (e.g. a retried sync), never a
		// shared branch.
		fmt.Sprintf("git push --force %q %q", pushURL, hotfixBranch+":"+hotfixBranch),
		"echo \"PUSH_EXIT=$?\"",
	}, "\n")

	_, out, err := r.execInRepo(ctx, script)
	if err != nil {
		return fmt.Errorf("cherry-pick exec: %w", err)
	}
	conflictFiles := parseSentinel(out, "CONFLICT_FILES=")
	conflict := strings.TrimSpace(conflictFiles) != ""
	if strings.Contains(out, "PUSH_EXIT=") && !strings.Contains(out, "PUSH_EXIT=0") {
		r.t.Logf("  HotfixApply push output:\n%s", out)
		return fmt.Errorf("failed to push hotfix branch %s", hotfixBranch)
	}

	// Wait out the post-push API staleness window (same class as the B1 flake)
	// before opening the PR, polling until Gitea reports the pushed branch.
	if err := r.waitForBranch(ctx, hotfixBranch, 30*time.Second); err != nil {
		return fmt.Errorf("waiting for pushed branch %s: %w", hotfixBranch, err)
	}

	// Build the PR body with the three product trailers; append the conflict file
	// list on the conflict path.
	body := fmt.Sprintf("Cascade-Hotfix-Target: %s\nCascade-Hotfix-Source: %s\nCascade-Hotfix-Base: %s\n", env, commit, baseSHA)
	title := fmt.Sprintf("hotfix(%s): cherry-pick %s", env, short)
	label := "cascade-hotfix"
	if conflict {
		title = fmt.Sprintf("hotfix(%s): cherry-pick %s (conflicts)", env, short)
		label = "cascade-hotfix-conflict"
		body += "\nConflicting files:\n" + strings.TrimSpace(conflictFiles) + "\n"
	}

	index, err := r.harness.gitea.CreatePR(ctx, r.harness.repo, hotfixBranch, envBranch, title, body, []string{label})
	if err != nil {
		return fmt.Errorf("create hotfix PR: %w", err)
	}

	r.lastPRIndex = index
	r.lastPRConflict = conflict
	r.lastHotfixBranch = hotfixBranch
	r.lastHotfixEnv = env
	r.lastHotfixBody = body
	if r.prByLabel == nil {
		r.prByLabel = make(map[string]int64)
	}
	r.prByLabel[label] = index
	r.t.Logf("  HotfixApply: opened PR #%d (label=%s, conflict=%v)", index, label, conflict)
	return nil
}

// executeMergePR squash-merges an open PR identified by index or label.
func (r *Runner) executeMergePR(ctx context.Context, step *MergePRStep) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Log("  Would execute merge_pr (no harness)")
		return nil
	}

	index := step.Index
	if index <= 0 {
		if idx, ok := r.prByLabel[step.Label]; ok {
			index = idx
		} else {
			indices, err := r.harness.gitea.ListOpenPRs(ctx, r.harness.repo, "", step.Label)
			if err != nil {
				return fmt.Errorf("list open PRs for label %s: %w", step.Label, err)
			}
			if len(indices) == 0 {
				return fmt.Errorf("merge_pr: no open PR found with label %q", step.Label)
			}
			index = indices[0]
		}
	}

	r.t.Logf("  MergePR: squash-merging PR #%d", index)
	if err := r.harness.gitea.MergePR(ctx, r.harness.repo, index, "squash"); err != nil {
		return fmt.Errorf("merge PR #%d: %w", index, err)
	}

	// Record the post-merge env branch tip as "hotfix_head". The squash merge
	// produces a commit that lives only on env/<env>; trunk never merges that
	// branch back, so this SHA is not an ancestor of any trunk commit. Scenarios
	// reference it as an off-trunk patch to exercise the patch-containment guard.
	if r.lastHotfixEnv != "" {
		envBranch := "env/" + r.lastHotfixEnv
		if branchSHA, err := r.harness.gitea.GetBranchSHA(ctx, r.harness.repo, envBranch); err == nil {
			r.ctx.RecordCommit("hotfix_head", branchSHA)
			r.t.Logf("  MergePR: recorded hotfix_head=%s (post-merge env/%s tip)", truncateSHA(branchSHA), r.lastHotfixEnv)
		}
	}
	return nil
}

// executeResolveConflict pushes resolved file content to the last hotfix PR head
// branch (the gitea contents-API equivalent of a force-push that advances the
// head), then replays a pull_request "synchronize" event so the check job runs.
func (r *Runner) executeResolveConflict(ctx context.Context, step *ResolveConflictStep) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Log("  Would execute resolve_conflict (no harness)")
		return nil
	}
	if r.lastHotfixBranch == "" {
		return fmt.Errorf("resolve_conflict: no prior hotfix_apply branch recorded")
	}

	r.t.Logf("  ResolveConflict: committing %d file(s) to %s", len(step.Files), r.lastHotfixBranch)
	if _, err := r.harness.gitea.CreateCommitOnBranch(ctx, r.harness.repo, r.lastHotfixBranch,
		"hotfix: resolve conflicts", step.Files); err != nil {
		return fmt.Errorf("commit conflict resolution: %w", err)
	}

	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	event := map[string]any{
		"action": "synchronize",
		"pull_request": map[string]any{
			"number": r.lastPRIndex,
			"merged": false,
			"base":   map[string]any{"ref": "env/" + r.lastHotfixEnv},
			"head":   map[string]any{"ref": r.lastHotfixBranch},
			"labels": []map[string]any{{"name": "cascade-hotfix-conflict"}},
		},
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal synchronize event: %w", err)
	}

	result, err := r.harness.act.RunWorkflowFromRepo(ctx, RunOpts{
		WorkflowPath: hotfixWorkflowPath,
		Event:        "pull_request",
		EventJSON:    string(eventJSON),
		Env:          r.repoEnv(),
	})
	if err != nil {
		return fmt.Errorf("failed to run check workflow: %w", err)
	}
	r.lastWorkflowResult = result

	// Be lenient on conclusion: jobs gated off this event are skipped, which can
	// surface as a non-success overall conclusion. Surface logs but do not fail
	// the step on conclusion alone; Wave-D scenarios assert observable state.
	if result.Conclusion != "success" {
		r.t.Logf("  ResolveConflict: check run conclusion=%s (non-fatal)\n%s", result.Conclusion, result.Logs)
	}
	return nil
}

// executeHotfixMerged replays the merged pull_request "closed" event for the
// recorded hotfix PR so the context/build/deploy/finalize jobs run. finalize
// invokes the real `cascade hotfix finalize`, writing the diverged state.
func (r *Runner) executeHotfixMerged(ctx context.Context, step *HotfixMergedStep, config Config) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Log("  Would execute hotfix_merged (no harness)")
		return nil
	}

	env := step.TargetEnv
	envBranch := "env/" + env

	// The squash merge advanced env/<env>; its tip is the merge commit SHA.
	mergeSHA, err := r.harness.gitea.GetBranchSHA(ctx, r.harness.repo, envBranch)
	if err != nil {
		return fmt.Errorf("get merge SHA for %s: %w", envBranch, err)
	}

	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	event := map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"number":           r.lastPRIndex,
			"merged":           true,
			"merge_commit_sha": mergeSHA,
			"base":             map[string]any{"ref": envBranch},
			"head":             map[string]any{"ref": r.lastHotfixBranch},
			"labels":           []map[string]any{{"name": "cascade-hotfix"}},
			"body":             r.lastHotfixBody,
		},
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal closed event: %w", err)
	}

	r.t.Logf("  HotfixMerged: replaying merged PR #%d for %s (merge_sha=%s)", r.lastPRIndex, env, truncateSHA(mergeSHA))
	result, err := r.harness.act.RunWorkflowFromRepo(ctx, RunOpts{
		WorkflowPath: hotfixWorkflowPath,
		Event:        "pull_request",
		EventJSON:    string(eventJSON),
		Env:          r.repoEnv(),
	})
	if err != nil {
		return fmt.Errorf("failed to run hotfix merged workflow: %w", err)
	}
	r.lastWorkflowResult = result

	if result.Conclusion != "success" {
		r.t.Logf("  HotfixMerged workflow logs:\n%s", result.Logs)
		return workflowFailureError("hotfix merged", result)
	}

	if err := r.syncStateFromGitea(ctx, config); err != nil {
		r.t.Logf("  Warning: failed to sync state from Gitea: %v", err)
	}
	r.t.Logf("  HotfixMerged: workflow completed successfully")
	return nil
}

// authedRepoURL builds the admin-credentialed origin URL for the test repo,
// mirroring GenerateWorkflows's clone URL construction.
func (r *Runner) authedRepoURL() string {
	externalURL := r.harness.act.GiteaURL()
	host := strings.TrimPrefix(strings.TrimPrefix(externalURL, "http://"), "https://")
	return fmt.Sprintf("http://%s:%s@%s/%s/%s.git",
		AdminUsername, AdminPassword, host, AdminUsername, r.harness.repo.Name)
}

// waitForBranch polls Gitea until it reports the given branch (closing the
// post-push API staleness window) or the timeout elapses.
func (r *Runner) waitForBranch(ctx context.Context, branch string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := r.harness.gitea.GetBranchSHA(ctx, r.harness.repo, branch); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("branch %s not visible before timeout", branch)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// waitForBranchListed polls Gitea's branch-list endpoint until the given branch
// appears or the timeout elapses. GetBranchSHA can resolve a freshly created
// branch before the list endpoint reflects it, so assertions that enumerate
// branches need this stronger wait.
func (r *Runner) waitForBranchListed(ctx context.Context, branch string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		branches, err := r.harness.gitea.ListBranches(ctx, r.harness.repo)
		if err == nil && containsString(branches, branch) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("branch %s not listed before timeout", branch)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// parseSentinel extracts the value following the first occurrence of prefix on
// its own line in out (e.g. "CONFLICT_FILES=a.txt b.txt").
func parseSentinel(out, prefix string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

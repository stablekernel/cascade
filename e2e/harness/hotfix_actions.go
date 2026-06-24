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

// resolveCommits resolves a comma-delimited commit-ref list, resolving each
// entry to its SHA via the execution context and rejoining with commas. It
// mirrors how the plan job forwards a multi-commit dispatch input to
// `cascade hotfix plan --commits`: each ref must be a real SHA the planner can
// rev-parse, so a scenario reference like "commit2,commit3" must resolve to
// "<sha2>,<sha3>" before reaching the workflow. A single ref with no comma
// resolves identically to resolveCommit, keeping single-commit callers stable.
func (r *Runner) resolveCommits(refList string) string {
	parts := strings.Split(refList, ",")
	resolved := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		resolved = append(resolved, r.resolveCommit(part))
	}
	return strings.Join(resolved, ",")
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

	// CommitRef may be a comma-delimited list (the multi-commit chain dispatch
	// input). Resolve each entry to a real SHA so the plan job's
	// `cascade hotfix plan --commits` can rev-parse every commit; a single ref
	// resolves identically to the single-commit path.
	sha := r.resolveCommits(step.CommitRef)
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

// resolveEnvAnchor determines the commit env/<env> must be created at when it
// does not yet exist. The anchor's tree content fully determines whether a later
// cherry-pick applies cleanly or conflicts, so it is resolved deterministically:
//
//  1. an explicit baseRef (resolved via the execution context, falling back to
//     literal) when the scenario pins the base, then
//  2. the env's recorded state SHA.
//
// A silent fallback to trunk HEAD is deliberately NOT used. When the recorded
// SHA is momentarily empty (a gitea state-propagation race), anchoring on trunk
// HEAD would seed env/<env> with the just-patched tip, turning an engineered
// conflict into an empty (clean) cherry-pick and flipping the resulting PR label
// run-to-run. An empty resolution returns an error so the race surfaces loudly
// instead of being masked by a non-deterministic anchor.
func (r *Runner) resolveEnvAnchor(env, baseRef string) (string, error) {
	if baseRef != "" {
		if anchor := r.resolveCommit(baseRef); anchor != "" {
			return anchor, nil
		}
	}
	if anchor := r.ctx.GetState(env).SHA; anchor != "" {
		return anchor, nil
	}
	return "", fmt.Errorf("hotfix_apply: cannot anchor env/%s: no base_ref given and recorded state SHA for %q is empty (likely a gitea state sync race); pin the scenario step's base_ref to make the cherry-pick outcome deterministic", env, env)
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

	// CommitRef may be a comma-delimited set so a single apply (and the single
	// finalize that follows it) carries a multi-commit hotfix, mirroring the
	// product workflow's per-env $COMMITS cherry-pick loop. resolveCommits maps each
	// scenario ref to its SHA and rejoins with commas; a single ref resolves
	// identically to resolveCommit, keeping single-commit scenarios stable.
	commitList := r.resolveCommits(step.CommitRef)
	commits := strings.Split(commitList, ",")
	commit := commits[0]
	env := step.TargetEnv
	envBranch := "env/" + env
	short := shortSHA(commit)
	hotfixBranch := "hotfix/" + env + "/" + short
	r.t.Logf("  HotfixApply: commits=%s env=%s branch=%s", commitList, env, hotfixBranch)

	// Determine whether env/<env> already exists so we know whether to seed it.
	branches, err := r.harness.gitea.ListBranches(ctx, r.harness.repo)
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}
	needsSeedEnvBranch := !containsString(branches, envBranch)

	// Resolve the anchor SHA for env branch seeding. The anchor's tree content
	// determines the cherry-pick outcome: only needed when the branch is absent.
	// resolveEnvAnchor errors (rather than silently using trunk HEAD) when the
	// anchor is unresolvable, surfacing sync races loudly.
	var anchorSHA string
	if needsSeedEnvBranch {
		anchorSHA, err = r.resolveEnvAnchor(env, step.BaseRef)
		if err != nil {
			return err
		}
	}

	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	// Drive the cherry-pick in the act container. A CONFLICT_FILES sentinel line
	// reports any conflicting paths so we can classify the outcome and build the
	// conflict PR body. The push uses the admin-credentialed origin URL.
	pushURL := r.authedRepoURL()
	short8 := short

	// Env branch seed snippet: when env/<env> does not yet exist, push the anchor
	// SHA directly via git rather than the gitea HTTP branches API. The HTTP API
	// accepts old_ref_name as a branch or tag name; passing a raw commit SHA is
	// not reliable across gitea versions (some ignore it and branch from HEAD
	// instead). A git push from inside the act container is precise: git resolves
	// the object by its SHA and the push creates the remote ref at exactly that
	// commit. The act container has the full object database (SyncRepoToActContainer
	// fetches all of main including the anchor commit) so the push always finds the
	// object.
	seedEnvBranchLines := []string{}
	if needsSeedEnvBranch {
		seedEnvBranchLines = []string{
			// Create env/<env> at anchorSHA via git push. --force handles the case
			// where a prior scenario retry created a stale copy.
			fmt.Sprintf("git push --force %q %q", pushURL, anchorSHA+":refs/heads/"+envBranch),
			"echo \"SEED_ENV_EXIT=$?\"",
		}
	}

	scriptLines := []string{
		"set +e",
		// Abort any half-finished cherry-pick left in the shared /tmp/repo by a
		// prior apply in this scenario, then drop any stale local hotfix branch so
		// the re-create below always anchors on the freshly-fetched remote tip.
		"git cherry-pick --abort >/dev/null 2>&1 || true",
	}
	scriptLines = append(scriptLines, seedEnvBranchLines...)
	scriptLines = append(scriptLines,
		// Force-update the env tracking ref so a second apply onto an env branch
		// the first finalize already advanced (squash-merge) anchors on the
		// current tip rather than a stale ref, otherwise the cherry-pick replays
		// an already-merged change and the push is rejected non-fast-forward.
		fmt.Sprintf("git fetch origin %q --tags >/dev/null 2>&1", "+refs/heads/"+envBranch+":refs/remotes/origin/"+envBranch),
		"git fetch origin '+refs/heads/*:refs/remotes/origin/*' --tags >/dev/null 2>&1",
		fmt.Sprintf("git branch -D %q >/dev/null 2>&1 || true", hotfixBranch),
		fmt.Sprintf("git switch -c %q %q", hotfixBranch, "origin/"+envBranch),
		// Cherry-pick every commit in the set onto the hotfix branch, mirroring the
		// product apply loop. On the first conflict, classify and force-commit the
		// partial resolution, then stop; clean sets apply all commits.
		"CONFLICT_FILES=",
		fmt.Sprintf("for commit in %s; do", strings.Join(commits, " ")),
		"  git cherry-pick -x \"$commit\"",
		"  CP_EXIT=$?",
		"  if [ \"$CP_EXIT\" -ne 0 ]; then",
		"    CONFLICT_FILES=$(git diff --name-only --diff-filter=U | tr '\\n' ' ')",
		"    git add -A",
		fmt.Sprintf("    git -c core.editor=true cherry-pick --continue || git commit -m %q", "hotfix: cherry-pick "+short8+" with conflicts"),
		"    break",
		"  fi",
		"done",
		"echo \"CONFLICT_FILES=$CONFLICT_FILES\"",
		// Force-push the uniquely-named, per-apply throwaway hotfix branch. The
		// branch name embeds the source short SHA, so a force-push only ever
		// overwrites this apply's own prior attempt (e.g. a retried sync), never a
		// shared branch.
		fmt.Sprintf("git push --force %q %q", pushURL, hotfixBranch+":"+hotfixBranch),
		"echo \"PUSH_EXIT=$?\"",
	)
	script := strings.Join(scriptLines, "\n")

	_, out, err := r.execInRepo(ctx, script)
	if err != nil {
		return fmt.Errorf("cherry-pick exec: %w", err)
	}
	if needsSeedEnvBranch {
		if strings.Contains(out, "SEED_ENV_EXIT=") && !strings.Contains(out, "SEED_ENV_EXIT=0") {
			r.t.Logf("  HotfixApply seed-env push output:\n%s", out)
			return fmt.Errorf("failed to seed env branch %s at %s", envBranch, truncateSHA(anchorSHA))
		}
		r.t.Logf("  HotfixApply: seeded %s at %s via git push", envBranch, truncateSHA(anchorSHA))
		// Gitea's branch-list endpoint lags a push: wait until the new branch
		// is listed so a later branches.exist assertion (which lists branches)
		// observes it rather than racing the push.
		if err := r.waitForBranchListed(ctx, envBranch, 30*time.Second); err != nil {
			return fmt.Errorf("waiting for seeded env branch %s to be listed: %w", envBranch, err)
		}
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

	// Read the env branch's current tip as the base for the PR trailers. This is
	// read after the script so it reflects the seeded-or-pre-existing branch head.
	baseSHA, err := r.harness.gitea.GetBranchSHA(ctx, r.harness.repo, envBranch)
	if err != nil {
		return fmt.Errorf("get base SHA for %s: %w", envBranch, err)
	}

	// Build the PR body with the three product trailers; append the conflict file
	// list on the conflict path.
	body := fmt.Sprintf("Cascade-Hotfix-Target: %s\nCascade-Hotfix-Source: %s\nCascade-Hotfix-Base: %s\n", env, commitList, baseSHA)
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

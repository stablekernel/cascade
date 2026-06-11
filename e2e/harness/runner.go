package harness

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Runner executes multi-step scenarios
type Runner struct {
	t                  *testing.T
	ctx                *ExecutionContext
	harness            *Harness // The existing harness for infra
	lastWorkflowResult *ExtendedWorkflowResult
}

// NewRunner creates a new scenario runner
func NewRunner(t *testing.T, h *Harness) *Runner {
	return &Runner{
		t:       t,
		ctx:     NewExecutionContext(),
		harness: h,
	}
}

// ValidateScenario validates a scenario before execution
func (r *Runner) ValidateScenario(scenario *MultiStepScenario) error {
	if scenario.Name == "" {
		return fmt.Errorf("scenario must have a name")
	}

	for i, step := range scenario.Steps {
		switch step.Action {
		case "commit":
			if step.Commit == nil {
				return fmt.Errorf("step %d (%s): commit action requires commit config", i, step.Name)
			}
			if step.Commit.Message == "" {
				return fmt.Errorf("step %d (%s): commit must have a message", i, step.Name)
			}
			if len(step.Commit.Files) == 0 {
				return fmt.Errorf("step %d (%s): commit must have files", i, step.Name)
			}
		case "orchestrate":
			// No additional config required
		case "promote":
			if step.Promote == nil {
				return fmt.Errorf("step %d (%s): promote action requires promote config", i, step.Name)
			}
			if step.Promote.Mode != "default" && step.Promote.Mode != "cascade" {
				return fmt.Errorf("step %d (%s): promote mode must be 'default' or 'cascade'", i, step.Name)
			}
			if step.Promote.Mode == "cascade" && step.Promote.Target == "" {
				return fmt.Errorf("step %d (%s): cascade promote requires target", i, step.Name)
			}
		default:
			return fmt.Errorf("step %d (%s): unknown action %q", i, step.Name, step.Action)
		}
	}

	return nil
}

// Run executes a multi-step scenario
func (r *Runner) Run(ctx context.Context, scenario *MultiStepScenario) error {
	if err := r.ValidateScenario(scenario); err != nil {
		return fmt.Errorf("invalid scenario: %w", err)
	}

	r.t.Logf("Running scenario: %s", scenario.Name)

	// Apply initial setup if provided
	if scenario.Setup != nil {
		if err := r.applySetup(ctx, scenario.Setup); err != nil {
			return fmt.Errorf("setup failed: %w", err)
		}
	}

	// Execute each step
	for i, step := range scenario.Steps {
		r.t.Logf("Step %d: %s (%s)", i+1, step.Name, step.Action)

		// Snapshot state before step for "unchanged" assertions
		preState := r.ctx.Clone()

		if err := r.executeStep(ctx, &step, scenario.Config); err != nil {
			return fmt.Errorf("step %d (%s) failed: %w", i+1, step.Name, err)
		}

		// Run assertions if present
		if step.Expect != nil {
			if errs := r.assertStep(ctx, &step, preState); len(errs) > 0 {
				for _, err := range errs {
					r.t.Errorf("  assertion failed: %v", err)
				}
				return fmt.Errorf("step %d (%s) assertions failed", i+1, step.Name)
			}
		}
	}

	return nil
}

// applySetup applies initial state. In addition to recording state/tags/releases
// in the harness's ExecutionContext (used by assertions), the state is committed
// into the repo's manifest.yaml and tags are created in Gitea so that real
// workflows running via ActRunner can read them. Without this, orchestrate's
// version computation can't see prior versions and incorrectly restarts at
// v0.1.0-rc.0 even when the setup specifies a published baseline.
func (r *Runner) applySetup(ctx context.Context, setup *SetupState) error {
	// Apply initial state
	for env, state := range setup.State {
		r.ctx.RecordState(env, state.SHA, state.Version)
		// Stage integration-branch divergence when any divergence field is set.
		// base_sha/patches may be commit references; resolve to literal SHAs.
		if state.Ref != "" || state.BaseSHA != "" || len(state.Patches) > 0 || state.PreviousVersion != "" {
			baseSHA := r.ctx.ResolveSHA(state.BaseSHA)
			if baseSHA == "" {
				baseSHA = state.BaseSHA
			}
			patches := make([]string, 0, len(state.Patches))
			for _, p := range state.Patches {
				if resolved := r.ctx.ResolveSHA(p); resolved != "" {
					patches = append(patches, resolved)
				} else {
					patches = append(patches, p)
				}
			}
			r.ctx.RecordStateDivergence(env, state.Ref, baseSHA, patches, state.PreviousVersion)
		}
	}

	// Apply initial tags
	for _, tag := range setup.Tags {
		r.ctx.RecordTag(tag, true)
	}

	// Apply initial releases
	for _, rel := range setup.Releases {
		r.ctx.RecordRelease(&ReleaseInfo{
			Tag:        rel.Tag,
			Prerelease: rel.Prerelease,
		})
	}

	// Materialize state into the manifest in Gitea so the workflow's CLI
	// invocations see the setup baseline. Skipped when there's no harness
	// (some unit tests construct a Runner without one).
	if r.harness != nil && r.harness.gitea != nil && r.harness.repo != nil {
		if len(setup.State) > 0 {
			if err := r.materializeManifestState(ctx, setup.State); err != nil {
				return fmt.Errorf("materializing manifest state: %w", err)
			}
		}
		if len(setup.Tags) > 0 {
			if err := r.materializeTags(ctx, setup.Tags); err != nil {
				return fmt.Errorf("materializing tags: %w", err)
			}
		}
	}

	return nil
}

// materializeManifestState reads the current manifest from Gitea, merges
// setup state into the ci.state map, and commits the result.
func (r *Runner) materializeManifestState(ctx context.Context, state map[string]*EnvStateSetup) error {
	content, err := r.harness.gitea.GetFileContent(ctx, r.harness.repo, ".github/manifest.yaml")
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	var manifest map[string]any
	if err := yaml.Unmarshal([]byte(content), &manifest); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if manifest == nil {
		manifest = map[string]any{}
	}

	ci, _ := manifest["ci"].(map[string]any)
	if ci == nil {
		ci = map[string]any{}
		manifest["ci"] = ci
	}

	stateMap, _ := ci["state"].(map[string]any)
	if stateMap == nil {
		stateMap = map[string]any{}
		ci["state"] = stateMap
	}

	// Default SHA for env states that don't explicitly specify one. Without
	// this, deploy change-detection (preflight.detectDeployChanges) would see
	// targetSHA == "" and unconditionally include every deploy on the first
	// promotion, bypassing trigger filters.
	defaultSHA, _ := r.harness.gitea.getHeadSHA(ctx, r.harness.repo)
	for env, s := range state {
		entry := map[string]any{
			"version": s.Version,
		}
		switch {
		case s.SHA != "":
			entry["sha"] = s.SHA
		case defaultSHA != "":
			entry["sha"] = defaultSHA
		}
		// Stage integration-branch divergence into the manifest using the
		// product's exact yaml tags (ref/base_sha/patches), so a diverged env
		// exists without a full hotfix run. base_sha/patches may be commit
		// references; resolve to literal SHAs. There is no previous_version
		// manifest key, so PreviousVersion is harness-side only.
		if s.Ref != "" {
			entry["ref"] = s.Ref
		}
		if s.BaseSHA != "" {
			base := r.ctx.ResolveSHA(s.BaseSHA)
			if base == "" {
				base = s.BaseSHA
			}
			entry["base_sha"] = base
		}
		if len(s.Patches) > 0 {
			patches := make([]string, 0, len(s.Patches))
			for _, p := range s.Patches {
				if resolved := r.ctx.ResolveSHA(p); resolved != "" {
					patches = append(patches, resolved)
				} else {
					patches = append(patches, p)
				}
			}
			entry["patches"] = patches
		}
		stateMap[env] = entry
	}

	updated, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	files := map[string]string{
		".github/manifest.yaml": string(updated),
	}
	if _, err := r.harness.gitea.CreateCommit(ctx, r.harness.repo, "test: apply setup state", files); err != nil {
		return fmt.Errorf("commit manifest state: %w", err)
	}
	return nil
}

// materializeTags creates each setup tag in Gitea pointing to the current
// HEAD commit. The exact tag→commit mapping isn't expressed in setup syntax;
// using HEAD matches the behavior of the single-step staging path.
func (r *Runner) materializeTags(ctx context.Context, tags []string) error {
	sha, err := r.harness.gitea.getHeadSHA(ctx, r.harness.repo)
	if err != nil {
		return fmt.Errorf("get HEAD SHA: %w", err)
	}
	for _, tag := range tags {
		if err := r.harness.gitea.CreateTag(ctx, r.harness.repo, tag, sha); err != nil {
			return fmt.Errorf("create tag %s: %w", tag, err)
		}
	}
	return nil
}

// executeStep executes a single step
func (r *Runner) executeStep(ctx context.Context, step *Step, config Config) error {
	switch step.Action {
	case "commit":
		return r.executeCommit(ctx, step.Commit)
	case "orchestrate":
		return r.executeOrchestrate(ctx, config, step.ExpectFailure)
	case "promote":
		return r.executePromote(ctx, step.Promote, config)
	default:
		return fmt.Errorf("unknown action: %s", step.Action)
	}
}

// executeCommit creates a commit
func (r *Runner) executeCommit(ctx context.Context, commit *CommitStep) error {
	// Track commit reference
	commitNum := r.ctx.GetCommitCount() + 1
	ref := fmt.Sprintf("commit%d", commitNum)

	// If harness is available, actually create the commit
	if r.harness != nil && r.harness.gitea != nil && r.harness.repo != nil {
		sha, err := r.harness.gitea.CreateCommit(ctx, r.harness.repo, commit.Message, commit.Files)
		if err != nil {
			return fmt.Errorf("failed to create commit: %w", err)
		}
		r.ctx.RecordCommit(ref, sha)
		r.ctx.RecordCommitMessage(sha, commit.Message)
		r.t.Logf("  Created commit %s: %s", ref, sha[:7])
	} else {
		// For unit tests, use a placeholder SHA
		sha := fmt.Sprintf("sha_%s", ref)
		r.ctx.RecordCommit(ref, sha)
		r.ctx.RecordCommitMessage(sha, commit.Message)
	}

	return nil
}

// executeOrchestrate runs the orchestrate workflow via ActRunner. When
// expectFailure is set, a failure conclusion is the success path (mirrors
// executePromote's ExpectFailure handling) and a success conclusion is an error.
func (r *Runner) executeOrchestrate(ctx context.Context, config Config, expectFailure bool) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Log("  Would execute orchestrate workflow (no harness)")
		return nil
	}

	// Get the current HEAD SHA for later reference
	sha, err := r.harness.gitea.getHeadSHA(ctx, r.harness.repo)
	if err != nil {
		return fmt.Errorf("failed to get HEAD SHA: %w", err)
	}

	r.t.Logf("  Orchestrate: running workflow for SHA %s", truncateSHA(sha))

	// Sync the repo to act container before running workflow
	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	// Debug: check what's in /tmp/repo
	debugCmd := []string{"bash", "-c", "cd /tmp/repo && git branch -a && ls -la .github/actions/ && ls -la .github/actions/setup-cli/ && cat .github/workflows/orchestrate.yaml | head -50"}
	_, debugReader, _ := r.harness.act.Container().Exec(ctx, debugCmd)
	if debugReader != nil {
		var debugOut bytes.Buffer
		_, _ = io.Copy(&debugOut, debugReader)
		r.t.Logf("  DEBUG /tmp/repo: %s", debugOut.String())
	}

	// Determine the branch ref
	branch := config.TrunkBranch
	if branch == "" {
		branch = "main"
	}

	// Run the actual orchestrate workflow via ActRunner
	result, err := r.harness.act.RunWorkflowFromRepo(ctx, RunOpts{
		WorkflowPath: ".github/workflows/orchestrate.yaml",
		Event:        "push",
		Env: map[string]string{
			"GITHUB_SHA":        sha,
			"GITHUB_REF":        fmt.Sprintf("refs/heads/%s", branch),
			"GITHUB_REPOSITORY": fmt.Sprintf("%s/%s", AdminUsername, r.harness.repo.Name),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to run orchestrate workflow: %w", err)
	}

	// Store workflow result for assertions
	r.lastWorkflowResult = result

	// Handle expected failures (mirrors executePromote's ExpectFailure path).
	if expectFailure {
		if result.Conclusion == "failure" {
			r.t.Log("  Orchestrate: workflow failed as expected")
			return nil
		}
		return fmt.Errorf("expected orchestrate to fail but it succeeded")
	}

	if result.Conclusion != "success" {
		r.t.Logf("  Orchestrate failed with conclusion: %s", result.Conclusion)
		r.t.Logf("  Workflow logs:\n%s", result.Logs)
		return fmt.Errorf("orchestrate workflow failed: %s", result.Error)
	}

	// Debug: show what jobs were parsed
	r.t.Logf("  Orchestrate: parsed %d jobs from output", len(result.Jobs))
	for jobName, job := range result.Jobs {
		r.t.Logf("    - Job '%s': conclusion=%s", jobName, job.Conclusion)
	}

	// Debug: show first 500 chars of logs to see what we're parsing
	logSample := result.Logs
	if len(logSample) > 500 {
		logSample = logSample[:500] + "..."
	}
	r.t.Logf("  Workflow output sample:\n%s", logSample)

	// Update context state from workflow results
	// The workflow should have created tags and updated manifest
	// We need to read the actual state from Gitea
	if err := r.syncStateFromGitea(ctx, config); err != nil {
		r.t.Logf("  Warning: failed to sync state from Gitea: %v", err)
	}

	r.t.Logf("  Orchestrate: workflow completed successfully")
	return nil
}

// executePromote runs the promote workflow via ActRunner
func (r *Runner) executePromote(ctx context.Context, promote *PromoteStep, config Config) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Log("  Would execute promote workflow (no harness)")
		return nil
	}

	r.t.Logf("  Promote: running workflow (mode=%s, target=%s)", promote.Mode, promote.Target)

	// Sync the repo to act container before running workflow
	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	// Debug: Check if promote.yaml exists
	debugCmd := []string{"bash", "-c", "ls -la /tmp/repo/.github/workflows/ && head -30 /tmp/repo/.github/workflows/promote.yaml 2>&1 || echo 'promote.yaml not found'"}
	_, debugReader, _ := r.harness.act.Container().Exec(ctx, debugCmd)
	if debugReader != nil {
		var debugOut bytes.Buffer
		_, _ = io.Copy(&debugOut, debugReader)
		r.t.Logf("  DEBUG promote.yaml: %s", debugOut.String())
	}

	// Build inputs for workflow_dispatch. The CLI's `mode` parameter accepts
	// "default" or a cascade target like "dev-to-prod"; it does NOT accept
	// the literal "cascade". Translate scenarios that use the cascade+target
	// pair into the "<source>-to-<target>" form. Source defaults to the first
	// env (typically dev) since the workflow generator only emits dev-rooted
	// cascade options.
	var inputs map[string]string
	if len(config.Environments) == 1 {
		// Single-environment repos generate a Release workflow (see
		// command.go IsSingleEnvironment → NewReleaseGenerator) written to
		// promote.yaml. That workflow's dispatch input is release_action
		// (create-draft|prerelease|release), not the multi-env promote's
		// mode. A "promote to release" step on a single-env repo means
		// publish the final release, so dispatch release_action: release.
		// Sending mode here was silently ignored, the workflow fell back to
		// its create-draft default, and the run "succeeded" without ever
		// publishing v0.1.0, wiping prod state, or cleaning up the RC tags.
		inputs = map[string]string{
			"release_action": "release",
		}
		if promote.AllowBreaking {
			inputs["allow_breaking_changes"] = "true"
		}
	} else {
		mode := promote.Mode
		if mode == "cascade" {
			source := "dev"
			if len(config.Environments) > 0 {
				source = config.Environments[0]
			}
			mode = fmt.Sprintf("%s-to-%s", source, promote.Target)
		}
		inputs = map[string]string{
			"mode": mode,
		}
		if promote.Force {
			// Workflow input is named "force" (env PROMOTION_FORCE), forwarded to
			// the CLI's --force flag. Only meaningful for default-mode promotions;
			// it bypasses the no-op promotion guard.
			inputs["force"] = "true"
		}
		if promote.AllowBreaking {
			// Workflow input is named allow_breaking_changes (see internal/generate
			// promote.go); the workflow forwards it to the CLI's --allow-breaking
			// flag. The harness's previous "allow_breaking" key was silently
			// ignored, leaving the breaking-change gate active even when the
			// scenario asked for it to be bypassed.
			inputs["allow_breaking_changes"] = "true"
		}
	}

	// Determine the branch ref
	branch := config.TrunkBranch
	if branch == "" {
		branch = "main"
	}

	// Run the actual promote workflow via ActRunner
	result, err := r.harness.act.RunWorkflowFromRepo(ctx, RunOpts{
		WorkflowPath: ".github/workflows/promote.yaml",
		Event:        "workflow_dispatch",
		Inputs:       inputs,
		Env: map[string]string{
			"GITHUB_REF":        fmt.Sprintf("refs/heads/%s", branch),
			"GITHUB_REPOSITORY": fmt.Sprintf("%s/%s", AdminUsername, r.harness.repo.Name),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to run promote workflow: %w", err)
	}

	// Store workflow result for assertions
	r.lastWorkflowResult = result

	// Handle expected failures
	if promote.ExpectFailure {
		if result.Conclusion == "failure" {
			r.t.Log("  Promote: workflow failed as expected")
			return nil
		}
		return fmt.Errorf("expected promote to fail but it succeeded")
	}

	if result.Conclusion != "success" {
		r.t.Logf("  Promote workflow logs:\n%s", result.Logs)
		return fmt.Errorf("promote workflow failed: %s", result.Error)
	}

	// Sync state from Gitea
	if err := r.syncStateFromGitea(ctx, config); err != nil {
		r.t.Logf("  Warning: failed to sync state from Gitea: %v", err)
	}

	r.t.Logf("  Promote: workflow completed successfully")
	return nil
}

// syncStateFromGitea reads current state from Gitea after workflow execution
func (r *Runner) syncStateFromGitea(ctx context.Context, config Config) error {
	// Get tags from Gitea
	tags, err := r.harness.gitea.GetTags(ctx, r.harness.repo)
	if err != nil {
		return fmt.Errorf("failed to get tags: %w", err)
	}

	// Find final release tags (non-RC semver) and clean up their RC tags
	// This simulates what the GitHub manage-release action does after publishing
	// The workflow can't call GitHub API (it's running against Gitea), so we do it here
	var finalReleaseTags []string
	for _, tag := range tags {
		if isVersionTag(tag) && !isPreleaseVersion(tag) {
			finalReleaseTags = append(finalReleaseTags, tag)
		}
	}

	// Delete RC tags for each final release (simulating publish cleanup)
	for _, finalTag := range finalReleaseTags {
		if err := r.harness.gitea.DeleteRCTags(ctx, r.harness.repo, finalTag); err != nil {
			r.t.Logf("  Warning: failed to cleanup RC tags for %s: %v", finalTag, err)
		} else {
			r.t.Logf("  Cleaned up RC tags for %s", finalTag)
		}
	}

	// Re-fetch tags after cleanup
	tags, err = r.harness.gitea.GetTags(ctx, r.harness.repo)
	if err != nil {
		return fmt.Errorf("failed to get tags after cleanup: %w", err)
	}

	// Clear all previously recorded tags before re-syncing
	// This ensures deleted tags are properly removed from tracking
	r.ctx.ClearTags()

	// An RC is a draft only while it's exclusive to the first env (e.g., dev).
	// once it's been promoted into any later env, it's "blessed" and the
	// workflow's prerelease step would have flipped its draft flag. Collect
	// the set of RC versions that are present in any non-firstEnv state.
	promotedRCs := r.readPromotedRCVersions(ctx, config.Environments)

	// Record tags and create release entries for version tags
	// In real GitHub, the workflow creates both tags and releases
	// Since our mock only creates tags, we infer releases from version tags
	for _, tag := range tags {
		r.ctx.RecordTag(tag, true)

		// Create release entry for version tags (e.g., v0.1.0, v0.1.0-rc.0)
		if isVersionTag(tag) {
			isPrerelease := isPreleaseVersion(tag)
			draft := isPrerelease && !promotedRCs[tag]
			r.ctx.RecordRelease(&ReleaseInfo{
				Tag:        tag,
				Prerelease: isPrerelease,
				Draft:      draft,
				Latest:     !isPrerelease,
			})
		}
	}

	// Read manifest from Gitea
	manifestContent, err := r.harness.gitea.GetFileContent(ctx, r.harness.repo, ".github/manifest.yaml")
	if err != nil {
		r.t.Logf("  Note: Could not read manifest: %v", err)
		return nil
	}

	// Parse manifest and update state
	// The manifest uses key "ci" by default (config.DefaultManifestKey)
	var manifest map[string]struct {
		State map[string]struct {
			SHA     string `yaml:"sha"`
			Version string `yaml:"version"`
			Deploys map[string]struct {
				SHA string `yaml:"sha"`
			} `yaml:"deploys"`
			// Divergence fields written by the real hotfix finalize step. Tags
			// match the product's config.EnvState (ref/base_sha/patches). The
			// product has no previous_version manifest key, so divergence's
			// PreviousVersion is left unset on sync.
			Ref     string   `yaml:"ref"`
			BaseSHA string   `yaml:"base_sha"`
			Patches []string `yaml:"patches"`
		} `yaml:"state"`
		// LatestRelease is the single-environment Release workflow's published
		// pointer (ci.latest_release). Single-env repos publish via the Release
		// workflow's finalize step, which writes latest_release.{version,sha}
		// rather than a state[release] env (see internal/generate/release.go).
		LatestRelease struct {
			SHA     string `yaml:"sha"`
			Version string `yaml:"version"`
		} `yaml:"latest_release"`
	}
	if err := yaml.Unmarshal([]byte(manifestContent), &manifest); err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Update context state from manifest
	// Access the "ci" key in the manifest (default manifest key)
	ciData, ok := manifest["ci"]
	if !ok {
		r.t.Logf("  Note: No 'ci' key found in manifest")
		return nil
	}

	// Clear ctx state so deletions in the manifest (e.g. finalize wiping
	// state[prerelease] on publish) are reflected. Otherwise stale entries
	// from prior steps make wiped: true assertions fail.
	r.ctx.ClearState()

	for env, state := range ciData.State {
		r.ctx.RecordState(env, state.SHA, state.Version)
		r.t.Logf("  Synced state[%s] = %s @ %s", env, truncateSHA(state.SHA), state.Version)

		// Record integration-branch divergence so divergence assertions can see
		// a hotfixed environment. PreviousVersion has no manifest key (the
		// product tracks prior versions separately), so it stays empty here.
		if state.Ref != "" || state.BaseSHA != "" || len(state.Patches) > 0 {
			r.ctx.RecordStateDivergence(env, state.Ref, state.BaseSHA, state.Patches, "")
			r.t.Logf("  Synced state[%s] divergence ref=%s base=%s patches=%d",
				env, state.Ref, truncateSHA(state.BaseSHA), len(state.Patches))
		}

		// Also record per-deploy state
		for deployName, deployState := range state.Deploys {
			r.ctx.RecordDeployState(env, deployName, deployState.SHA)
			r.t.Logf("  Synced state[%s].deploys[%s] = %s", env, deployName, truncateSHA(deployState.SHA))
		}
	}

	// Surface the single-env Release workflow's latest_release pointer under the
	// synthetic "release" state key so scenarios can assert it the same way they
	// assert any other environment's state. Multi-env repos leave latest_release
	// empty (they wipe and repopulate state[release] directly), so this only
	// records when the Release workflow has actually published.
	if lr := ciData.LatestRelease; lr.Version != "" || lr.SHA != "" {
		r.ctx.RecordState("release", lr.SHA, lr.Version)
		r.t.Logf("  Synced state[release] (from latest_release) = %s @ %s", truncateSHA(lr.SHA), lr.Version)
	}

	return nil
}

// readPromotedRCVersions reads the manifest and returns the set of RC versions
// that appear in any state[env] beyond the first env. Once an RC has been
// promoted past dev (the first env), the workflow's prerelease step would have
// flipped its draft flag, so the harness should treat it as non-draft.
func (r *Runner) readPromotedRCVersions(ctx context.Context, envs []string) map[string]bool {
	promoted := make(map[string]bool)
	if len(envs) < 2 {
		return promoted
	}
	content, err := r.harness.gitea.GetFileContent(ctx, r.harness.repo, ".github/manifest.yaml")
	if err != nil {
		return promoted
	}
	var manifest map[string]struct {
		State map[string]struct {
			Version string `yaml:"version"`
		} `yaml:"state"`
	}
	if err := yaml.Unmarshal([]byte(content), &manifest); err != nil {
		return promoted
	}
	ci, ok := manifest["ci"]
	if !ok {
		return promoted
	}
	firstEnv := envs[0]
	for env, state := range ci.State {
		if env == firstEnv || state.Version == "" {
			continue
		}
		if isPreleaseVersion(state.Version) {
			promoted[state.Version] = true
		}
	}
	return promoted
}

// isVersionTag checks if a tag looks like a semver version tag
func isVersionTag(tag string) bool {
	if len(tag) < 2 {
		return false
	}
	return tag[0] == 'v' && (tag[1] >= '0' && tag[1] <= '9')
}

// isPreleaseVersion checks if a version tag is a prerelease (contains -rc, -alpha, -beta, etc.)
func isPreleaseVersion(tag string) bool {
	for i := 0; i < len(tag); i++ {
		if tag[i] == '-' {
			return true
		}
	}
	return false
}

// assertStep runs assertions for a step
func (r *Runner) assertStep(ctx context.Context, step *Step, preState *ExecutionContext) []error {
	var allErrs []error
	expect := step.Expect

	// Assert state
	for env, stateExpect := range expect.State {
		// Handle "unchanged" expectation
		if stateExpect.Unchanged {
			preEnvState := preState.GetState(env)
			currentState := r.ctx.GetState(env)
			if preEnvState.SHA != currentState.SHA || preEnvState.Version != currentState.Version {
				allErrs = append(allErrs, fmt.Errorf("state[%s] expected unchanged but changed from %s/%s to %s/%s",
					env, preEnvState.SHA, preEnvState.Version, currentState.SHA, currentState.Version))
			}
			continue
		}
		errs := AssertState(r.ctx, env, stateExpect)
		allErrs = append(allErrs, errs...)
	}

	// Assert jobs from workflow result
	if expect.Jobs != nil && r.lastWorkflowResult != nil {
		errs := AssertWorkflowExecution(r.lastWorkflowResult, expect.Jobs)
		allErrs = append(allErrs, errs...)
	}

	// Assert tags
	if expect.Tags != nil {
		errs := AssertTags(r.ctx, expect.Tags)
		allErrs = append(allErrs, errs...)
	}

	// Assert releases
	if len(expect.Releases) > 0 {
		errs := AssertReleasesInternal(r.ctx, expect.Releases)
		allErrs = append(allErrs, errs...)
	}

	// Assert workflow file content (verifies manifest -> generated yaml mapping).
	for _, wfe := range expect.WorkflowFiles {
		errs := r.assertWorkflowFile(ctx, wfe)
		allErrs = append(allErrs, errs...)
	}

	return allErrs
}

// assertWorkflowFile reads a workflow file from the test repo (in /tmp/repo
// inside the act container) and checks its content against the expectation.
// Returns errors for missing-substring or unexpected-substring matches.
func (r *Runner) assertWorkflowFile(ctx context.Context, expect WorkflowFileExpect) []error {
	if r.harness == nil || r.harness.act == nil {
		// In unit-test mode there's no act container; skip silently.
		return nil
	}
	if expect.Path == "" {
		return []error{fmt.Errorf("workflow_files entry missing path")}
	}

	cmd := []string{"bash", "-c", fmt.Sprintf("cat /tmp/repo/%s 2>/dev/null", expect.Path)}
	exitCode, reader, err := r.harness.act.Container().Exec(ctx, cmd)
	if err != nil {
		return []error{fmt.Errorf("read %s: %w", expect.Path, err)}
	}
	var content bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&content, reader)
	}
	if expect.NotExists {
		if exitCode == 0 {
			return []error{fmt.Errorf("workflow file %s should not exist but was found", expect.Path)}
		}
		return nil
	}
	if exitCode != 0 {
		return []error{fmt.Errorf("workflow file not found: %s", expect.Path)}
	}
	body := content.String()

	var errs []error
	for _, want := range expect.Contains {
		if !strings.Contains(body, want) {
			errs = append(errs, fmt.Errorf("%s missing expected substring %q", expect.Path, want))
		}
	}
	for _, unwant := range expect.NotContains {
		if strings.Contains(body, unwant) {
			errs = append(errs, fmt.Errorf("%s contains unexpected substring %q", expect.Path, unwant))
		}
	}
	return errs
}

// GetContext returns the execution context (for testing)
func (r *Runner) GetContext() *ExecutionContext {
	return r.ctx
}

// truncateSHA safely truncates a SHA to 7 characters for display
func truncateSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

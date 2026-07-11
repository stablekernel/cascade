package harness

import (
	"bytes"
	"context"
	"encoding/json"
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
	// Hotfix apply bookkeeping, carried across steps so merge_pr,
	// resolve_conflict, and hotfix_merged can act on the most recent apply.
	lastPRIndex         int64            // PR index opened by the most recent hotfix_apply
	lastPRConflict      bool             // whether that apply hit a cherry-pick conflict
	lastHotfixBranch    string           // head branch of that apply
	lastHotfixEnv       string           // target env of that apply
	lastHotfixComponent string           // component of that apply ("" for single-component)
	lastHotfixBody      string           // PR body (with trailers) of that apply
	prByLabel           map[string]int64 // label -> most recent PR index opened with it
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
		case "hotfix_plan":
			if step.HotfixPlan == nil {
				return fmt.Errorf("step %d (%s): hotfix_plan action requires hotfix_plan config", i, step.Name)
			}
			if step.HotfixPlan.TargetEnv == "" {
				return fmt.Errorf("step %d (%s): hotfix_plan requires target_env", i, step.Name)
			}
			if step.HotfixPlan.CommitRef == "" {
				return fmt.Errorf("step %d (%s): hotfix_plan requires commit_ref", i, step.Name)
			}
		case "hotfix_apply":
			if step.HotfixApply == nil {
				return fmt.Errorf("step %d (%s): hotfix_apply action requires hotfix_apply config", i, step.Name)
			}
			if step.HotfixApply.TargetEnv == "" {
				return fmt.Errorf("step %d (%s): hotfix_apply requires target_env", i, step.Name)
			}
			if step.HotfixApply.CommitRef == "" {
				return fmt.Errorf("step %d (%s): hotfix_apply requires commit_ref", i, step.Name)
			}
		case "merge_pr":
			if step.MergePR == nil {
				return fmt.Errorf("step %d (%s): merge_pr action requires merge_pr config", i, step.Name)
			}
			if step.MergePR.Label == "" && step.MergePR.Index <= 0 {
				return fmt.Errorf("step %d (%s): merge_pr requires label or index", i, step.Name)
			}
		case "resolve_conflict":
			if step.ResolveConflict == nil {
				return fmt.Errorf("step %d (%s): resolve_conflict action requires resolve_conflict config", i, step.Name)
			}
			if len(step.ResolveConflict.Files) == 0 {
				return fmt.Errorf("step %d (%s): resolve_conflict requires at least one file", i, step.Name)
			}
		case "hotfix_merged":
			if step.HotfixMerged == nil {
				return fmt.Errorf("step %d (%s): hotfix_merged action requires hotfix_merged config", i, step.Name)
			}
			if step.HotfixMerged.TargetEnv == "" {
				return fmt.Errorf("step %d (%s): hotfix_merged requires target_env", i, step.Name)
			}
		case "stage_divergence":
			if step.StageDivergence == nil {
				return fmt.Errorf("step %d (%s): stage_divergence action requires stage_divergence config", i, step.Name)
			}
			if step.StageDivergence.Env == "" {
				return fmt.Errorf("step %d (%s): stage_divergence requires env", i, step.Name)
			}
		case "rollback":
			if step.Rollback == nil {
				return fmt.Errorf("step %d (%s): rollback action requires rollback config", i, step.Name)
			}
			if step.Rollback.Environment == "" {
				return fmt.Errorf("step %d (%s): rollback requires environment", i, step.Name)
			}
		case "verify":
			if step.Verify == nil {
				return fmt.Errorf("step %d (%s): verify action requires verify config", i, step.Name)
			}
			if step.Verify.MutatePath != "" && step.Verify.MutateAppend == "" {
				return fmt.Errorf("step %d (%s): verify mutate_path requires mutate_append", i, step.Name)
			}
			if (step.Verify.CreatePath == "") != (step.Verify.CreateFrom == "") {
				return fmt.Errorf("step %d (%s): verify create_path and create_from must be set together", i, step.Name)
			}
		case "plan":
			if step.Plan == nil {
				return fmt.Errorf("step %d (%s): plan action requires plan config", i, step.Name)
			}
			if step.Plan.MutatePath != "" && step.Plan.MutateAppend == "" {
				return fmt.Errorf("step %d (%s): plan mutate_path requires mutate_append", i, step.Name)
			}
		case "consistency":
			if step.Consistency == nil {
				return fmt.Errorf("step %d (%s): consistency action requires consistency config", i, step.Name)
			}
		case "reconcile":
			if step.Reconcile == nil {
				return fmt.Errorf("step %d (%s): reconcile action requires reconcile config", i, step.Name)
			}
			if step.Reconcile.MutatePath == "" {
				return fmt.Errorf("step %d (%s): reconcile requires mutate_path", i, step.Name)
			}
			if step.Reconcile.MutateFind == "" {
				return fmt.Errorf("step %d (%s): reconcile requires mutate_find", i, step.Name)
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
		return r.executeOrchestrate(ctx, config, step.ExpectFailure, step.Orchestrate)
	case "promote":
		return r.executePromote(ctx, step.Promote, config)
	case "hotfix_plan":
		return r.executeHotfixPlan(ctx, step.HotfixPlan)
	case "hotfix_apply":
		return r.executeHotfixApply(ctx, step.HotfixApply)
	case "merge_pr":
		return r.executeMergePR(ctx, step.MergePR)
	case "resolve_conflict":
		return r.executeResolveConflict(ctx, step.ResolveConflict)
	case "hotfix_merged":
		return r.executeHotfixMerged(ctx, step.HotfixMerged, config)
	case "stage_divergence":
		return r.executeStageDivergence(ctx, step.StageDivergence)
	case "rollback":
		return r.executeRollback(ctx, step.Rollback, config)
	case "verify":
		return r.executeVerify(ctx, step.Verify)
	case "plan":
		return r.executePlan(ctx, step.Plan)
	case "consistency":
		return r.executeConsistency(ctx, step.Consistency)
	case "reconcile":
		return r.executeReconcile(ctx, step.Reconcile)
	case "run_workflow":
		return r.executeRunWorkflow(ctx, config, step.ExpectFailure, step.RunWorkflow)
	default:
		return fmt.Errorf("unknown action: %s", step.Action)
	}
}

// executeVerify runs `cascade verify` in the synced repo and asserts the exit
// code matches the step's ExpectExit. When Regenerate is set it first runs
// `cascade generate-workflow -f` so verify checks pristine generated output
// rather than the harness's localized copies. When MutatePath is set it appends
// MutateAppend to that file before verifying, driving the drift path. When
// CreatePath/CreateFrom are set it copies an existing generated (marker-carrying)
// file to an unplanned path before verifying, driving the orphan path; AllowOrphans
// adds --allow-orphans so the opt-out can be exercised. The whole step is
// read-through-the-CLI and never asserts on workflow execution.
func (r *Runner) executeVerify(ctx context.Context, step *VerifyStep) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Logf("  Would run cascade verify (expect exit %d, no harness)", step.ExpectExit)
		return nil
	}

	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("verify: failed to sync repo: %w", err)
	}

	if step.Regenerate {
		regenCmd := []string{"bash", "-c", "cd /tmp/repo && /usr/local/bin/cascade generate-workflow -f"}
		exitCode, reader, err := r.harness.act.Container().Exec(ctx, regenCmd)
		if err != nil {
			return fmt.Errorf("verify: regenerate exec failed: %w", err)
		}
		var out bytes.Buffer
		if reader != nil {
			_, _ = io.Copy(&out, reader)
		}
		if exitCode != 0 {
			return fmt.Errorf("verify: regenerate failed (exit %d): %s", exitCode, out.String())
		}
	}

	if step.MutatePath != "" {
		mutateCmd := []string{"bash", "-c", fmt.Sprintf(
			"cd /tmp/repo && printf '%%s' %s >> %s",
			shellQuote(step.MutateAppend), shellQuote(step.MutatePath),
		)}
		exitCode, reader, err := r.harness.act.Container().Exec(ctx, mutateCmd)
		if err != nil {
			return fmt.Errorf("verify: mutate exec failed: %w", err)
		}
		var out bytes.Buffer
		if reader != nil {
			_, _ = io.Copy(&out, reader)
		}
		if exitCode != 0 {
			return fmt.Errorf("verify: mutate failed (exit %d): %s", exitCode, out.String())
		}
	}

	if step.CreatePath != "" {
		copyCmd := []string{"bash", "-c", fmt.Sprintf(
			"cd /tmp/repo && cp %s %s",
			shellQuote(step.CreateFrom), shellQuote(step.CreatePath),
		)}
		exitCode, reader, err := r.harness.act.Container().Exec(ctx, copyCmd)
		if err != nil {
			return fmt.Errorf("verify: create exec failed: %w", err)
		}
		var out bytes.Buffer
		if reader != nil {
			_, _ = io.Copy(&out, reader)
		}
		if exitCode != 0 {
			return fmt.Errorf("verify: create failed (exit %d): %s", exitCode, out.String())
		}
	}

	verifyArgs := "/usr/local/bin/cascade verify"
	if step.AllowOrphans {
		verifyArgs += " --allow-orphans"
	}
	verifyCmd := []string{"bash", "-c", "cd /tmp/repo && " + verifyArgs}
	exitCode, reader, err := r.harness.act.Container().Exec(ctx, verifyCmd)
	if err != nil {
		return fmt.Errorf("verify: exec failed: %w", err)
	}
	var out bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&out, reader)
	}
	r.t.Logf("  Verify: exit=%d (expected %d): %s", exitCode, step.ExpectExit, out.String())

	if exitCode != step.ExpectExit {
		return fmt.Errorf("verify: expected exit %d, got %d: %s", step.ExpectExit, exitCode, out.String())
	}
	return nil
}

// executePlan runs `cascade plan` in the synced repo and asserts the exit code
// matches the step's ExpectExit (0 on success, since plan is informational and
// never a gate). When Regenerate is set it first runs `cascade generate-workflow
// -f` so plan previews against pristine generated output rather than the
// harness's localized copies. When MutatePath is set it appends MutateAppend to
// that file before planning, driving a specific diff. The captured stdout is
// checked against ExpectContains/ExpectNotContains. The whole step is
// read-through-the-CLI and never asserts on workflow execution.
func (r *Runner) executePlan(ctx context.Context, step *PlanStep) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Logf("  Would run cascade plan (expect exit %d, no harness)", step.ExpectExit)
		return nil
	}

	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("plan: failed to sync repo: %w", err)
	}

	if step.Regenerate {
		regenCmd := []string{"bash", "-c", "cd /tmp/repo && /usr/local/bin/cascade generate-workflow -f"}
		exitCode, reader, err := r.harness.act.Container().Exec(ctx, regenCmd)
		if err != nil {
			return fmt.Errorf("plan: regenerate exec failed: %w", err)
		}
		var out bytes.Buffer
		if reader != nil {
			_, _ = io.Copy(&out, reader)
		}
		if exitCode != 0 {
			return fmt.Errorf("plan: regenerate failed (exit %d): %s", exitCode, out.String())
		}
	}

	if step.MutatePath != "" {
		mutateCmd := []string{"bash", "-c", fmt.Sprintf(
			"cd /tmp/repo && printf '%%s' %s >> %s",
			shellQuote(step.MutateAppend), shellQuote(step.MutatePath),
		)}
		exitCode, reader, err := r.harness.act.Container().Exec(ctx, mutateCmd)
		if err != nil {
			return fmt.Errorf("plan: mutate exec failed: %w", err)
		}
		var out bytes.Buffer
		if reader != nil {
			_, _ = io.Copy(&out, reader)
		}
		if exitCode != 0 {
			return fmt.Errorf("plan: mutate failed (exit %d): %s", exitCode, out.String())
		}
	}

	planCmd := []string{"bash", "-c", "cd /tmp/repo && /usr/local/bin/cascade plan"}
	exitCode, reader, err := r.harness.act.Container().Exec(ctx, planCmd)
	if err != nil {
		return fmt.Errorf("plan: exec failed: %w", err)
	}
	var out bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&out, reader)
	}
	output := out.String()
	r.t.Logf("  Plan: exit=%d (expected %d): %s", exitCode, step.ExpectExit, output)

	if exitCode != step.ExpectExit {
		return fmt.Errorf("plan: expected exit %d, got %d: %s", step.ExpectExit, exitCode, output)
	}
	for _, want := range step.ExpectContains {
		if !strings.Contains(output, want) {
			return fmt.Errorf("plan: stdout missing expected substring %q: %s", want, output)
		}
	}
	for _, unwant := range step.ExpectNotContains {
		if strings.Contains(output, unwant) {
			return fmt.Errorf("plan: stdout contains unexpected substring %q: %s", unwant, output)
		}
	}
	return nil
}

// executeReconcile runs `cascade reconcile` in the synced repo to prove a
// governed pin bump landing in one already-generated workflow file (simulating
// an external change such as a merged Dependabot bump) is adopted into the
// manifest and survives regeneration. It first substitutes step.MutateFind for
// step.MutateReplace in step.MutatePath (a sed pattern, not a literal match),
// simulating the bump landing in that file; it then runs `cascade reconcile
// --changed-file <step.ChangedFile or MutatePath>` and asserts its exit code,
// then asserts the regenerated MutatePath contains every ExpectContains
// substring, and finally runs `cascade verify` and requires a clean exit,
// proving the adopted pin survives regeneration rather than drifting back out
// of it.
func (r *Runner) executeReconcile(ctx context.Context, step *ReconcileStep) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Logf("  Would run cascade reconcile (no harness)")
		return nil
	}

	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("reconcile: failed to sync repo: %w", err)
	}

	mutateCmd := []string{"bash", "-c", fmt.Sprintf(
		"cd /tmp/repo && sed -i %s %s",
		shellQuote(fmt.Sprintf("s|%s|%s|", step.MutateFind, step.MutateReplace)),
		shellQuote(step.MutatePath),
	)}
	exitCode, reader, err := r.harness.act.Container().Exec(ctx, mutateCmd)
	if err != nil {
		return fmt.Errorf("reconcile: mutate exec failed: %w", err)
	}
	var out bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&out, reader)
	}
	if exitCode != 0 {
		return fmt.Errorf("reconcile: mutate failed (exit %d): %s", exitCode, out.String())
	}

	changedFile := step.ChangedFile
	if changedFile == "" {
		changedFile = step.MutatePath
	}

	reconcileCmd := []string{"bash", "-c", fmt.Sprintf(
		"cd /tmp/repo && /usr/local/bin/cascade reconcile --changed-file %s",
		shellQuote(changedFile),
	)}
	exitCode, reader, err = r.harness.act.Container().Exec(ctx, reconcileCmd)
	if err != nil {
		return fmt.Errorf("reconcile: exec failed: %w", err)
	}
	out.Reset()
	if reader != nil {
		_, _ = io.Copy(&out, reader)
	}
	output := out.String()
	r.t.Logf("  Reconcile: exit=%d (expected %d): %s", exitCode, step.ExpectExit, output)
	if exitCode != step.ExpectExit {
		return fmt.Errorf("reconcile: expected exit %d, got %d: %s", step.ExpectExit, exitCode, output)
	}

	if len(step.ExpectContains) > 0 {
		catCmd := []string{"bash", "-c", "cd /tmp/repo && cat " + shellQuote(step.MutatePath)}
		catExit, catReader, catErr := r.harness.act.Container().Exec(ctx, catCmd)
		if catErr != nil {
			return fmt.Errorf("reconcile: reading regenerated %s failed: %w", step.MutatePath, catErr)
		}
		var catOut bytes.Buffer
		if catReader != nil {
			_, _ = io.Copy(&catOut, catReader)
		}
		if catExit != 0 {
			return fmt.Errorf("reconcile: cat %s failed (exit %d): %s", step.MutatePath, catExit, catOut.String())
		}
		content := catOut.String()
		for _, want := range step.ExpectContains {
			if !strings.Contains(content, want) {
				return fmt.Errorf("reconcile: regenerated %s missing expected substring %q:\n%s", step.MutatePath, want, content)
			}
		}
	}

	verifyCmd := []string{"bash", "-c", "cd /tmp/repo && /usr/local/bin/cascade verify"}
	verifyExit, verifyReader, verifyErr := r.harness.act.Container().Exec(ctx, verifyCmd)
	if verifyErr != nil {
		return fmt.Errorf("reconcile: verify exec failed: %w", verifyErr)
	}
	var verifyOut bytes.Buffer
	if verifyReader != nil {
		_, _ = io.Copy(&verifyOut, verifyReader)
	}
	r.t.Logf("  Verify after reconcile: exit=%d: %s", verifyExit, verifyOut.String())
	if verifyExit != 0 {
		return fmt.Errorf("reconcile: cascade verify was not clean after regenerate (exit %d): %s", verifyExit, verifyOut.String())
	}

	return nil
}

// shellQuote wraps a string in single quotes for safe interpolation into a
// bash -c command, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// orchestrateWorkflowPath returns the repo-relative orchestrate workflow path for
// a component. An empty component selects the repo-wide orchestrate.yaml; a named
// component selects the fanned-out orchestrate-<name>.yaml the generator emits for
// a manifest with a components: block.
func orchestrateWorkflowPath(component string) string {
	if component == "" {
		return ".github/workflows/orchestrate.yaml"
	}
	return fmt.Sprintf(".github/workflows/orchestrate-%s.yaml", component)
}

// promoteWorkflowPath returns the repo-relative promote workflow path for a
// component. An empty component selects the repo-wide promote.yaml; a named
// component selects the fanned-out promote-<name>.yaml the generator emits for a
// manifest with a components: block.
func promoteWorkflowPath(component string) string {
	if component == "" {
		return ".github/workflows/promote.yaml"
	}
	return fmt.Sprintf(".github/workflows/promote-%s.yaml", component)
}

// componentStateKey composes the ExecutionContext key under which a component's
// per-environment state is recorded. Component-scoped state lives at
// state.components.<component>.<env> in the manifest; the harness records it under
// this composite key so the flat state.<env> path is untouched and every existing
// state helper (record/get/clone/unchanged) keeps working without change.
func componentStateKey(component, env string) string {
	return "components/" + component + "/" + env
}

// componentEnvStateYAML is the subset of a component's per-env manifest row the
// harness reads back. It mirrors the flat env row parsed in syncStateFromGitea.
type componentEnvStateYAML struct {
	SHA     string `yaml:"sha"`
	Version string `yaml:"version"`
	Deploys map[string]struct {
		SHA string `yaml:"sha"`
	} `yaml:"deploys"`
	// Divergence fields written by the per-component hotfix and rollback finalize
	// steps, mirroring the flat parse's ref/base_sha/patches. Component-scoped
	// divergence nests under state.components.<name>.<env>, so a scenario can assert
	// a hotfixed component tracks env/<name>/<env> while a sibling stays undiverged.
	Ref     string   `yaml:"ref"`
	BaseSHA string   `yaml:"base_sha"`
	Patches []string `yaml:"patches"`
}

// parseComponentStates extracts ci.state.components.<name>.<env> rows from a
// manifest document under manifestKey (typically config.DefaultManifestKey). A
// manifest with no components subtree yields an empty map and no error. It is a
// pure function so the component-scoped readback is unit-testable without a live
// gitea/harness. The flat state.<env> rows alongside components are ignored here:
// they are read by the existing flat parse in syncStateFromGitea.
func parseComponentStates(manifestContent, manifestKey string) (map[string]map[string]componentEnvStateYAML, error) {
	var doc map[string]struct {
		State struct {
			Components map[string]map[string]componentEnvStateYAML `yaml:"components"`
		} `yaml:"state"`
	}
	if err := yaml.Unmarshal([]byte(manifestContent), &doc); err != nil {
		return nil, err
	}
	section, ok := doc[manifestKey]
	if !ok {
		return nil, nil
	}
	return section.State.Components, nil
}

// consistencyReport mirrors the JSON shape printed by `cascade status
// consistency --json`. Only the fields the harness asserts on are modeled.
type consistencyReport struct {
	OrphanEnvBranches []string `json:"orphan_env_branches"`
	HealedEnvBranches []string `json:"healed_env_branches"`
}

// parseConsistencyJSON extracts the report object from command stdout. The
// command prints a single JSON object; the object spans from the first '{' to
// the last '}', so any non-JSON preamble the container exec emits is skipped.
func parseConsistencyJSON(out string) (consistencyReport, error) {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return consistencyReport{}, fmt.Errorf("no JSON object in output")
	}
	var report consistencyReport
	if err := json.Unmarshal([]byte(out[start:end+1]), &report); err != nil {
		return consistencyReport{}, err
	}
	return report, nil
}

// assertStringSetEqual reports an error when got and want differ as sets,
// ignoring order. The command emits branches in remote-listing order, which is
// not contractually stable, so the assertion compares membership.
func assertStringSetEqual(label string, got, want []string) error {
	gotSet := make(map[string]struct{}, len(got))
	for _, g := range got {
		gotSet[g] = struct{}{}
	}
	if len(gotSet) != len(want) {
		return fmt.Errorf("%s: got %v, want %v", label, got, want)
	}
	for _, w := range want {
		if _, ok := gotSet[w]; !ok {
			return fmt.Errorf("%s: got %v, want %v", label, got, want)
		}
	}
	return nil
}

// executeConsistency runs `cascade status consistency` (optionally --fix) in the
// synced repo whose origin is the Gitea remote, then asserts the JSON report and
// the live remote branch set. SeedBranches are created on the remote first so
// the command observes them. With Fix the command deletes each orphan via
// `git push <remote> --delete`, exercising the real strictly-git deletion path.
func (r *Runner) executeConsistency(ctx context.Context, step *ConsistencyStep) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Logf("  Would run cascade status consistency (no harness)")
		return nil
	}

	// Seed the requested env/* branches on the remote from current trunk HEAD so
	// the command lists them. CreateBranch starts the branch at the given commit.
	if len(step.SeedBranches) > 0 {
		headSHA, err := r.harness.gitea.getHeadSHA(ctx, r.harness.repo)
		if err != nil {
			return fmt.Errorf("consistency: get HEAD SHA: %w", err)
		}
		for _, b := range step.SeedBranches {
			if err := r.harness.gitea.CreateBranch(ctx, r.harness.repo, b, headSHA); err != nil {
				return fmt.Errorf("consistency: seed branch %s: %w", b, err)
			}
		}
	}

	// Sync so /tmp/repo's origin remote-tracking refs include the seeded env/*
	// branches; the command lists refs/remotes/origin/* via git for-each-ref.
	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("consistency: failed to sync repo: %w", err)
	}

	args := "/usr/local/bin/cascade status consistency --json"
	if step.Fix {
		args += " --fix"
	}
	cmd := []string{"bash", "-c", "cd /tmp/repo && " + args}
	exitCode, reader, err := r.harness.act.Container().Exec(ctx, cmd)
	if err != nil {
		return fmt.Errorf("consistency: exec failed: %w", err)
	}
	var out bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&out, reader)
	}
	r.t.Logf("  Consistency: exit=%d: %s", exitCode, out.String())
	if exitCode != 0 {
		return fmt.Errorf("consistency: expected exit 0, got %d: %s", exitCode, out.String())
	}

	report, err := parseConsistencyJSON(out.String())
	if err != nil {
		return fmt.Errorf("consistency: parse JSON (%q): %w", out.String(), err)
	}
	if err := assertStringSetEqual("orphan_env_branches", report.OrphanEnvBranches, step.ExpectOrphans); err != nil {
		return fmt.Errorf("consistency: %w", err)
	}
	if step.Fix {
		if err := assertStringSetEqual("healed_env_branches", report.HealedEnvBranches, step.ExpectHealed); err != nil {
			return fmt.Errorf("consistency: %w", err)
		}
	}

	// Assert the live remote branch set after the run. Query the remote's git
	// refs directly via ls-remote: this is the same git layer the command lists
	// and deletes through, and it reflects a just-created or just-deleted ref
	// immediately, unlike Gitea's higher-level branches API which can lag.
	if len(step.ExpectBranchesAbsent) > 0 || len(step.ExpectBranchesPresent) > 0 {
		lsCmd := []string{"bash", "-c", "cd /tmp/repo && git ls-remote --heads origin"}
		lsExit, lsReader, err := r.harness.act.Container().Exec(ctx, lsCmd)
		if err != nil {
			return fmt.Errorf("consistency: ls-remote exec failed: %w", err)
		}
		var lsOut bytes.Buffer
		if lsReader != nil {
			_, _ = io.Copy(&lsOut, lsReader)
		}
		if lsExit != 0 {
			return fmt.Errorf("consistency: ls-remote failed (exit %d): %s", lsExit, lsOut.String())
		}
		present := parseRemoteHeads(lsOut.String())
		r.t.Logf("  Consistency: remote heads after run: %v", present)
		for _, b := range step.ExpectBranchesAbsent {
			if _, ok := present[b]; ok {
				return fmt.Errorf("consistency: branch %s expected deleted but still present on remote", b)
			}
		}
		for _, b := range step.ExpectBranchesPresent {
			if _, ok := present[b]; !ok {
				return fmt.Errorf("consistency: branch %s expected present but missing on remote", b)
			}
		}
	}
	return nil
}

// parseRemoteHeads parses `git ls-remote --heads` output into a set of branch
// names. Each line is "<sha>\trefs/heads/<branch>"; non-matching lines are
// skipped.
func parseRemoteHeads(out string) map[string]struct{} {
	const prefix = "refs/heads/"
	heads := make(map[string]struct{})
	for _, line := range strings.Split(out, "\n") {
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[idx+len(prefix):])
		if name != "" {
			heads[name] = struct{}{}
		}
	}
	return heads
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

// executeStageDivergence rewrites an environment's divergence fields directly in
// the live manifest (via materializeManifestState) and records the same
// divergence in the execution context. No workflow runs. Ref/BaseSHA/Patches
// entries may be commit references (resolved via the execution context) or
// literal SHAs. This lets a scenario re-wire a diverged env's patch set to an
// off-trunk SHA so a later promote exercises the patch-containment guard at the
// e2e level.
func (r *Runner) executeStageDivergence(ctx context.Context, step *StageDivergenceStep) error {
	setup := map[string]*EnvStateSetup{
		step.Env: {
			Ref:             step.Ref,
			BaseSHA:         step.BaseSHA,
			Patches:         step.Patches,
			PreviousVersion: step.PreviousVersion,
		},
	}

	if r.harness != nil && r.harness.gitea != nil && r.harness.repo != nil {
		if err := r.materializeManifestState(ctx, setup); err != nil {
			return fmt.Errorf("stage_divergence: %w", err)
		}
	}

	// Mirror the staged divergence into the execution context, resolving any
	// commit references to literal SHAs the same way applySetup does.
	baseSHA := r.ctx.ResolveSHA(step.BaseSHA)
	if baseSHA == "" {
		baseSHA = step.BaseSHA
	}
	patches := make([]string, 0, len(step.Patches))
	for _, p := range step.Patches {
		if resolved := r.ctx.ResolveSHA(p); resolved != "" {
			patches = append(patches, resolved)
		} else {
			patches = append(patches, p)
		}
	}
	r.ctx.RecordStateDivergence(step.Env, step.Ref, baseSHA, patches, step.PreviousVersion)
	r.t.Logf("  StageDivergence: env=%s ref=%s base=%s patches=%d",
		step.Env, step.Ref, truncateSHA(baseSHA), len(patches))
	return nil
}

// executeRunWorkflow runs an arbitrary generated workflow file under a chosen
// GitHub event via ActRunner and stores the result for job/log assertions. It is
// the read-only counterpart to executeOrchestrate: it performs no post-run state
// sync, so it drives validation lanes whose only observable outcome is the job
// conclusion and logs (for example the merge-queue lane, which runs on
// merge_group and writes no state). When expectFailure is set a failure
// conclusion is the success path and a success conclusion is the error, so a
// scenario can prove a gate reds on an invalid or breaking candidate.
func (r *Runner) executeRunWorkflow(ctx context.Context, config Config, expectFailure bool, step *RunWorkflowStep) error {
	if step == nil || step.WorkflowPath == "" {
		return fmt.Errorf("run_workflow: workflow_path is required")
	}

	if r.harness == nil || r.harness.act == nil {
		r.t.Logf("  Would run workflow %s under %q (no harness)", step.WorkflowPath, step.Event)
		return nil
	}

	event := step.Event
	if event == "" {
		event = "push"
	}

	// Get the current HEAD SHA for later reference.
	sha, err := r.harness.gitea.getHeadSHA(ctx, r.harness.repo)
	if err != nil {
		return fmt.Errorf("run_workflow: failed to get HEAD SHA: %w", err)
	}

	// Sync the repo to the act container so the run sees the committed workflows
	// and manifest (including any manifest a preceding commit step overwrote).
	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("run_workflow: failed to sync repo: %w", err)
	}

	branch := config.TrunkBranch
	if branch == "" {
		branch = "main"
	}

	r.t.Logf("  RunWorkflow: %s under event %q for SHA %s", step.WorkflowPath, event, truncateSHA(sha))

	result, err := r.harness.act.RunWorkflowFromRepo(ctx, RunOpts{
		WorkflowPath: step.WorkflowPath,
		Event:        event,
		Env: map[string]string{
			"GITHUB_SHA":        sha,
			"GITHUB_REF":        fmt.Sprintf("refs/heads/%s", branch),
			"GITHUB_REPOSITORY": fmt.Sprintf("%s/%s", AdminUsername, r.harness.repo.Name),
		},
	})
	if err != nil {
		return fmt.Errorf("run_workflow: failed to run %s: %w", step.WorkflowPath, err)
	}

	// Store workflow result for assertions (expect.jobs, expect.expect_log).
	r.lastWorkflowResult = result

	// Handle expected failures (mirrors executeOrchestrate's ExpectFailure path).
	// A failing conclusion is the success path: it proves the lane blocks an
	// invalid or breaking candidate from merging.
	if expectFailure {
		if result.Conclusion == "failure" {
			r.t.Logf("  RunWorkflow: %s failed as expected", step.WorkflowPath)
			return nil
		}
		return fmt.Errorf("run_workflow: expected %s to fail but it succeeded", step.WorkflowPath)
	}

	if result.Conclusion != "success" {
		r.t.Logf("  RunWorkflow %s failed with conclusion: %s", step.WorkflowPath, result.Conclusion)
		r.t.Logf("  Workflow logs:\n%s", result.Logs)
		return workflowFailureError("run_workflow", result)
	}

	r.t.Logf("  RunWorkflow: %s parsed %d jobs", step.WorkflowPath, len(result.Jobs))
	for jobName, job := range result.Jobs {
		r.t.Logf("    - Job '%s': conclusion=%s", jobName, job.Conclusion)
	}

	return nil
}

// executeOrchestrate runs the orchestrate workflow via ActRunner. When
// expectFailure is set, a failure conclusion is the success path (mirrors
// executePromote's ExpectFailure handling) and a success conclusion is an error.
// When orch names a component, it runs that component's fanned-out
// orchestrate-<name>.yaml instead of the repo-wide orchestrate.yaml, so a
// components: manifest can seed one component's version line independently.
func (r *Runner) executeOrchestrate(ctx context.Context, config Config, expectFailure bool, orch *OrchestrateStep) error {
	if r.harness == nil || r.harness.act == nil {
		r.t.Log("  Would execute orchestrate workflow (no harness)")
		return nil
	}

	component := ""
	if orch != nil {
		component = orch.Component
	}
	workflowPath := orchestrateWorkflowPath(component)

	// Get the current HEAD SHA for later reference
	sha, err := r.harness.gitea.getHeadSHA(ctx, r.harness.repo)
	if err != nil {
		return fmt.Errorf("failed to get HEAD SHA: %w", err)
	}

	if component != "" {
		r.t.Logf("  Orchestrate: running %s for SHA %s", workflowPath, truncateSHA(sha))
	} else {
		r.t.Logf("  Orchestrate: running workflow for SHA %s", truncateSHA(sha))
	}

	// Sync the repo to act container before running workflow
	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	// Debug: check what's in /tmp/repo
	debugCmd := []string{"bash", "-c", "cd /tmp/repo && git branch -a && ls -la .github/actions/ && ls -la .github/actions/setup-cli/ && cat " + workflowPath + " | head -50"}
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

	// The event defaults to push (a trunk merge). A dispatch-only orchestrate
	// (release_trigger: dispatch) is run under "push" with ExpectNoRun to prove
	// no job fires, and under "workflow_dispatch" to prove the dispatch path
	// still advances state.
	event := "push"
	if orch != nil && orch.Event != "" {
		event = orch.Event
	}

	// Run the actual orchestrate workflow via ActRunner
	result, err := r.harness.act.RunWorkflowFromRepo(ctx, RunOpts{
		WorkflowPath: workflowPath,
		Event:        event,
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

	// Handle the suppressed-trigger case: the workflow scheduled no jobs because
	// the event matches none of its triggers. act marks a zero-job targeted run
	// as a "failure" (missing/unloadable workflow); ExpectNoRun reinterprets that
	// specific outcome as the success path, proving the trigger was dropped.
	if orch != nil && orch.ExpectNoRun {
		if len(result.Jobs) == 0 {
			r.t.Logf("  Orchestrate: no job ran under event %q, as expected", event)
			return nil
		}
		return fmt.Errorf("expected no orchestrate run under event %q but %d job(s) ran", event, len(result.Jobs))
	}

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
		return workflowFailureError("orchestrate", result)
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

	// Select the promote workflow. A component-scoped step runs that component's
	// fanned-out promote-<name>.yaml (emitted for a components: manifest); the
	// single-component default is the repo-wide promote.yaml, byte-identical to
	// before. The workflow's dispatch inputs (mode/force/...) are identical across
	// both shapes, so only the path changes here.
	workflowPath := promoteWorkflowPath(promote.Component)

	r.t.Logf("  Promote: running %s (mode=%s, target=%s, component=%s)",
		workflowPath, promote.Mode, promote.Target, promote.Component)

	// Sync the repo to act container before running workflow
	if err := r.harness.SyncRepoToActContainer(ctx); err != nil {
		return fmt.Errorf("failed to sync repo: %w", err)
	}

	// Debug: Check if the selected promote workflow exists
	debugCmd := []string{"bash", "-c", fmt.Sprintf(
		"ls -la /tmp/repo/.github/workflows/ && head -30 /tmp/repo/%s 2>&1 || echo '%s not found'",
		workflowPath, workflowPath)}
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
	// cascade options, but a step may set Source to drive a non-default leg.
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
			// Source defaults to the first env (typically dev, the trunk-rooted
			// leg the generator emits cascade options for). A scenario can override
			// it to drive a non-default leg, e.g. test-to-prod sourced from a
			// diverged env to exercise the diverged-source guard.
			source := "dev"
			if len(config.Environments) > 0 {
				source = config.Environments[0]
			}
			if promote.Source != "" {
				source = promote.Source
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
		if promote.RollbackOnFailure {
			// Workflow input is named "rollback_on_failure"; preflight surfaces it
			// to the rollback-<name> jobs, which re-deploy each successful deploy at
			// the target env's previously deployed SHA when any deploy fails.
			inputs["rollback_on_failure"] = "true"
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
		WorkflowPath: workflowPath,
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
		return workflowFailureError("promote", result)
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

	// Delete RC tags for each final release (simulating publish cleanup).
	// Honor the configured tag grammar so custom pre-release tokens (e.g.
	// "beta" with no separator) are reaped, not just the default "-rc." shape.
	spec := config.ResolveTagGrammar()
	for _, finalTag := range finalReleaseTags {
		if err := r.harness.gitea.DeleteRCTags(ctx, r.harness.repo, finalTag, spec); err != nil {
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
		// "components" is not an environment: it is the per-component subtree
		// (state.components.<name>.<env>), read separately below via
		// parseComponentStates. Skip it here so it is not recorded as a junk env.
		if env == "components" {
			continue
		}
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

	// Read component-scoped state (state.components.<name>.<env>) written by a
	// per-component promote finalize, recording each row under a composite key so
	// component-scoped assertions can observe that one component advanced while a
	// sibling's subtree stayed byte-intact. ClearState above already dropped any
	// prior composite keys, so wiped/unchanged assertions see a faithful rebuild.
	// The manifest key is "ci" by default, matching the flat parse above (the
	// config parameter shadows the config package here, so use the literal).
	components, err := parseComponentStates(manifestContent, "ci")
	if err != nil {
		r.t.Logf("  Note: could not parse component state: %v", err)
		return nil
	}
	for comp, envs := range components {
		for env, st := range envs {
			key := componentStateKey(comp, env)
			r.ctx.RecordState(key, st.SHA, st.Version)
			r.t.Logf("  Synced state.components[%s][%s] = %s @ %s",
				comp, env, truncateSHA(st.SHA), st.Version)
			// Record component-scoped divergence so a per-component hotfix or
			// rollback assertion (ref/base_sha/patches under the composite key) can
			// observe the diverged component while a sibling's subtree stays
			// undiverged. Mirrors the flat parse above.
			if st.Ref != "" || st.BaseSHA != "" || len(st.Patches) > 0 {
				r.ctx.RecordStateDivergence(key, st.Ref, st.BaseSHA, st.Patches, "")
				r.t.Logf("  Synced state.components[%s][%s] divergence ref=%s base=%s patches=%d",
					comp, env, st.Ref, truncateSHA(st.BaseSHA), len(st.Patches))
			}
			for deployName, deployState := range st.Deploys {
				r.ctx.RecordDeployState(key, deployName, deployState.SHA)
				r.t.Logf("  Synced state.components[%s][%s].deploys[%s] = %s",
					comp, env, deployName, truncateSHA(deployState.SHA))
			}
		}
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

	// Assert state. The map key is the flat env for a single-component scenario;
	// when the expectation names a Component, the lookup is redirected to that
	// component's composite key (state.components.<component>.<env>), where env
	// defaults to the map key unless an explicit Env disambiguates two components
	// asserted at the same env in one step.
	for key, stateExpect := range expect.State {
		lookupKey := key
		if stateExpect.Component != "" {
			env := stateExpect.Env
			if env == "" {
				env = key
			}
			lookupKey = componentStateKey(stateExpect.Component, env)
		}

		// Handle "unchanged" expectation
		if stateExpect.Unchanged {
			preEnvState := preState.GetState(lookupKey)
			currentState := r.ctx.GetState(lookupKey)
			if preEnvState.SHA != currentState.SHA || preEnvState.Version != currentState.Version {
				allErrs = append(allErrs, fmt.Errorf("state[%s] expected unchanged but changed from %s/%s to %s/%s",
					lookupKey, preEnvState.SHA, preEnvState.Version, currentState.SHA, currentState.Version))
			}
			continue
		}
		errs := AssertState(r.ctx, lookupKey, stateExpect)
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

	// Assert branch presence/absence in Gitea (live).
	if expect.Branches != nil {
		errs := r.assertBranches(ctx, expect.Branches)
		allErrs = append(allErrs, errs...)
	}

	// Assert open pull requests in Gitea (live).
	if expect.PRs != nil {
		errs := r.assertPRs(ctx, expect.PRs)
		allErrs = append(allErrs, errs...)
	}

	// Assert substrings in the live manifest (verifies a state write preserved
	// config fields it does not itself touch).
	if expect.Manifest != nil {
		errs := r.assertManifest(ctx, expect.Manifest)
		allErrs = append(allErrs, errs...)
	}

	// Assert a runtime log marker the last workflow run actually emitted. This
	// reads r.lastWorkflowResult (the same result the Jobs assertion consumes),
	// so it proves a behavior ran rather than that its marker text was rendered
	// into the workflow file. Skipped in unit-test mode where no workflow ran.
	if expect.ExpectLog != "" && r.lastWorkflowResult != nil {
		if !strings.Contains(r.lastWorkflowResult.Logs, expect.ExpectLog) {
			allErrs = append(allErrs, fmt.Errorf("expected workflow logs to contain %q but did not", expect.ExpectLog))
		}
	}

	return allErrs
}

// assertManifest reads the live manifest from Gitea and checks its content
// against the expectation. Returns nil in unit-test mode (no harness). The read
// sees exactly what the last state-writing step committed, so a Contains entry
// that names a config field proves the field survived the write.
func (r *Runner) assertManifest(ctx context.Context, expect *ManifestExpect) []error {
	if r.harness == nil || r.harness.gitea == nil || r.harness.repo == nil {
		return nil
	}
	content, err := r.harness.gitea.GetFileContent(ctx, r.harness.repo, ".github/manifest.yaml")
	if err != nil {
		return []error{fmt.Errorf("read manifest: %w", err)}
	}
	var errs []error
	for _, want := range expect.Contains {
		if !strings.Contains(content, want) {
			errs = append(errs, fmt.Errorf("manifest expected to contain %q but did not:\n%s", want, content))
		}
	}
	for _, unwant := range expect.NotContains {
		if strings.Contains(content, unwant) {
			errs = append(errs, fmt.Errorf("manifest expected NOT to contain %q but did:\n%s", unwant, content))
		}
	}
	return errs
}

// assertBranches checks branch existence in Gitea against the expectation.
// Returns nil in unit-test mode (no harness).
func (r *Runner) assertBranches(ctx context.Context, expect *BranchesExpect) []error {
	if r.harness == nil || r.harness.gitea == nil || r.harness.repo == nil {
		return nil
	}
	branches, err := r.harness.gitea.ListBranches(ctx, r.harness.repo)
	if err != nil {
		return []error{fmt.Errorf("list branches: %w", err)}
	}
	var errs []error
	for _, want := range expect.Exist {
		if !containsString(branches, want) {
			errs = append(errs, fmt.Errorf("branch %s expected to exist but not found", want))
		}
	}
	for _, gone := range expect.Deleted {
		if containsString(branches, gone) {
			errs = append(errs, fmt.Errorf("branch %s expected to be deleted but exists", gone))
		}
	}
	return errs
}

// assertPRs checks open pull requests in Gitea against the expectation. When
// OpenCount is set the count must match exactly; otherwise at least one matching
// PR must be open. Returns nil in unit-test mode (no harness).
func (r *Runner) assertPRs(ctx context.Context, expect *PRsExpect) []error {
	if r.harness == nil || r.harness.gitea == nil || r.harness.repo == nil {
		return nil
	}
	indices, err := r.harness.gitea.ListOpenPRs(ctx, r.harness.repo, "", expect.OpenWithLabel)
	if err != nil {
		return []error{fmt.Errorf("list open PRs: %w", err)}
	}
	var errs []error
	if expect.OpenCount != nil {
		if len(indices) != *expect.OpenCount {
			errs = append(errs, fmt.Errorf("expected %d open PR(s) with label %q, got %d",
				*expect.OpenCount, expect.OpenWithLabel, len(indices)))
		}
		return errs
	}
	if len(indices) == 0 {
		errs = append(errs, fmt.Errorf("expected at least one open PR with label %q, got none", expect.OpenWithLabel))
	}
	return errs
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

package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/hotfix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// threeEnvHotfixConfig returns a 3-environment manifest config suitable for
// exercising the hotfix generator. The first env ("dev") is the build target and
// is excluded from the hotfix target choices; "test" and "prod" are targets.
func threeEnvHotfixConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml"},
		},
		Deploys: []config.DeployConfig{
			{Name: "service", Workflow: ".github/workflows/deploy.yaml"},
		},
	}
}

func TestHotfixGenerator_Enabled(t *testing.T) {
	// Two or more environments enables the hotfix workflow.
	assert.True(t, NewHotfixGenerator(&config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
	}, "").Enabled(), "2 envs should enable the hotfix workflow")

	assert.True(t, NewHotfixGenerator(threeEnvHotfixConfig(), "").Enabled(), "3 envs should enable")

	// Below two environments emits nothing.
	assert.False(t, NewHotfixGenerator(&config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
	}, "").Enabled(), "1 env should not enable")

	assert.False(t, NewHotfixGenerator(&config.TrunkConfig{
		TrunkBranch: "main",
	}, "").Enabled(), "0 envs should not enable")

	// Nil config reports disabled rather than panicking.
	assert.False(t, NewHotfixGenerator(nil, "").Enabled(), "nil config should not enable")
}

// TestHotfixGenerator_Threshold_EmitsNothingBelowTwoEnvs confirms the Q1
// generation threshold: with a single env the generator gate is closed.
func TestHotfixGenerator_Threshold_EmitsNothingBelowTwoEnvs(t *testing.T) {
	oneEnv := &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev"}}
	assert.False(t, NewHotfixGenerator(oneEnv, "").Enabled())

	zeroEnv := &config.TrunkConfig{TrunkBranch: "main"}
	assert.False(t, NewHotfixGenerator(zeroEnv, "").Enabled())
}

func TestHotfixGenerator_Triggers(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "name: Cascade Hotfix")
	assert.Contains(t, content, "workflow_dispatch:")
	assert.Contains(t, content, "pull_request:")
	assert.Contains(t, content, "types: [closed]")
	assert.Contains(t, content, "branches:")
	assert.Contains(t, content, "'env/**'")

	// Dispatch inputs.
	assert.Contains(t, content, "commit:")
	assert.Contains(t, content, "target_env:")
	assert.Contains(t, content, "pr_number:")
	assert.Contains(t, content, "dry_run:")

	// target_env choice options list non-first envs, not the build target.
	assert.Contains(t, content, "- test")
	assert.Contains(t, content, "- prod")
	assert.NotContains(t, content, "- dev")
}

// TestHotfixGenerator_FinalizeTriggerMatchesNestedEnvBranches guards that the
// finalize pull_request trigger's branch filter matches multi-component env
// branches. Per-component env branches carry two path segments
// (env/api/staging), and a GitHub Actions `*` glob stops at a slash, so a
// single-star `env/*` filter never matches them and the closed-PR finalize
// never fires. A double-star `env/**` matches any depth, covering both the
// single-component branch (env/staging) and the per-component branch
// (env/api/staging).
func TestHotfixGenerator_FinalizeTriggerMatchesNestedEnvBranches(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "      - 'env/**'",
		"finalize trigger must use env/** so it matches per-component env branches like env/api/staging")
	assert.NotContains(t, content, "      - 'env/*'\n",
		"finalize trigger must not use a single-star env/* filter, which a GitHub Actions glob will not match across the slash of env/api/staging")
}

// TestHotfixGenerator_CommitInputAcceptsMultiple guards that the dispatch
// `commit` input documents comma-delimited multi-commit hotfixes, so an operator
// can hand the workflow a stack of trunk fixes to cherry-pick as one chain.
func TestHotfixGenerator_CommitInputAcceptsMultiple(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "comma",
		"the commit input description must advertise comma-delimited multi-commit input")
	assert.Contains(t, content,
		"description: 'Trunk commit SHA(s) to hotfix, comma-delimited (must be on trunk)'",
		"the commit input must carry the multi-commit description verbatim")
}

// TestHotfixGenerator_PlanJobChainOutputs guards that the plan job exposes the
// per-env chain outputs the apply job consumes: the bottom-up env_sequence plus
// each target env's commit list, no-op flag, and base SHA.
func TestHotfixGenerator_PlanJobChainOutputs(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	planJob := extractJobSection(t, content, "plan:")
	require.NotEmpty(t, planJob, "plan job section should be present")

	assert.Contains(t, planJob, "env_sequence: ${{ steps.plan.outputs.env_sequence }}",
		"plan job must expose the env_sequence chain order")

	// The single-flight gate and orphan self-heal only run when --repo wires a
	// real gh-backed checker; the plan job must pass the repository slug and carry
	// contents: write so the self-heal force-push to origin can land.
	assert.Contains(t, planJob, `--repo "${{ github.repository }}"`,
		"plan job must pass --repo so the single-flight gate runs with a real checker")
	assert.Contains(t, planJob, "contents: write",
		"plan job must carry contents: write so the orphan self-heal can force-push env/<env>")
	for _, key := range []string{
		"commits_test: ${{ steps.plan.outputs.commits_test }}",
		"no_op_test: ${{ steps.plan.outputs.no_op_test }}",
		"base_test: ${{ steps.plan.outputs.base_test }}",
		"commits_prod: ${{ steps.plan.outputs.commits_prod }}",
		"no_op_prod: ${{ steps.plan.outputs.no_op_prod }}",
		"base_prod: ${{ steps.plan.outputs.base_prod }}",
	} {
		assert.Contains(t, planJob, key, "plan job must expose per-env chain output %q", key)
	}
}

// TestHotfixGenerator_ApplyLoopsEnvSequence guards that the apply job walks the
// planner's env_sequence, cherry-picking each env's commits from the statically
// baked per-env outputs.
func TestHotfixGenerator_ApplyLoopsEnvSequence(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	applyJob := extractJobSection(t, content, "apply:")
	require.NotEmpty(t, applyJob, "apply job section should be present")

	assert.Contains(t, applyJob, "ENV_SEQUENCE: ${{ needs.plan.outputs.env_sequence }}",
		"apply job must consume the planner's env_sequence")
	assert.Contains(t, applyJob, `for env in $(echo "$ENV_SEQUENCE" | tr ',' '\n')`,
		"apply job must loop over the env_sequence")
	assert.Contains(t, applyJob, "git cherry-pick -x",
		"apply job must cherry-pick each commit with -x")
	assert.Contains(t, applyJob, "COMMITS_TEST: ${{ needs.plan.outputs.commits_test }}",
		"apply job must wire the per-env commit list into the cherry-pick step env")
	assert.Contains(t, applyJob, `test) COMMITS="$COMMITS_TEST"`,
		"apply job must resolve the per-env commit list via a case statement")
}

// TestHotfixGenerator_ConflictHaltsChain guards that the conflict path's
// resolution PR body tells the operator which env was resolved, which envs still
// pending, and how to re-engage the chain after merge.
func TestHotfixGenerator_ConflictHaltsChain(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "This resolves %s.",
		"conflict PR body must state which env the PR resolves")
	assert.Contains(t, content, "Environments still pending: %s.",
		"conflict PR body must list the envs the halted chain has not reached")
	assert.Contains(t, content, "After merge, re-engage the hotfix workflow targeting %s.",
		"conflict PR body must tell the operator how to resume the chain")
}

func TestHotfixGenerator_Concurrency(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// The finalize (pull_request close) path keys on a per-repository constant so
	// concurrent per-environment finalize runs queue instead of racing on the
	// shared manifest blob SHA. The apply (dispatch) path stays keyed per target
	// environment so unrelated cherry-picks still run in parallel.
	assert.Contains(t, content,
		"github.event_name == 'pull_request' && format('hotfix-finalize-{0}', github.repository)",
		"finalize must use a per-repository concurrency group so writes queue")
	assert.Contains(t, content, "format('hotfix-{0}', github.event.inputs.target_env)",
		"the dispatch path must stay keyed per target environment")
	assert.Contains(t, content, "cancel-in-progress: false")
}

func TestHotfixGenerator_Permissions(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)
	assert.Contains(t, content, "contents: write")
	assert.Contains(t, content, "pull-requests: write")
	assert.Contains(t, content, "actions: read")
	// issues: write is required so the apply job can call `gh label create` to
	// seed the cascade-hotfix and cascade-hotfix-conflict labels. Without it
	// GitHub returns HTTP 403, the || true swallows it silently, and the
	// subsequent `gh pr create --label` hard-fails.
	assert.Contains(t, content, "issues: write")
}

func TestHotfixGenerator_Jobs(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "  plan:")
	assert.Contains(t, content, "  apply:")
	assert.Contains(t, content, "  check:")
	assert.Contains(t, content, "  finalize:")
	// Build and deploy jobs are emitted per configured callback.
	assert.Contains(t, content, "build-app:")
	assert.Contains(t, content, "deploy-service:")

	// plan runs cascade hotfix plan; finalize runs cascade hotfix finalize.
	assert.Contains(t, content, "cascade hotfix plan")
	assert.Contains(t, content, "cascade hotfix finalize")

	// check job runs the parse-config validity gate.
	assert.Contains(t, content, "cascade parse-config")
}

func TestHotfixGenerator_ConflictPath(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "cascade-hotfix-conflict")
	assert.Contains(t, content, "--force-with-lease")
	assert.Contains(t, content, "Cascade-Hotfix-Target:")
	assert.Contains(t, content, "Cascade-Hotfix-Source:")
	assert.Contains(t, content, "Cascade-Hotfix-Base:")
}

// TestHotfixGenerator_SourceTrailerCarriesFullCommitSet guards the multi-commit
// patch-accumulation fix: the resolution PR's Cascade-Hotfix-Source trailer must
// carry the whole per-env $COMMITS set so the post-merge finalize records every
// cherry-picked commit, not just the first. Stamping only the first commit (the
// old $FIRST_COMMIT trailer) dropped the rest from state.<env>.patches.
func TestHotfixGenerator_SourceTrailerCarriesFullCommitSet(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// The trailer interpolates the full comma-joined per-env commit set.
	assert.Contains(t, content, `Cascade-Hotfix-Source: %s\n`,
		"the Source trailer must be a printf field")
	assert.Contains(t, content, `"$env" "$COMMITS" "$BASE"`,
		"the Source trailer must carry the full per-env $COMMITS set, not just the first commit")
	assert.NotContains(t, content, `"$env" "$FIRST_COMMIT" "$BASE"`,
		"the Source trailer must not stamp only the first commit")

	// The context job recovers the whole Source value (it strips the prefix and
	// keeps the comma-joined remainder) and the finalize job passes it straight
	// to --fix-sha, which the command splits back into the full set.
	assert.Contains(t, content, `sed 's/^Cascade-Hotfix-Source:[[:space:]]*//'`,
		"the context job must keep the whole Source value, not a single field")
	assert.Contains(t, content, `--fix-sha "$FIX_SHA"`,
		"the finalize job must forward the recovered (possibly comma-joined) fix-sha set")
}

func TestHotfixGenerator_CleanPath(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "--label cascade-hotfix")
	assert.Contains(t, content, "gh pr merge --squash --delete-branch \"$BRANCH\"")
}

// TestHotfixGenerator_CleanPathPATMerge guards the structural fix where the
// clean cherry-pick path merged with `gh pr merge --auto`. GitHub auto-merge
// completes the merge as github-actions[bot] under GITHUB_TOKEN, and merges
// authored by GITHUB_TOKEN do not emit pull_request events, so the
// pull_request(closed) finalize chain never fired and state was never recorded
// after a hotfix. The clean path must instead poll until the resolution PR is
// mergeable (so a protected env branch with a required check still gates the
// merge) and then merge with the configured state token, which is trigger
// capable and reaches the finalize chain.
func TestHotfixGenerator_CleanPathPATMerge(t *testing.T) {
	cfg := threeEnvHotfixConfig()
	cfg.StateToken = "${{ secrets.CASCADE_BOT_TOKEN }}"
	gen := NewHotfixGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// The clean path no longer relies on auto-merge, which suppresses the
	// finalize trigger.
	assert.NotContains(t, content, "gh pr merge --auto",
		"clean path must not use auto-merge; it suppresses the pull_request finalize trigger")

	// The merge step authenticates with the configured state token so the
	// merge actor is trigger capable and finalize is reachable.
	assert.Contains(t, content, "GH_TOKEN: ${{ secrets.CASCADE_BOT_TOKEN }}",
		"the clean-path merge step must authenticate with the configured state token")

	// A poll-until-mergeable construct gates the merge so a protected env
	// branch with a required check still blocks until the check is green.
	assert.Contains(t, content, "gh pr view \"$BRANCH\" --json mergeable",
		"the clean path must poll PR mergeability before merging")
	assert.Contains(t, content, "gh pr merge --squash --delete-branch \"$BRANCH\"",
		"the clean path must squash-merge once the PR is mergeable")
}

// TestHotfixGenerator_CleanPathMergeDefaultsToGitHubToken confirms that when no
// state token is configured the merge step degrades to the default
// GITHUB_TOKEN expression, matching the state-write token plumbing used by the
// release and promote generators.
func TestHotfixGenerator_CleanPathMergeDefaultsToGitHubToken(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.NotContains(t, content, "gh pr merge --auto")
	assert.Contains(t, content, "GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}")
}

// applyJobGHToken extracts the apply job's job-level GH_TOKEN expression from a
// generated hotfix workflow. It parses the workflow as YAML so the assertion
// targets the job-level env value rather than any step-level override, isolating
// the actor that authors the resolution PR via gh pr create.
func applyJobGHToken(t *testing.T, content string) string {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Env map[string]string `yaml:"env"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(content), &wf))
	apply, ok := wf.Jobs["apply"]
	require.True(t, ok, "apply job must be present")
	return apply.Env["GH_TOKEN"]
}

// TestHotfixGenerator_ApplyCreatesPRWithStateToken guards the structural fix for
// the protected-env-branch deadlock. The apply job opens the resolution PR with
// gh pr create, which authenticates with the job-level GH_TOKEN. When that token
// is the default GITHUB_TOKEN the PR is authored by github-actions[bot], and a
// bot-authored PR does not trigger on: pull_request workflows. The env-branch
// required check then can only post via on: workflow_run after the hotfix run
// finishes, but the apply job will not finish until the PR merges, the PR cannot
// merge until the check posts, and the check cannot post until the apply job
// finishes: a deadlock. Authoring the PR with the trigger-capable state token
// fires on: pull_request so the required check posts on PR open, independent of
// the apply job, breaking the cycle.
func TestHotfixGenerator_ApplyCreatesPRWithStateToken(t *testing.T) {
	cfg := threeEnvHotfixConfig()
	cfg.StateToken = "${{ secrets.CASCADE_BOT_TOKEN }}"
	gen := NewHotfixGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// The apply job's job-level GH_TOKEN, which gh pr create uses to author the
	// resolution PR, must be the configured state token, not bare GITHUB_TOKEN.
	assert.Equal(t, "${{ secrets.CASCADE_BOT_TOKEN }}", applyJobGHToken(t, content),
		"the apply job must author the resolution PR with the trigger-capable state token so on: pull_request fires and the env-branch required check posts on PR open")
}

// TestHotfixGenerator_ApplyTokenDefaultsToGitHubToken confirms back-compat: when
// no state token is configured the apply job's GH_TOKEN degrades to the default
// GITHUB_TOKEN expression, matching the token plumbing used elsewhere. Post-hotfix
// automation (the env-branch check firing on PR open and the finalize chain)
// requires a configured state_token, consistent with the merge step's caveat.
func TestHotfixGenerator_ApplyTokenDefaultsToGitHubToken(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Equal(t, "${{ secrets.GITHUB_TOKEN }}", applyJobGHToken(t, content),
		"with no state token configured the apply job must fall back to GITHUB_TOKEN")
}

// TestHotfixGenerator_SeedsLabels guards the regression where the apply job ran
// `gh pr create --label cascade-hotfix[-conflict]` without ever creating those
// labels. `gh pr create --label X` hard-fails when label X does not exist, so
// both the clean and conflict resolution PR paths broke. The apply job must seed
// both labels before the cherry-pick step opens any PR.
func TestHotfixGenerator_SeedsLabels(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Both labels the resolution PRs reference must be created in the workflow.
	assert.Contains(t, content, "gh label create cascade-hotfix ",
		"apply job must seed the clean-path label so gh pr create --label does not hard-fail")
	assert.Contains(t, content, "gh label create cascade-hotfix-conflict ",
		"apply job must seed the conflict-path label so gh pr create --label does not hard-fail")

	// The seed must run before the cherry-pick step that opens the labeled PRs.
	seedIdx := strings.Index(content, "gh label create cascade-hotfix ")
	cherryPickIdx := strings.Index(content, "Cherry-pick and open resolution PR")
	require.NotEqual(t, -1, seedIdx, "label seed step must be present")
	require.NotEqual(t, -1, cherryPickIdx, "cherry-pick step must be present")
	assert.Less(t, seedIdx, cherryPickIdx,
		"the label seed step must appear before the cherry-pick/open-PR step")
}

func TestHotfixGenerator_Q2BranchProtectionWarn(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// gh api branch-protection call (slash URL-encoded as %2F) plus a loud warning.
	assert.Contains(t, content, "branches/env")
	assert.Contains(t, content, "protection")
	assert.Contains(t, content, "::warning::")
}

func TestHotfixGenerator_Q6ProtectionSuggestions(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)
	assert.Contains(t, content, "protection_suggestions")
}

func TestHotfixGenerator_ProdGatingEnvironment(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)
	// Deploy job must carry an environment: key for org protection gating.
	assert.Contains(t, content, "environment:")
}

func TestHotfixGenerator_DryRunSafety(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// apply job is skipped on dry-run; plan forwards --dry-run.
	assert.Contains(t, content, "dry_run != 'true'")
	assert.Contains(t, content, "--dry-run")
}

func TestHotfixGenerator_MergedLabelGate(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "github.event.pull_request.merged == true")
	assert.Contains(t, content, "'cascade-hotfix')")
}

func TestHotfixGenerator_ValidYAML(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(content), &parsed), "emitted workflow must be valid YAML")
	assert.Contains(t, parsed, "jobs")
	assert.Contains(t, parsed, "on")
	assert.Contains(t, parsed, "permissions")
}

// TestHotfixGenerator_SetupCLIPassesToken asserts that the setup-cli step
// passes github.token so that gh release download can authenticate on a cold
// tool-cache. Without the token: input the composite action's GH_TOKEN is
// empty and gh exits non-zero.
func TestHotfixGenerator_SetupCLIPassesToken(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "token: ${{ github.token }}",
		"setup-cli step must pass github.token so gh release download succeeds on a cold cache")
}

// TestHotfixGenerator_PinModeSHA confirms third-party action refs route through
// the shared pin helper rather than emitting a raw @v4.
func TestHotfixGenerator_PinModeSHA(t *testing.T) {
	cfg := threeEnvHotfixConfig()
	cfg.PinMode = config.PinModeSHA
	gen := NewHotfixGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "uses: actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0")
	assert.NotContains(t, content, "uses: actions/checkout@v4")
}

// TestHotfixGeneratorE2E exercises the manifest -> parse -> generate path: a
// 3-env manifest enables the hotfix workflow; a single-env manifest disables it.
func TestHotfixGeneratorE2E(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - test
      - prod
`
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0644))

	cfg, err := config.ParseWithKey(manifestPath, "ci")
	require.NoError(t, err)

	gen := NewHotfixGenerator(cfg, tmpDir)
	require.True(t, gen.Enabled(), "3-env manifest should enable the hotfix workflow")
	content, err := gen.Generate()
	require.NoError(t, err)
	assert.Contains(t, content, "name: Cascade Hotfix")
	assert.Contains(t, content, "cascade hotfix plan")
	assert.Contains(t, content, "- test")
	assert.Contains(t, content, "- prod")

	// Single-env manifest reports disabled: nothing is emitted.
	single := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
`
	require.NoError(t, os.WriteFile(manifestPath, []byte(single), 0644))
	singleCfg, err := config.ParseWithKey(manifestPath, "ci")
	require.NoError(t, err)
	assert.False(t, NewHotfixGenerator(singleCfg, tmpDir).Enabled(), "single-env manifest emits nothing")
}

// TestHotfixGenerator_ContextEmitsNonEmptyRollbackSHA guards the rollback_sha
// wiring regression: the context job formerly emitted an empty `rollback_sha=`,
// so the rollback-<deploy> gate (which requires rollback_sha != '') never fired.
// The context job must check out the repo and read the target env's pre-hotfix
// state SHA from the manifest via yq, then emit it as the rollback_sha output.
func TestHotfixGenerator_ContextEmitsNonEmptyRollbackSHA(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	contextJob := extractJobSection(t, content, "context:")
	require.NotEmpty(t, contextJob, "context job section should be present")

	// The context job must check out the repo so the manifest is on disk for yq.
	assert.Contains(t, contextJob, "actions/checkout",
		"context job must check out the repo to read the manifest")

	// It must read the target env's pre-hotfix state SHA from the manifest.
	assert.Contains(t, contextJob, "yq eval",
		"context job must read the rollback SHA from the manifest via yq")
	assert.Contains(t, contextJob, ".state.",
		"context job must read .state.<env>.sha from the manifest")
	assert.Contains(t, contextJob, ".sha",
		"context job must read the state sha field")

	// The empty placeholder echo must be gone, replaced by a non-empty assignment.
	assert.NotContains(t, contextJob, `echo "rollback_sha="`,
		"context job must not emit an empty rollback_sha placeholder")
	assert.Contains(t, contextJob, "rollback_sha=${ROLLBACK_SHA}",
		"context job must emit the resolved rollback SHA as its output")
}

// TestHotfixGenerator_RollbackJobGatedCorrectly confirms the rollback job exists
// and is gated on a non-empty rollback_sha and a failed deploy, mirroring the
// promote workflow's rollback shape.
func TestHotfixGenerator_RollbackJobGatedCorrectly(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "  rollback-service:",
		"a rollback job must be emitted per deploy")
	assert.Contains(t, content,
		"if: always() && needs.context.outputs.rollback_sha != '' && needs.deploy-service.result == 'failure'",
		"rollback job must be gated on a non-empty rollback_sha and a failed deploy")
}

// hotfixFlagInvocation captures a single `cascade hotfix <subcommand> --flag`
// pairing parsed from the generated workflow, for cross-checking against the
// real cobra command tree.
type hotfixFlagInvocation struct {
	subcommand string
	flag       string
	line       string
}

// parseHotfixInvocations scans the generated workflow for every
// `cascade hotfix <subcommand> ...` invocation and the long-form flags it
// passes, returning one entry per (subcommand, flag) pair. It handles the
// shell line-continuation style the generator emits (a leading verb line such
// as `cascade hotfix finalize \` followed by `  --flag "..." \` lines).
func parseHotfixInvocations(content string) []hotfixFlagInvocation {
	var (
		invocations []hotfixFlagInvocation
		current     string
	)
	invokeRe := regexp.MustCompile(`cascade hotfix (\S+)`)
	flagRe := regexp.MustCompile(`(?:^|\s)--([a-zA-Z][a-zA-Z0-9-]*)`)

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)

		if m := invokeRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			// A verb line can also carry flags on the same physical line.
			for _, fm := range flagRe.FindAllStringSubmatch(line, -1) {
				invocations = append(invocations, hotfixFlagInvocation{
					subcommand: current,
					flag:       fm[1],
					line:       line,
				})
			}
			continue
		}

		if current == "" {
			continue
		}

		flags := flagRe.FindAllStringSubmatch(line, -1)
		if len(flags) == 0 {
			// A line with no flags that does not continue the command ends it.
			if !strings.HasSuffix(line, `\`) {
				current = ""
			}
			continue
		}
		for _, fm := range flags {
			invocations = append(invocations, hotfixFlagInvocation{
				subcommand: current,
				flag:       fm[1],
				line:       line,
			})
		}
		if !strings.HasSuffix(line, `\`) {
			current = ""
		}
	}
	return invocations
}

// subcommandByName returns the named direct subcommand of root, or nil.
func subcommandByName(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// TestHotfixGenerator_EmittedFlagsExistOnCommands is a regression guard against
// the whole class of "generated workflow passes a flag the CLI does not define"
// bug: for every `cascade hotfix <subcommand> --flag` the generated YAML emits,
// the flag must be a real registered flag on that subcommand's cobra command.
// It builds the command tree from the same constructor the CLI wires up, so the
// check tracks the real flag set rather than a hand-maintained list.
func TestHotfixGenerator_EmittedFlagsExistOnCommands(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	root := hotfix.NewCommand()

	invocations := parseHotfixInvocations(content)
	require.NotEmpty(t, invocations, "expected to parse at least one cascade hotfix invocation")

	// Sanity: the bug-prone subcommands are actually exercised by the workflow.
	seen := map[string]bool{}
	for _, inv := range invocations {
		seen[inv.subcommand] = true
	}
	assert.True(t, seen["plan"], "workflow should invoke cascade hotfix plan")
	assert.True(t, seen["finalize"], "workflow should invoke cascade hotfix finalize")

	for _, inv := range invocations {
		sub := subcommandByName(root, inv.subcommand)
		require.NotNilf(t, sub, "generated workflow invokes unknown subcommand %q (line: %s)", inv.subcommand, inv.line)
		assert.NotNilf(t, sub.Flags().Lookup(inv.flag),
			"generated `cascade hotfix %s` passes --%s, which is not a registered flag on that command (line: %s)",
			inv.subcommand, inv.flag, inv.line)
	}
}

// TestHotfixGenerator_FinalizeRequiredFlags asserts the finalize invocation
// supplies every flag the command marks required, threaded from upstream job
// outputs and the workflow context.
func TestHotfixGenerator_FinalizeRequiredFlags(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	for _, flag := range []string{"--target-env", "--merge-sha", "--fix-sha", "--base-sha"} {
		assert.Contains(t, content, flag,
			"finalize invocation must pass the required %s flag", flag)
	}
	// The retired alias must not reappear.
	assert.NotContains(t, content, "cascade hotfix finalize \\\n            --sha ",
		"finalize must not pass the nonexistent --sha flag")

	// The plan job must expose fix_sha as a job output (the planner emits it),
	// matching the planner's GHA output key.
	assert.Contains(t, content, "fix_sha: ${{ steps.plan.outputs.fix_sha }}",
		"plan job must expose fix_sha as a job output")

	// On the merged-PR finalize path the plan job does not run, so fix-sha and
	// base-sha are recovered by the context job (from the resolution PR-body
	// trailers) and finalize consumes them via needs.context.outputs.
	assert.Contains(t, content, "fix_sha: ${{ steps.ctx.outputs.fix_sha }}",
		"context job must expose fix_sha for finalize to consume")
	assert.Contains(t, content, "needs.context.outputs.fix_sha",
		"finalize must thread fix-sha from the context job output")
	assert.Contains(t, content, "needs.context.outputs.base_sha",
		"finalize must thread base-sha from the context job output")
}

// TestHotfixGenerator_PlanJobOutputsMatchPlannerKeys guards the plan job's
// declared outputs against the GHA output keys the planner actually writes:
// referencing a steps.plan.outputs.<key> that the CLI never sets silently
// yields an empty value at runtime.
func TestHotfixGenerator_PlanJobOutputsMatchPlannerKeys(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Keys the planner emits via writePlanGHAOutput.
	refRe := regexp.MustCompile(`steps\.plan\.outputs\.([a-zA-Z0-9_]+)`)
	plannerKeys := map[string]bool{
		"target_env": true, "fix_sha": true, "branch": true, "base_sha": true,
		"no_op": true, "branch_created": true, "hotfix_version_candidate": true,
		"conflict_expected": true, "dry_run": true,
		"protection_suggestions": true, "protection_suggestions_text": true,
		// Chain keys emitted by chainGHAOutputs for the target envs (test, prod).
		"env_sequence": true, "env_count": true,
		"commits_test": true, "no_op_test": true, "conflict_expected_test": true, "base_test": true,
		"commits_prod": true, "no_op_prod": true, "conflict_expected_prod": true, "base_prod": true,
	}
	for _, m := range refRe.FindAllStringSubmatch(content, -1) {
		assert.Truef(t, plannerKeys[m[1]],
			"plan job references steps.plan.outputs.%s, which the planner never sets", m[1])
	}
}

// TestHotfixGenerator_Actionlint runs actionlint over the generated workflow
// when the binary is available on PATH. It is skipped otherwise so the unit
// suite stays hermetic.
func TestHotfixGenerator_Actionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	wfPath := filepath.Join(wfDir, "cascade-hotfix.yaml")
	require.NoError(t, os.WriteFile(wfPath, []byte(content), 0644))

	// actionlint resolves local reusable-workflow refs (`uses: ./...`) against the
	// enclosing git repository root, discovered via `git rev-parse --show-toplevel`
	// from the linted file's directory. t.TempDir() can sit inside this repository,
	// which would make actionlint resolve `./.github/workflows/<x>.yaml` against the
	// real repo root rather than the temp dir. Initialize the temp dir as its own
	// git repository so it becomes the project root and resolution stays scoped to
	// the stubs written below.
	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")

	// The generated workflow may reference local reusable workflows via
	// `uses: ./.github/workflows/<x>.yaml`. actionlint resolves those `./`-prefixed
	// refs against the filesystem and validates that the referenced workflows are
	// well-formed workflow_call workflows. Write a minimal valid stub for every
	// such reference the generator emits so resolution stays honest (rather than
	// suppressing the workflow-call check) and the test tracks fixture changes.
	writeReusableWorkflowStubs(t, dir, content)

	cmd := exec.Command(bin, wfPath)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues:\n%s", string(out))
}

// reusableWorkflowStub is a minimal valid workflow_call reusable workflow that
// satisfies actionlint's resolution of local `uses: ./...` references. It
// declares the inputs the hotfix generator passes to a reusable build workflow
// (sha, target_env) so actionlint can validate the call site's `with:` block
// against the called workflow's declared inputs under full strictness.
const reusableWorkflowStub = `name: Stub
on:
  workflow_call:
    inputs:
      sha:
        required: false
        type: string
      target_env:
        required: false
        type: string
jobs:
  stub:
    runs-on: ubuntu-latest
    steps:
      - run: 'true'
`

// writeReusableWorkflowStubs scans the generated workflow content for local
// reusable-workflow references of the form `uses: ./.github/workflows/<x>.yaml`
// and writes a minimal valid stub workflow at each referenced path under root,
// so actionlint can resolve and validate every call site.
func writeReusableWorkflowStubs(t *testing.T, root, content string) {
	t.Helper()

	const marker = "uses: ./"
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		idx := strings.Index(trimmed, marker)
		if idx < 0 {
			continue
		}
		ref := strings.Fields(trimmed[idx+len("uses: "):])[0]
		// ref is like "./.github/workflows/build.yaml"; strip the leading "./".
		rel := strings.TrimPrefix(ref, "./")
		stubPath := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(stubPath), 0755))
		require.NoError(t, os.WriteFile(stubPath, []byte(reusableWorkflowStub), 0644))
	}
}

// TestHotfixGenerator_PlanAndFinalizePassConfig guards the regression where the
// plan and finalize invocations ran without --config and resolved the manifest
// from an implicit default that does not exist in the runner. Both CLI calls
// must thread the explicit manifest path so they parse the same config the
// workflow was generated from. The assertions are scoped per job so a --config
// that appears only in one job cannot mask a missing flag in the other.
func TestHotfixGenerator_PlanAndFinalizePassConfig(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	planJob := extractJobSection(t, content, "plan:")
	require.NotEmpty(t, planJob, "plan job section should be present")
	assert.Contains(t, planJob, "cascade hotfix plan",
		"plan job should invoke cascade hotfix plan")
	assert.Contains(t, planJob, "--config ",
		"plan job must pass --config so the verb parses the generated manifest")

	finalizeJob := extractJobSection(t, content, "finalize:")
	require.NotEmpty(t, finalizeJob, "finalize job section should be present")
	assert.Contains(t, finalizeJob, "cascade hotfix finalize",
		"finalize job should invoke cascade hotfix finalize")
	assert.Contains(t, finalizeJob, "--config ",
		"finalize job must pass --config so the verb parses the generated manifest")
}

// TestHotfixGenerator_ApplyMaterializesAbsentEnvBranch guards the first-hotfix
// regression where the apply job branched from origin/env/<env>, a remote ref
// that does not exist until an env branch has been pushed. The plan verb creates
// env/<env> only locally, so the first hotfix into an environment must
// materialize and push it from the validated base SHA before cherry-picking.
func TestHotfixGenerator_ApplyMaterializesAbsentEnvBranch(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	applyJob := extractJobSection(t, content, "apply:")
	require.NotEmpty(t, applyJob, "apply job section should be present")

	// The hotfix branch must be cut from the per-env validated base SHA, not from
	// a remote-tracking ref that may not exist on a first hotfix.
	assert.Contains(t, applyJob, `git switch -c "$BRANCH" "$BASE"`,
		"apply job must branch the hotfix from the per-env validated BASE")
	assert.NotContains(t, applyJob, `git switch -c "$BRANCH" "origin/env/${env}"`,
		"apply job must not branch from origin/env/<env>, which is absent on a first hotfix")

	// When the remote env branch is absent the apply job must create and push it
	// at the per-env base so the resolution PR has a base to target.
	assert.Contains(t, applyJob, `refs/remotes/origin/env/${env}`,
		"apply job must probe for the remote env branch before relying on it")
	assert.Contains(t, applyJob, `git push origin "${BASE}:refs/heads/env/${env}"`,
		"apply job must push env/<env> at the per-env base when it is absent")
}

// TestHotfixGenerator_ApplySkippedOnNoOp guards that the apply job does not run
// when the planner reports a no-op (the fix is already contained in the target
// state SHA): there is nothing to cherry-pick, and attempting one would fail.
// The gate depends on the plan job exposing no_op as a job output.
func TestHotfixGenerator_ApplySkippedOnNoOp(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	planJob := extractJobSection(t, content, "plan:")
	require.NotEmpty(t, planJob, "plan job section should be present")
	assert.Contains(t, planJob, "no_op: ${{ steps.plan.outputs.no_op }}",
		"plan job must expose no_op so the single-env plan path stays available")

	// no_op is now per-env; the job-level gate checks env_sequence non-empty, and
	// per-env no-ops are skipped inside the loop when that env's COMMITS is empty.
	applyJob := extractJobSection(t, content, "apply:")
	require.NotEmpty(t, applyJob, "apply job section should be present")
	assert.Contains(t, applyJob, "needs.plan.outputs.env_sequence != ''",
		"apply job must skip when the plan reports no envs to process")
}

// TestHotfixGenerator_FinalizeWritesStateWithStateToken guards the trunk
// branch-protection block on the finalize state write. The finalize step runs
// `cascade hotfix finalize`, which performs a Contents REST API write to the
// trunk manifest. Authored by the default GITHUB_TOKEN (github-actions[bot]) that
// write is rejected by trunk's require-pull-request rule with a 409. The state
// token is an admin PAT configured to bypass that rule (enforce_admins=false),
// exactly as the promote and orchestrate finalize state writes do. The step's
// GH_TOKEN must therefore carry the configured state token, not bare
// GITHUB_TOKEN.
func TestHotfixGenerator_FinalizeWritesStateWithStateToken(t *testing.T) {
	cfg := threeEnvHotfixConfig()
	cfg.StateToken = "${{ secrets.CASCADE_BOT_TOKEN }}"
	gen := NewHotfixGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	finalizeJob := extractJobSection(t, content, "finalize:")
	require.NotEmpty(t, finalizeJob, "finalize job section should be present")

	// The Contents API write that finalize performs against the protected trunk
	// must authenticate with the trigger-capable state token, mirroring promote.
	assert.Contains(t, finalizeJob, "GH_TOKEN: ${{ secrets.CASCADE_BOT_TOKEN }}",
		"finalize must write trunk state with the state token so it bypasses trunk's require-PR protection; bare GITHUB_TOKEN is blocked with a 409")
	assert.NotContains(t, finalizeJob, "GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
		"finalize GH_TOKEN must not be the bot GITHUB_TOKEN when a state token is configured")
}

// TestHotfixGenerator_FinalizeStateTokenDefaultsToGitHubToken confirms back-compat:
// with no state token configured the finalize step's GH_TOKEN degrades to the
// default GITHUB_TOKEN expression, matching the token plumbing used by the apply,
// merge, promote, and orchestrate state writes. A repo with a protected trunk
// must configure a bypass-capable state_token for finalize to succeed there.
func TestHotfixGenerator_FinalizeStateTokenDefaultsToGitHubToken(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	finalizeJob := extractJobSection(t, content, "finalize:")
	require.NotEmpty(t, finalizeJob, "finalize job section should be present")
	assert.Contains(t, finalizeJob, "GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
		"with no state token configured the finalize GH_TOKEN must fall back to GITHUB_TOKEN")
}

// TestHotfixGenerator_FinalizeFetchesEnvBranches guards that the finalize job
// fetches the env/* branches before running the verb. finalize cross-checks the
// merge SHA against the env-branch tip; without the fetch the branch is absent
// from the fresh checkout and the verb fails resolving it.
func TestHotfixGenerator_FinalizeFetchesEnvBranches(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	finalizeJob := extractJobSection(t, content, "finalize:")
	require.NotEmpty(t, finalizeJob, "finalize job section should be present")
	assert.Contains(t, finalizeJob, "Fetch env branches and tags",
		"finalize job must fetch env branches before the env-branch tip cross-check")
	assert.Contains(t, finalizeJob, "refs/heads/env/*:refs/remotes/origin/env/*",
		"finalize job must fetch the env/* refs into remote-tracking refs")
}

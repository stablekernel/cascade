package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A GitHub Actions job result is immutable: once deploy-web ends in 'failure',
// needs.deploy-web.result reports 'failure' for the rest of the run even when a
// retry shim re-invokes the same workflow and succeeds. Any gate that reads the
// base job's result alone therefore contradicts what actually happened on the
// environment. These tests pin the effective result, meaning "did any attempt in
// the base-plus-shim ladder succeed?", across the four shapes a ladder can take.

// retriesFixture writes a deploy callback workflow and returns a config whose
// single deploy declares the requested retry count.
func retriesFixture(t *testing.T, retries int) (*config.TrunkConfig, string) {
	t.Helper()

	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0o755))

	deployWorkflow := `
name: Deploy Web
on:
  workflow_call:
    inputs:
      environment:
        type: string
`
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/deploy.yaml"),
		[]byte(deployWorkflow), 0o644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Deploys: []config.DeployConfig{
			{
				Name:     "web",
				Workflow: ".github/workflows/deploy.yaml",
				Triggers: []string{"src/**"},
				Retries:  retries,
			},
		},
	}
	return cfg, tmpDir
}

// TestEffectiveResult_FailureGate_ToleratesSucceedingRetry is the core F1
// regression. With retries: 2, a run where deploy-web fails and
// deploy-web-retry-1 succeeds has genuinely deployed the environment, so the
// finalize failure gate must not fire. Before the fix the gate read only
// needs.deploy-web.result, which is frozen at 'failure', and the run went red
// even though the retry succeeded.
func TestEffectiveResult_FailureGate_ToleratesSucceedingRetry(t *testing.T) {
	cfg, tmpDir := retriesFixture(t, 2)

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	// The failure gate must consult the retry shims, not the base job alone.
	assert.Contains(t, result,
		"!(needs.deploy-web-retry-1.result == 'success' || needs.deploy-web-retry-2.result == 'success')",
		"failure gate must exonerate a ladder in which some attempt succeeded")
}

// TestEffectiveResult_FailureGate_LadderClauseIsParenthesized guards the gate's
// grouping. Callbacks are joined with " || ", so an unparenthesized ladder
// clause would read "A || B && !C" and rely on GitHub Actions binding && more
// tightly than ||. That happens to be true, but a gate deciding whether a run
// goes red must not rest on implicit precedence: the clause is grouped as a unit.
func TestEffectiveResult_FailureGate_LadderClauseIsParenthesized(t *testing.T) {
	cfg, tmpDir := retriesFixture(t, 2)
	// A second, retry-free callback forces the " || " join that makes grouping matter.
	cfg.Builds = []config.BuildConfig{{
		Name:     "app",
		Workflow: ".github/workflows/deploy.yaml",
		Triggers: []string{"src/**"},
	}}

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, result,
		"(contains(fromJSON('[\"failure\", \"cancelled\"]'), needs.deploy-web.result) && "+
			"!(needs.deploy-web-retry-1.result == 'success' || needs.deploy-web-retry-2.result == 'success'))",
		"the ladder clause must be parenthesized as a unit, not left to operator precedence")
}

// TestEffectiveResult_ManifestGate_RecordsSucceedingRetry is the second half of
// F1. The manifest update gates the deploys.web.* yq edits on WEB_RESULT, which
// was bound to the immutable base result. A retry that deployed the environment
// was therefore denied in recorded state.
func TestEffectiveResult_ManifestGate_RecordsSucceedingRetry(t *testing.T) {
	cfg, tmpDir := retriesFixture(t, 2)

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.NotContains(t, result, "WEB_RESULT: ${{ needs.deploy-web.result }}",
		"WEB_RESULT must not bind to the immutable base result when retries are declared")
	assert.Contains(t, result,
		"WEB_RESULT: ${{ (needs.deploy-web.result == 'success' || needs.deploy-web-retry-1.result == 'success' || needs.deploy-web-retry-2.result == 'success') && 'success' || 'failure' }}",
		"WEB_RESULT must reflect whether any attempt in the ladder succeeded")
}

// TestEffectiveResult_ZeroRetries_IsUnchanged pins the frozen-schema promise:
// a manifest that does not use retries must emit exactly what it emits today.
// The effective-result expression must collapse to the bare base result when
// there are no shims to consult.
func TestEffectiveResult_ZeroRetries_IsUnchanged(t *testing.T) {
	cfg, tmpDir := retriesFixture(t, 0)

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "WEB_RESULT: ${{ needs.deploy-web.result }}",
		"a retry-free deploy must keep the bare base result")
	assert.Contains(t, result,
		"contains(fromJSON('[\"failure\", \"cancelled\"]'), needs.deploy-web.result)",
		"a retry-free deploy must keep today's failure gate verbatim")
	// Scoped to the ladder: the state-push shell legitimately says "retrying".
	assert.NotContains(t, result, "deploy-web-retry-", "a retry-free deploy must emit no ladder")
}

// TestEffectiveResult_SkippedBaseIsNotAFailure guards the crux of the design.
// A shim whose predecessor did not fail is 'skipped', NOT 'failure', and a base
// job whose triggers did not match is also 'skipped'. Expressing effective
// failure as "not success" would turn both of those into spurious run failures.
// The gate must stay anchored on the base job's failure/cancelled states and
// only then ask whether a retry rescued it.
func TestEffectiveResult_SkippedBaseIsNotAFailure(t *testing.T) {
	cfg, tmpDir := retriesFixture(t, 2)

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	// The gate keeps the failure/cancelled anchor, so a skipped base (triggers
	// did not match) never trips it, and it never degrades to a bare !success.
	assert.Contains(t, result,
		"contains(fromJSON('[\"failure\", \"cancelled\"]'), needs.deploy-web.result) && "+
			"!(needs.deploy-web-retry-1.result == 'success' || needs.deploy-web-retry-2.result == 'success')",
		"effective failure must anchor on the base failing, then ask if a retry rescued it")
	assert.NotContains(t, result, "!(needs.deploy-web.result == 'success')",
		"a bare !success gate would turn a skipped base into a spurious failure")
}

// TestEffectiveResult_AllAttemptsFail_StillFails is the negative control: the
// fix must not make a genuinely failed ladder look green. When the base and
// every shim fail, the gate must still fire and the manifest must still refuse
// to record the deploy.
func TestEffectiveResult_AllAttemptsFail_StillFails(t *testing.T) {
	cfg, tmpDir := retriesFixture(t, 2)

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	// The gate is still present and still exits non-zero.
	assert.Contains(t, result, "- name: Check for Failures")
	assert.Contains(t, result, "exit 1")
	// The manifest write is still conditional rather than unconditional.
	assert.Contains(t, result, `if [[ "$WEB_RESULT" == "success" ]]; then`)
}

// TestEffectiveResult_Promote_DoesNotEmitRetryLadder pins the scope decision for
// F2, so that a later author who adds a promote ladder must confront the reason
// one is not there today rather than discovering it in production.
//
// retries is an orchestrate-only capability. A promote deploy that declares
// inputs compiles to a matrix job fanned across environments with
// fail-fast: false (see writeDeployJobs), and a matrix job exposes a single
// aggregate result: 'failure' if ANY leg failed. A caller-side shim gated on
// that aggregate re-invokes the reusable workflow for EVERY leg, redeploying the
// environments that already succeeded. GitHub Actions offers no caller-side way
// to re-run only the failed legs of a dependency's matrix, so a ladder here
// would redeploy healthy production environments on account of an unrelated
// environment's transient failure. That is worse than no retry.
//
// Emitting a ladder only on the non-matrix path was rejected too: it would make
// retries silently work or not work depending on whether the deploy happened to
// declare inputs. The documented claim is scoped to orchestrate instead.
func TestEffectiveResult_Promote_DoesNotEmitRetryLadder(t *testing.T) {
	for _, retries := range []int{0, 2} {
		cfg, tmpDir := retriesFixture(t, retries)

		result, err := NewPromoteGenerator(cfg, tmpDir).Generate()
		require.NoError(t, err)

		assert.NotContains(t, result, "deploy-web-retry-",
			"promote emits no retry ladder at retries=%d; see this test's rationale", retries)
	}
}

// TestEffectiveResult_Promote_RetriesDoNotPerturbOutput proves retries is inert
// on the promote path rather than partially wired: a manifest that declares
// retries must emit byte-identical promote output to one that does not.
func TestEffectiveResult_Promote_RetriesDoNotPerturbOutput(t *testing.T) {
	withRetries, dirA := retriesFixture(t, 2)
	without, dirB := retriesFixture(t, 0)

	a, err := NewPromoteGenerator(withRetries, dirA).Generate()
	require.NoError(t, err)
	b, err := NewPromoteGenerator(without, dirB).Generate()
	require.NoError(t, err)

	assert.Equal(t, b, a, "retries must not perturb promote output while it is orchestrate-only")
}

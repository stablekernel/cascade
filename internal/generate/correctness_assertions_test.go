package generate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// This file holds the generation-CORRECTNESS assertions the census
// (correctness_census_test.go) maps emitted-affecting fields to. Each pins the
// EXACT emitted shape for a field whose output carries a distinct semantic
// contract (least-privilege, secret propagation direction, pinned refs, trigger
// expressions), beyond the validity the actionlint sweeps already guarantee.
// Each is red-first: deliberately breaking its emitter makes it fail.

// correctnessDir stages the orchestrate/deploy callback stubs guardBaseConfig
// references, so the correctness assertions generate a real workflow set.
func correctnessDir(t *testing.T) string {
	t.Helper()
	return callbackWorkflowDir(t, "build.yaml", "deploy.yaml")
}

// TestGenCorrectness_Permissions_LeastPrivilegePerJob pins the least-privilege
// contract for builds[].permissions / deploys[].permissions: a job with a
// configured permissions map emits exactly that block, scoped to that job, and
// a job with NO configured permissions emits NO permissions block (it inherits,
// rather than silently receiving an elevated default). A regression that
// emitted a default block, or leaked one job's scopes onto another, reds here.
func TestGenCorrectness_Permissions_LeastPrivilegePerJob(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	cfg.Builds[0].Permissions = map[string]string{"contents": "read", "id-token": "write"}

	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	build := pass10JobBlock(t, out, "build-app")
	assert.Contains(t, build, "permissions:", "a build with a configured permissions map must emit the block")
	assert.Contains(t, build, "contents: read", "configured scopes must be emitted verbatim")
	assert.Contains(t, build, "id-token: write")

	deploy := pass10JobBlock(t, out, "deploy-runner")
	assert.NotContains(t, deploy, "permissions:",
		"a job with no configured permissions must emit no block (least privilege, not a default grant)")
	assert.NotContains(t, deploy, "id-token: write",
		"one job's scopes must never leak onto another job")
}

// TestGenCorrectness_SecretsMap_PropagatesSourceToCallee pins the direction of
// a secrets.map entry {calleeInput: sourceSecret}: the emitted secrets: block
// maps the callee input name to ${{ secrets.<source> }}, never the reverse. A
// swapped direction would hand the callee the wrong secret under the right name,
// a valid-but-wrong emission actionlint cannot see.
func TestGenCorrectness_SecretsMap_PropagatesSourceToCallee(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	cfg.Builds[0].Secrets = &config.SecretsConfig{Map: map[string]string{"GOOD_IN": "GOOD_OUT"}}

	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	build := pass10JobBlock(t, out, "build-app")
	assert.Contains(t, build, "GOOD_IN: ${{ secrets.GOOD_OUT }}",
		"a secrets.map entry must map the callee input to the source secret expression")
	assert.NotContains(t, build, "GOOD_OUT: ${{ secrets.GOOD_IN }}",
		"the mapping direction must not be reversed")
}

// TestGenCorrectness_Concurrency_CancelInProgressHonored pins that a manifest
// cancel_in_progress: false emits cancel-in-progress: false, not a defaulted
// true. The two produce opposite runtime behavior (queued vs cancelled runs),
// so a value dropped to its zero or a hardcoded literal would silently invert
// the operator's intent.
func TestGenCorrectness_Concurrency_CancelInProgressHonored(t *testing.T) {
	dir := correctnessDir(t)

	for _, tc := range []struct {
		name string
		val  bool
		want string
	}{
		{"false", false, "cancel-in-progress: false"},
		{"true", true, "cancel-in-progress: true"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg := guardBaseConfig()
			cfg.Concurrency = &config.ConcurrencyConfig{
				Group:            "orchestrate-${{ github.ref }}",
				CancelInProgress: boolPtr(tc.val),
			}
			out, err := NewGenerator(cfg, dir).Generate()
			require.NoError(t, err)
			assert.Contains(t, out, tc.want, "cancel_in_progress must be emitted as configured, not defaulted")
		})
	}
}

// TestGenCorrectness_ActionPins_UsesCarriesPinnedRef pins that an action_pins
// override splices the pinned ref into the uses: line for that action, so a
// repo that pins actions/checkout to a SHA gets that SHA (with its trailing
// comment), not the generator's built-in default pin.
func TestGenCorrectness_ActionPins_UsesCarriesPinnedRef(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	cfg.ActionPins = map[string]string{"actions/checkout": "0123abcd # v4"}

	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	assert.Contains(t, out, "uses: actions/checkout@0123abcd # v4",
		"an action_pins override must splice the pinned ref into the uses: line")
}

// TestGenCorrectness_DispatchInputs_EmitsTypedInputBlock pins that a
// dispatch_inputs entry emits a workflow_dispatch input keyed by its name with
// the configured type, options, and default, so an operator dispatch form
// exposes the declared choices rather than a bare free-text field.
func TestGenCorrectness_DispatchInputs_EmitsTypedInputBlock(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	cfg.DispatchInputs = map[string]config.DispatchInput{
		"mode": {
			Type:    config.DispatchInputTypeChoice,
			Options: []string{"fast", "slow"},
			Default: "fast",
		},
	}

	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	assert.Contains(t, out, "mode:", "the dispatch input must be keyed by its name")
	assert.Contains(t, out, "type: choice", "the input type must be emitted")
	assert.Contains(t, out, "options:")
	assert.Contains(t, out, "- fast")
	assert.Contains(t, out, "- slow")
	assert.Contains(t, out, "default: 'fast'", "the default must be emitted, single-quote-escaped")
}

// TestGenCorrectness_ExtraTriggers_EmitsScheduleAndDispatch pins that
// extra_triggers emit the corresponding on: trigger blocks (schedule cron,
// repository_dispatch types, workflow_run workflows+types) into the orchestrate
// workflow, so a manifest that asks for a nightly cron or an upstream-run
// trigger actually gets one.
func TestGenCorrectness_ExtraTriggers_EmitsScheduleAndDispatch(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	cfg.ExtraTriggers = &config.ExtraTriggers{
		Schedule:           []config.ScheduleEntry{{Cron: "0 2 * * 1-5"}},
		RepositoryDispatch: &config.RepositoryDispatchTrigger{Types: []string{"external-update"}},
		WorkflowRun:        &config.WorkflowRunTrigger{Workflows: []string{"Upstream CI"}, Types: []string{"completed"}},
	}

	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	assert.Contains(t, out, "- cron: '0 2 * * 1-5'", "schedule cron must be emitted, hard single-quoted")
	assert.Contains(t, out, "repository_dispatch:")
	assert.Contains(t, out, "- external-update", "repository_dispatch types must be emitted")
	assert.Contains(t, out, "workflow_run:")
	assert.Contains(t, out, "- 'Upstream CI'", "workflow_run workflow names must be emitted, single-quoted")
}

// TestGenCorrectness_GitCustomUser_SplicesNameAndEmail pins that a custom git
// identity (git.mode: custom) splices the operator's name and email into the
// emitted git config commands, so state/finalize commits are attributed to the
// configured bot identity rather than the runner default.
func TestGenCorrectness_GitCustomUser_SplicesNameAndEmail(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	cfg.Git = &config.GitConfig{Mode: config.GitModeCustom, UserName: "Release Bot", UserEmail: "bot@example.com"}

	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	assert.Contains(t, out, `git config user.name "Release Bot"`,
		"a custom git identity must splice the configured user name")
	assert.Contains(t, out, `git config user.email "bot@example.com"`,
		"a custom git identity must splice the configured user email")
}

// TestGenCorrectness_EnvironmentURL_EmitsPerEnvCase pins that
// environments[].environment_url is threaded into the native-deployment status
// step as a per-environment shell case, so the GitHub Deployment records the
// configured URL for that environment. The runtime proof that the Deployment
// API receives it is fleet-only; this pins the emitted case shape.
func TestGenCorrectness_EnvironmentURL_EmitsPerEnvCase(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	cfg.Deployments = &config.DeploymentsConfig{Enabled: boolPtr(true)}
	cfg.Environments = []config.EnvironmentEntry{
		{Name: "dev"},
		{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{EnvironmentURL: "https://app.example.com"}},
	}

	out, err := NewPromoteGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	assert.Contains(t, out, "prod) environment_url='https://app.example.com'",
		"the configured environment_url must be threaded as a per-environment shell case")
}

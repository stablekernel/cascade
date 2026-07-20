package generate

import (
	"regexp"
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

	// A configured BUILD permission emits, scoped to that job; the permission-less
	// deploy emits no block (it inherits, rather than silently receiving elevated
	// defaults) and never inherits the build's scopes.
	t.Run("build_configured_deploy_omits", func(t *testing.T) {
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
	})

	// The mirror image: a configured DEPLOY permission emits exactly that scope on
	// the deploy job, and the permission-less build emits no block and never
	// inherits the deploy's scope. This positively pins the deploy-side emission
	// (the case the build-only scenario left implicit).
	t.Run("deploy_configured_build_omits", func(t *testing.T) {
		cfg := guardBaseConfig()
		cfg.Deploys[0].Permissions = map[string]string{"deployments": "write"}

		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)

		deploy := pass10JobBlock(t, out, "deploy-runner")
		assert.Contains(t, deploy, "permissions:", "a deploy with a configured permissions map must emit the block")
		assert.Contains(t, deploy, "deployments: write", "the configured deploy scope must be emitted verbatim")

		build := pass10JobBlock(t, out, "build-app")
		assert.NotContains(t, build, "permissions:",
			"a job with no configured permissions must emit no block (least privilege, not a default grant)")
		assert.NotContains(t, build, "deployments: write",
			"one job's scopes must never leak onto another job")
	})
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

// TestGenCorrectness_GPGSigning_WiresImportAndSigningKey pins the security-
// critical contract for git.gpg_key_id / git.gpg_key_secret: when both are
// configured, the emitted git setup imports the private key, turns on commit
// signing, AND sets the signing key, so state/finalize commits are actually
// GPG-signed. A regression that dropped the import, the gpgsign toggle, or the
// signingkey wiring would SILENTLY produce unsigned commits (valid YAML,
// actionlint-clean), which is exactly the silent-half defect this census hunts.
// The key id and secret are spliced as GHA secret names into ${{ secrets.<n> }}.
// This is emitted-shape correctness; the runtime proof that GitHub honors the
// signature is fleet-only (Part B).
func TestGenCorrectness_GPGSigning_WiresImportAndSigningKey(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	cfg.Git = &config.GitConfig{GPGKeyID: "GPG_KEY_ID", GPGKeySecret: "GPG_PRIVATE_KEY"}

	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	assert.Contains(t, out, `echo "${{ secrets.GPG_PRIVATE_KEY }}" | gpg --batch --import`,
		"the gpg_key_secret must be imported so the runner holds the private key")
	assert.Contains(t, out, "git config commit.gpgsign true",
		"commit signing must be turned on, or every commit ships unsigned")
	assert.Contains(t, out, `git config user.signingkey "${{ secrets.GPG_KEY_ID }}"`,
		"the gpg_key_id must be wired as the signing key")
}

// TestGenCorrectness_RunPolicy_GatesCallbackIf pins that run_policy shapes the
// emitted job if: gate per callback type. The default policy gates a build/deploy
// on change detection (needs.setup.outputs.run_<job> == 'true'); "always" wraps
// the gate in always() so the job runs even when upstream jobs are skipped. This
// pins the emitted expression SHAPE; whether always() actually rescues a skipped
// ladder at run time is a GitHub Actions runtime property proven by the fleet
// (Part B).
func TestGenCorrectness_RunPolicy_GatesCallbackIf(t *testing.T) {
	t.Run("build_default_gates_on_change", func(t *testing.T) {
		dir := correctnessDir(t)
		cfg := guardBaseConfig()
		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)
		build := pass10JobBlock(t, out, "build-app")
		assert.Contains(t, build, "needs.setup.outputs.run_build_app == 'true'",
			"a default-policy build must gate on its change-detection output")
		assert.NotContains(t, build, "always()",
			"a default-policy build must not run unconditionally")
	})

	t.Run("build_always_runs_unconditionally", func(t *testing.T) {
		dir := correctnessDir(t)
		cfg := guardBaseConfig()
		cfg.Builds[0].RunPolicy = config.RunPolicyAlways
		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)
		build := pass10JobBlock(t, out, "build-app")
		assert.Contains(t, build, "always()",
			"a run_policy: always build must wrap its gate in always()")
	})

	t.Run("deploy_always_runs_unconditionally", func(t *testing.T) {
		dir := correctnessDir(t)
		cfg := guardBaseConfig()
		cfg.Deploys[0].RunPolicy = config.RunPolicyAlways
		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)
		deploy := pass10JobBlock(t, out, "deploy-runner")
		assert.Contains(t, deploy, "always()",
			"a run_policy: always deploy must wrap its gate in always()")
	})

	t.Run("validate_default_no_gate_always_gates", func(t *testing.T) {
		dir := callbackWorkflowDir(t, "build.yaml", "deploy.yaml", "validate.yaml")

		base := guardBaseConfig()
		base.Validate = &config.ValidateConfig{Workflow: "validate.yaml"}
		out, err := NewGenerator(base, dir).Generate()
		require.NoError(t, err)
		validate := pass10JobBlock(t, out, "validate")
		assert.NotContains(t, validate, "if:",
			"a default-policy validate runs on every orchestrate and emits no if: gate")

		always := guardBaseConfig()
		always.Validate = &config.ValidateConfig{Workflow: "validate.yaml", RunPolicy: config.RunPolicyAlways}
		out, err = NewGenerator(always, dir).Generate()
		require.NoError(t, err)
		validate = pass10JobBlock(t, out, "validate")
		assert.Contains(t, validate, "if: always()",
			"a run_policy: always validate must gate on always()")
	})
}

// TestGenCorrectness_DependsOn_SequencesButOnlyRequiredGates pins the
// sequencing-versus-gating split for depends_on / optional_depends_on. A REQUIRED
// depends_on emits the dependency both into needs: (sequencing) AND into the job
// if: gate (so a failed dependency skips the dependent). An OPTIONAL
// optional_depends_on emits the dependency into needs: only: it orders the job
// after the dependency without gating on its result. A regression that gated an
// optional edge would wrongly skip a job whose optional upstream failed; one that
// dropped the required gate would run a dependent on a broken upstream. The
// needs: ordering and the if: gate are emitted-shape; the runtime skip behavior
// is a GitHub Actions property proven by the fleet (Part B).
func TestGenCorrectness_DependsOn_SequencesButOnlyRequiredGates(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	cfg.Builds = []config.BuildConfig{
		{Name: "lib", Workflow: "build.yaml", Triggers: []string{"lib/**"}},
		{Name: "app", Workflow: "build.yaml", Triggers: []string{"app/**"}, DependsOn: []string{"lib"}},
		{Name: "tool", Workflow: "build.yaml", Triggers: []string{"tool/**"}, OptionalDependsOn: []string{"lib"}},
	}
	// A required deploy dependency is pinned by TestGM5; here the deploy carries an
	// OPTIONAL dependency to pin the deploy-side sequencing-not-gating contract.
	cfg.Deploys = []config.DeployConfig{
		{Name: "runner", Workflow: "deploy.yaml", Triggers: []string{"deploy/**"}, OptionalDependsOn: []string{"lib"}},
	}

	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	app := pass10JobBlock(t, out, "build-app")
	assert.Contains(t, app, "needs: [setup, build-lib]",
		"a required depends_on must sequence the dependent after its dependency")
	assert.Contains(t, app, "needs.build-lib.result == 'success'",
		"a required depends_on must also gate the dependent on the dependency succeeding")

	tool := pass10JobBlock(t, out, "build-tool")
	assert.Contains(t, tool, "build-lib",
		"an optional_depends_on must still sequence the job after its dependency (in needs:)")
	assert.NotContains(t, tool, "needs.build-lib.result",
		"an optional_depends_on must NOT gate the job on the dependency's result")

	deploy := pass10JobBlock(t, out, "deploy-runner")
	assert.Contains(t, deploy, "build-lib",
		"a deploy optional_depends_on must sequence the deploy after its dependency (in needs:)")
	assert.NotContains(t, deploy, "needs.build-lib.result",
		"a deploy optional_depends_on must NOT gate the deploy on the dependency's result")
}

// TestGenCorrectness_EnvironmentRole_MovesReleaseStage pins that
// environments[].role: release selects which environment is the release (final)
// stage, overriding the positional default (last entry). The prod deploy job's
// name and environment: input are threaded from ReleaseEnvironment(), so a
// role on a non-last environment moves the release stage there. A regression that
// ignored role would publish the wrong environment as the release.
func TestGenCorrectness_EnvironmentRole_MovesReleaseStage(t *testing.T) {
	dir := correctnessDir(t)
	cfg := guardBaseConfig()
	// role: release on staging (NOT the positional-last prod) moves the release
	// stage off the default last entry.
	cfg.Environments = []config.EnvironmentEntry{
		{Name: "dev"},
		{Name: "staging", Role: config.EnvRoleRelease},
		{Name: "prod"},
	}

	out, err := NewPromoteGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	assert.Contains(t, out, "name: Deploy runner (staging)",
		"role: release must move the release stage to the declared environment")
	assert.Contains(t, out, "environment: staging",
		"the prod deploy job's environment input must be the role-declared release env")
	assert.NotContains(t, out, "name: Deploy runner (prod)",
		"the positional-last env must not be the release stage once a role overrides it")
}

// TestGenCorrectness_PinMode_ShaEmitsShaRefs pins that pin_mode selects whether a
// built-in action ref is emitted as a commit SHA (with a version comment) or a
// tag. pin_mode: sha hardens supply-chain posture by pinning to an immutable
// commit; the default (tag) keeps the readable tag. A regression that ignored
// pin_mode would silently downgrade a repo that asked for SHA pinning back to a
// mutable tag.
func TestGenCorrectness_PinMode_ShaEmitsShaRefs(t *testing.T) {
	dir := correctnessDir(t)
	shaRef := regexp.MustCompile(`uses: actions/checkout@[0-9a-f]{40} #`)
	tagRef := regexp.MustCompile(`uses: actions/checkout@v[0-9]`)

	t.Run("sha", func(t *testing.T) {
		cfg := guardBaseConfig()
		cfg.PinMode = config.PinModeSHA
		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)
		assert.Regexp(t, shaRef, out, "pin_mode: sha must emit a 40-hex commit SHA ref for a built-in action")
		assert.NotRegexp(t, tagRef, out, "pin_mode: sha must not leave a built-in action on a mutable tag")
	})

	t.Run("tag_default", func(t *testing.T) {
		cfg := guardBaseConfig()
		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)
		assert.Regexp(t, tagRef, out, "the default pin_mode (tag) must emit the readable tag ref")
		assert.NotRegexp(t, shaRef, out, "the default pin_mode must not emit a SHA ref")
	})
}

// TestGenCorrectness_ReleaseTrigger_DispatchDropsPush pins that release_trigger
// selects how orchestrate fires: the default (push) keeps the trunk-push trigger
// so every merge cuts a candidate; dispatch drops the push: trigger so orchestrate
// runs only on workflow_dispatch, handing a maintainer the release gate. A
// regression that ignored dispatch would re-arm push and cut a candidate on every
// merge against the operator's intent.
func TestGenCorrectness_ReleaseTrigger_DispatchDropsPush(t *testing.T) {
	dir := correctnessDir(t)

	t.Run("push_default", func(t *testing.T) {
		cfg := guardBaseConfig()
		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)
		assert.Contains(t, out, "  push:\n    branches: [main]",
			"the default release_trigger (push) must keep the trunk-push trigger")
	})

	t.Run("dispatch_drops_push", func(t *testing.T) {
		cfg := guardBaseConfig()
		cfg.ReleaseTrigger = config.ReleaseTriggerDispatch
		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)
		assert.NotContains(t, out, "  push:\n",
			"release_trigger: dispatch must drop the push: trigger so orchestrate runs only on dispatch")
		assert.Contains(t, out, "workflow_dispatch:",
			"dispatch mode must still expose the workflow_dispatch entry point")
	})
}

// TestGenCorrectness_ReconcileCommit_AppendVsFollowup pins that reconcile.commit
// routes the same-repo adoption commit: the default (append) pushes onto the
// triggering PR's own head branch (with a fork sticky-comment fallback), while
// followup pushes a separate branch and opens a followup PR (the automerge-
// without-review posture). A regression that swapped the routing would push a
// commit onto the wrong branch, or open a PR when the operator wanted an in-place
// push. This pins the emitted step SHAPE.
func TestGenCorrectness_ReconcileCommit_AppendVsFollowup(t *testing.T) {
	base := func() *config.TrunkConfig {
		return &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: config.EnvNames("dev"),
			Reconcile:    &config.ReconcileConfig{Enabled: true},
		}
	}

	t.Run("append_default", func(t *testing.T) {
		cfg := base()
		out, err := NewReconcileGenerator(cfg, "").GenerateCompanion()
		require.NoError(t, err)
		assert.Contains(t, out, "name: Push the reconcile commit",
			"the default (append) mode must push onto the triggering PR's head branch")
		assert.Contains(t, out, "name: Fork fallback (sticky comment)",
			"append mode must carry the mandatory fork sticky-comment fallback")
		assert.NotContains(t, out, "name: Push the followup branch",
			"append mode must not emit the followup-branch step")
	})

	t.Run("followup", func(t *testing.T) {
		cfg := base()
		cfg.Reconcile.Commit = config.ReconcileCommitFollowup
		out, err := NewReconcileGenerator(cfg, "").GenerateCompanion()
		require.NoError(t, err)
		assert.Contains(t, out, "name: Push the followup branch",
			"followup mode must push a separate branch")
		assert.Contains(t, out, "name: Open or update the followup PR",
			"followup mode must open a followup PR for automerge-without-review")
		assert.NotContains(t, out, "name: Push the reconcile commit",
			"followup mode must not push in place onto the PR head branch")
	})
}

// TestGenCorrectness_RetryShim_IfGateCarriesStatusFunction pins the fix for a
// real-GitHub defect the fleet caught: a retry shim (and a dependent of a
// retried callback) whose if: gate is a BARE boolean expression is skipped by
// GitHub's implicit needs-success gate exactly when it is needed. GitHub only
// lifts that implicit gate when the if: contains a status-check function
// (always(), !cancelled(), success(), failure(), cancelled()); a bare
// "needs.X.result == 'failure'" does not qualify, so when X fails the shim is
// skipped and the rescue never runs.
//
// A retry re-invokes a deployment, so a cancelled run must NOT trigger it:
// !cancelled() (not always()) is the correct function. The "== 'failure'"
// clause already excludes a cancelled predecessor (its result is 'cancelled',
// not 'failure'), so !cancelled() only adds the honor-the-cancel behavior for a
// run cancelled mid-flight, which is exactly what a re-deploy shim wants.
//
// The `retries` field is an int, so it is outside the string-field correctness
// census surface (emitted_fields_guard_test.go walks string-carrying fields
// only); that is precisely why this shim-if defect escaped the census. This
// assertion, plus the dependent coverage folded into GM5/GM7 (the censused
// deploys[].depends_on[] assertions), is the regression guard. The runtime
// rescue itself is the fleet's proof; this pins the emitted shape.
func TestGenCorrectness_RetryShim_IfGateCarriesStatusFunction(t *testing.T) {
	dir := correctnessDir(t)

	t.Run("retry_shims_and_dependent_lift_the_needs_gate", func(t *testing.T) {
		cfg := &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: config.EnvNames("dev"),
			Deploys: []config.DeployConfig{
				{Name: "web", Workflow: "deploy.yaml", Triggers: []string{"src/**"}, Retries: 2},
				{Name: "api", Workflow: "deploy.yaml", Triggers: []string{"src/**"}, DependsOn: []string{"web"}},
			},
		}
		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)

		retry1 := pass10JobBlock(t, out, "deploy-web-retry-1")
		assert.Contains(t, retry1, "if: ${{ !cancelled() && needs.deploy-web.result == 'failure' }}",
			"retry-1's if: must carry a status function so GitHub does not skip it via the implicit needs-success gate when the base fails")
		assert.NotContains(t, retry1, "if: needs.deploy-web.result == 'failure'",
			"a bare boolean gate is skipped on real GitHub exactly when the base failed")

		retry2 := pass10JobBlock(t, out, "deploy-web-retry-2")
		assert.Contains(t, retry2, "if: ${{ !cancelled() && needs.deploy-web-retry-1.result == 'failure' }}",
			"every rung of the ladder, including retry-N chained off retry-(N-1), must lift the implicit gate")
		assert.NotContains(t, retry2, "if: needs.deploy-web-retry-1.result == 'failure'",
			"retry-2's bare boolean gate would be skipped when retry-1 failed")

		dependent := pass10JobBlock(t, out, "deploy-api")
		assert.Contains(t, dependent, "!cancelled()",
			"a dependent of a retried deploy must lift the implicit needs-success gate so its effective-result OR is evaluated after the base failed but a shim rescued it")
		assert.Contains(t, dependent, "(needs.deploy-web.result == 'success' || needs.deploy-web-retry-1.result == 'success' || needs.deploy-web-retry-2.result == 'success')",
			"the dependent must still judge the dependency's effective result across the whole ladder")
	})

	t.Run("dependent_of_non_retried_deploy_is_unchanged", func(t *testing.T) {
		cfg := &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: config.EnvNames("dev"),
			Deploys: []config.DeployConfig{
				{Name: "web", Workflow: "deploy.yaml", Triggers: []string{"src/**"}},
				{Name: "api", Workflow: "deploy.yaml", Triggers: []string{"src/**"}, DependsOn: []string{"web"}},
			},
		}
		out, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err)

		dependent := pass10JobBlock(t, out, "deploy-api")
		assert.NotContains(t, dependent, "!cancelled()",
			"a dependent of a NON-retried deploy has no immutable-result hazard, so it must not gain a status function (no churn)")
		assert.Contains(t, dependent, "needs.deploy-web.result == 'success'",
			"the no-retry dependent still gates on the bare dependency success")
	})
}

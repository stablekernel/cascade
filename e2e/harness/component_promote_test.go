package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPromoteWorkflowPath verifies the promote workflow path selection: an empty
// component keeps the repo-wide promote.yaml (byte-identical single-component
// behavior), while a named component targets its fanned-out promote-<name>.yaml.
func TestPromoteWorkflowPath(t *testing.T) {
	cases := []struct {
		name      string
		component string
		want      string
	}{
		{name: "single component keeps repo-wide file", component: "", want: ".github/workflows/promote.yaml"},
		{name: "named component selects fanned-out file", component: "api", want: ".github/workflows/promote-api.yaml"},
		{name: "second component selects its own file", component: "web", want: ".github/workflows/promote-web.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, promoteWorkflowPath(tc.component))
		})
	}
}

// TestOrchestrateWorkflowPath verifies the orchestrate workflow path selection
// mirrors promote: empty keeps orchestrate.yaml, a component selects its
// orchestrate-<name>.yaml.
func TestOrchestrateWorkflowPath(t *testing.T) {
	cases := []struct {
		name      string
		component string
		want      string
	}{
		{name: "single component keeps repo-wide file", component: "", want: ".github/workflows/orchestrate.yaml"},
		{name: "named component selects fanned-out file", component: "api", want: ".github/workflows/orchestrate-api.yaml"},
		{name: "second component selects its own file", component: "web", want: ".github/workflows/orchestrate-web.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, orchestrateWorkflowPath(tc.component))
		})
	}
}

// TestComponentStateKey verifies the composite key is distinct per (component, env)
// and never collides with a flat env key.
func TestComponentStateKey(t *testing.T) {
	assert.Equal(t, "components/api/dev", componentStateKey("api", "dev"))
	assert.Equal(t, "components/api/prod", componentStateKey("api", "prod"))
	assert.Equal(t, "components/web/prod", componentStateKey("web", "prod"))
	// Distinct components at the same env must not collide.
	assert.NotEqual(t, componentStateKey("api", "prod"), componentStateKey("web", "prod"))
	// The composite key is never a bare env name, so a component-scoped record can
	// never overwrite a flat state.<env> record.
	assert.NotEqual(t, "prod", componentStateKey("api", "prod"))
}

// TestParseComponentStates parses a manifest carrying both flat env rows and a
// per-component subtree, asserting only the component rows are returned and the
// flat rows are ignored (they are handled by the flat parse).
func TestParseComponentStates(t *testing.T) {
	manifest := `ci:
  config:
    trunk_branch: main
  state:
    dev:
      sha: flatdevsha
      version: v0.1.0-rc.0
    components:
      api:
        dev:
          sha: apidevsha
          version: api-v0.1.0-rc.0
        prod:
          sha: apiprodsha
          version: api-v0.1.0
          deploys:
            app:
              sha: apideploysha
      web:
        dev:
          sha: webdevsha
          version: web-v0.1.0-rc.0
`
	components, err := parseComponentStates(manifest, "ci")
	require.NoError(t, err)
	require.Len(t, components, 2)

	assert.Equal(t, "apidevsha", components["api"]["dev"].SHA)
	assert.Equal(t, "api-v0.1.0-rc.0", components["api"]["dev"].Version)
	assert.Equal(t, "apiprodsha", components["api"]["prod"].SHA)
	assert.Equal(t, "api-v0.1.0", components["api"]["prod"].Version)
	assert.Equal(t, "apideploysha", components["api"]["prod"].Deploys["app"].SHA)
	assert.Equal(t, "webdevsha", components["web"]["dev"].SHA)

	// The flat "dev" row is not surfaced as a component.
	_, ok := components["dev"]
	assert.False(t, ok, "flat env row must not be parsed as a component")
}

// TestParseComponentStates_NoComponents confirms a flat manifest yields an empty
// component map and no error, so a single-component scenario's readback is a no-op.
func TestParseComponentStates_NoComponents(t *testing.T) {
	manifest := `ci:
  state:
    dev:
      sha: devsha
      version: v0.1.0-rc.0
    prod:
      sha: prodsha
      version: v0.1.0
`
	components, err := parseComponentStates(manifest, "ci")
	require.NoError(t, err)
	assert.Empty(t, components)
}

// TestParseComponentStates_MissingKey confirms a manifest without the requested
// top-level key yields an empty map rather than an error.
func TestParseComponentStates_MissingKey(t *testing.T) {
	components, err := parseComponentStates("other:\n  state: {}\n", "ci")
	require.NoError(t, err)
	assert.Empty(t, components)
}

// TestRunner_AssertStep_ComponentState_Isolation drives the component-scoped
// assertion branch: with only component A's prod subtree recorded, an expectation
// that A.prod advanced AND B.prod is absent (wiped) must pass, proving the
// component-scoped lookup targets the composite key rather than the flat env.
func TestRunner_AssertStep_ComponentState_Isolation(t *testing.T) {
	runner := &Runner{ctx: NewExecutionContext(), t: t}
	runner.ctx.RecordCommit("commit1", "abc123")
	// Only component A advanced to prod; component B has no prod row.
	runner.ctx.RecordState(componentStateKey("api", "prod"), "abc123", "api-v0.1.0")

	step := &Step{
		Name:   "assert per-component isolation",
		Action: "promote",
		Expect: &StepExpect{
			State: map[string]*StateExpect{
				// A advanced at its own prod subtree.
				"api-prod": {Component: "api", Env: "prod", SHA: "commit1", Version: "api-v0.1.0"},
				// B's prod subtree is untouched (absent). A distinct map key plus an
				// explicit env lets both components be asserted at the same env in one
				// step, which an env-keyed map alone cannot express.
				"web-prod": {Component: "web", Env: "prod", Wiped: true},
			},
		},
	}

	ctx := context.Background()
	preState := runner.ctx.Clone()
	errs := runner.assertStep(ctx, step, preState)
	assert.Empty(t, errs)

	// Recording B's prod subtree must now break the "wiped" expectation, proving
	// the sibling assertion is really scoped to component B and not component A.
	runner.ctx.RecordState(componentStateKey("web", "prod"), "def456", "web-v0.1.0")
	errs = runner.assertStep(ctx, step, preState)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "components/web/prod")
}

// TestRunner_AssertStep_ComponentState_Unchanged verifies the "unchanged"
// expectation resolves the component composite key: component B's dev subtree must
// read as unchanged across a step that only advanced component A.
func TestRunner_AssertStep_ComponentState_Unchanged(t *testing.T) {
	runner := &Runner{ctx: NewExecutionContext(), t: t}
	runner.ctx.RecordState(componentStateKey("web", "dev"), "webdev", "web-v0.1.0-rc.0")

	preState := runner.ctx.Clone()

	step := &Step{
		Name:   "assert sibling unchanged",
		Action: "promote",
		Expect: &StepExpect{
			State: map[string]*StateExpect{
				"dev": {Component: "web", Unchanged: true},
			},
		},
	}

	ctx := context.Background()
	errs := runner.assertStep(ctx, step, preState)
	assert.Empty(t, errs)

	// Mutating component B's dev subtree must now trip the unchanged assertion.
	runner.ctx.RecordState(componentStateKey("web", "dev"), "webdev2", "web-v0.1.0-rc.1")
	errs = runner.assertStep(ctx, step, preState)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "components/web/dev")
}

// TestHotfixWorkflowPath verifies the hotfix workflow path selection mirrors
// promote: an empty component keeps the repo-wide cascade-hotfix.yaml
// (byte-identical single-component behavior), a named component selects its
// fanned-out cascade-hotfix-<name>.yaml.
func TestHotfixWorkflowPath(t *testing.T) {
	cases := []struct {
		name      string
		component string
		want      string
	}{
		{name: "single component keeps repo-wide file", component: "", want: ".github/workflows/cascade-hotfix.yaml"},
		{name: "named component selects fanned-out file", component: "api", want: ".github/workflows/cascade-hotfix-api.yaml"},
		{name: "second component selects its own file", component: "web", want: ".github/workflows/cascade-hotfix-web.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, hotfixWorkflowPath(tc.component))
		})
	}
}

// TestRollbackWorkflowPath verifies the rollback workflow path selection mirrors
// hotfix: empty keeps cascade-rollback.yaml, a component selects its
// cascade-rollback-<name>.yaml.
func TestRollbackWorkflowPath(t *testing.T) {
	cases := []struct {
		name      string
		component string
		want      string
	}{
		{name: "single component keeps repo-wide file", component: "", want: ".github/workflows/cascade-rollback.yaml"},
		{name: "named component selects fanned-out file", component: "api", want: ".github/workflows/cascade-rollback-api.yaml"},
		{name: "second component selects its own file", component: "web", want: ".github/workflows/cascade-rollback-web.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rollbackWorkflowPath(tc.component))
		})
	}
}

// TestEnvBranchName verifies the integration-branch namespace: single-component
// stays flat env/<env> (byte-identical), a component nests env/<component>/<env>
// so two components' branches never collide.
func TestEnvBranchName(t *testing.T) {
	assert.Equal(t, "env/prod", envBranchName("", "prod"))
	assert.Equal(t, "env/api/prod", envBranchName("api", "prod"))
	assert.Equal(t, "env/web/prod", envBranchName("web", "prod"))
	assert.NotEqual(t, envBranchName("api", "prod"), envBranchName("web", "prod"))
}

// TestHotfixBranchName verifies the throwaway cherry-pick branch namespace:
// single-component stays hotfix/<env>/<short> (byte-identical), a component nests
// hotfix/<component>/<env>/<short> so two components hotfixing the same env at the
// same source commit push disjoint branches and never collide.
func TestHotfixBranchName(t *testing.T) {
	assert.Equal(t, "hotfix/prod/abc12345", hotfixBranchName("", "prod", "abc12345"))
	assert.Equal(t, "hotfix/api/prod/abc12345", hotfixBranchName("api", "prod", "abc12345"))
	// The exact collision the generator fix closes: same env, same source short SHA,
	// two components -> two distinct branches.
	assert.NotEqual(t,
		hotfixBranchName("api", "prod", "abc12345"),
		hotfixBranchName("web", "prod", "abc12345"))
}

// TestParseComponentStates_Divergence confirms the component subtree parse reads
// the hotfix/rollback divergence fields (ref/base_sha/patches) so a per-component
// lifecycle scenario can assert a hotfixed component tracks its own env branch.
func TestParseComponentStates_Divergence(t *testing.T) {
	manifest := `ci:
  state:
    components:
      api:
        prod:
          sha: apiprodsha
          version: api-v0.1.0
          ref: env/api/prod
          base_sha: apibasesha
          patches:
            - apipatchsha
      web:
        prod:
          sha: webprodsha
          version: web-v0.1.0
`
	components, err := parseComponentStates(manifest, "ci")
	require.NoError(t, err)
	assert.Equal(t, "env/api/prod", components["api"]["prod"].Ref)
	assert.Equal(t, "apibasesha", components["api"]["prod"].BaseSHA)
	assert.Equal(t, []string{"apipatchsha"}, components["api"]["prod"].Patches)
	// The undiverged sibling carries no divergence fields.
	assert.Empty(t, components["web"]["prod"].Ref)
	assert.Empty(t, components["web"]["prod"].Patches)
}

// TestParseComponentStates_PreviousRing parses a component leaf carrying a
// deploy-history ring and asserts the ring versions are read back newest first,
// so previous_contains assertions can observe them.
func TestParseComponentStates_PreviousRing(t *testing.T) {
	manifest := `ci:
  state:
    components:
      api:
        prod:
          sha: apiprodsha2
          version: api-0.2.0
          previous:
            - sha: apiprodsha1
              version: api-0.1.0
`
	states, err := parseComponentStates(manifest, "ci")
	require.NoError(t, err)
	prod := states["api"]["prod"]
	require.Len(t, prod.Previous, 1)
	assert.Equal(t, "api-0.1.0", prod.Previous[0].Version)
}

// TestAssertState_PreviousContains verifies the previous_contains expectation:
// it passes when every listed version is in the recorded ring and fails with a
// named version when the ring is missing one (the empty-ring case is exactly
// what a finalize that rebuilds the env leaf from empty would produce).
func TestAssertState_PreviousContains(t *testing.T) {
	ctx := NewExecutionContext()
	key := componentStateKey("api", "prod")
	ctx.RecordState(key, "apiprodsha2", "api-0.2.0")
	ctx.RecordStatePreviousVersions(key, []string{"api-0.1.0"})

	errs := AssertState(ctx, key, &StateExpect{PreviousContains: []string{"api-0.1.0"}})
	assert.Empty(t, errs, "a ring carrying the listed version must pass")

	errs = AssertState(ctx, key, &StateExpect{PreviousContains: []string{"api-0.0.1"}})
	require.Len(t, errs, 1, "a ring missing the listed version must fail")
	assert.Contains(t, errs[0].Error(), "api-0.0.1")

	// An empty ring (the rebuilt-from-empty leaf) fails the expectation.
	bare := componentStateKey("web", "prod")
	ctx.RecordState(bare, "webprodsha", "web-0.1.0")
	errs = AssertState(ctx, bare, &StateExpect{PreviousContains: []string{"web-0.0.1"}})
	require.Len(t, errs, 1, "an empty ring must fail previous_contains")
}

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

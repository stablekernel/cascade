package simulate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/promote"
)

// simMonorepoManifest is a two-component manifest whose recorded state lives
// under state.components.<comp>.<env>, the layout a monorepo repo uses. Component
// api and component web each populate dev with a distinct sha so a component-scoped
// simulation must surface api's rows, never web's. staging is empty for both, so a
// default promotion advances dev into staging.
const simMonorepoManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - staging
      - prod
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-v
      web:
        path: services/web
        tag_grammar:
          prefix: web-v
  state:
    components:
      api:
        dev:
          sha: apidevsha0001
          version: api-v1.2.0-rc.1
          committed_at: "2026-01-01T10:00:00Z"
          committed_by: seed
        staging: {}
      web:
        dev:
          sha: webdevsha0001
          version: web-v3.0.0-rc.1
          committed_at: "2026-01-01T10:00:00Z"
          committed_by: seed
        staging: {}
`

// writeRaw writes verbatim manifest bytes to a temp file and returns its path.
func writeRaw(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// TestEngine_Simulate_Promote_ComponentScopedState is the monorepo regression:
// a promotion simulated with WithComponent("api") must read api's recorded rows
// from state.components.api.<env> and produce a real dev->staging diff carrying
// api's sha and version, never web's and never an empty state. Against the flat
// parseState this fails, because the top-level state.<env> map is empty for a
// component-scoped manifest.
func TestEngine_Simulate_Promote_ComponentScopedState(t *testing.T) {
	t.Parallel()

	path := writeRaw(t, simMonorepoManifest)

	engine, err := NewEngine(path, WithActor("tester"), WithComponent("api"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	require.True(t, result.Diff.Changed(), "staging should gain api's dev sha/version")

	staging, ok := result.Diff.Env("staging")
	require.True(t, ok, "staging must appear in the diff")
	assert.True(t, staging.SHA.Changed)
	assert.Equal(t, "apidevsha0001", staging.SHA.To, "must carry api's dev sha, not web's")
	assert.True(t, staging.Version.Changed)
	assert.Equal(t, "api-v1.2.0-rc.1", staging.Version.To)

	require.GreaterOrEqual(t, len(result.Effects), 2)
	assert.Equal(t, "deploy", result.Effects[0].Action)
	assert.Equal(t, "staging", result.Effects[0].Target)
	assert.Equal(t, "write state", result.Effects[1].Action)
	assert.Equal(t, "staging", result.Effects[1].Target)
}

// TestEngine_Simulate_Component_IsolatesSiblingState proves the selection is
// scoped: simulating component web against the same manifest surfaces web's sha,
// confirming the engine reads the named component's subtree rather than a merged
// or first-wins view.
func TestEngine_Simulate_Component_IsolatesSiblingState(t *testing.T) {
	t.Parallel()

	path := writeRaw(t, simMonorepoManifest)

	engine, err := NewEngine(path, WithActor("tester"), WithComponent("web"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	staging, ok := result.Diff.Env("staging")
	require.True(t, ok)
	assert.Equal(t, "webdevsha0001", staging.SHA.To, "web selection must carry web's dev sha")
}

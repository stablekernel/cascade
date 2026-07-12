package orchestrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// componentSeedManifest is a two-component manifest whose recorded state lives
// entirely under state.components.<name>.<env>, the component-scoped form. It
// carries a sibling component ("web") the writes below never address, so every
// test asserts it survives verbatim.
const componentSeedManifest = `ci:
  config:
    environments: [dev, prod]
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-
      web:
        path: services/web
        tag_grammar:
          prefix: web-
  state:
    components:
      web:
        dev:
          sha: websha
          version: web-0.1.0
          committed_by: someone
`

// readManifestTree parses raw manifest bytes into a generic tree so tests can
// assert on the exact serialized shape, including keys the typed model ignores.
func readManifestTree(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

// componentEnvRow digs out ci.state.components.<comp>.<env> from a parsed
// manifest, failing the test when any level is missing.
func componentEnvRow(t *testing.T, m map[string]any, comp, env string) map[string]any {
	t.Helper()
	ci, ok := m["ci"].(map[string]any)
	require.True(t, ok, "ci block present")
	state, ok := ci["state"].(map[string]any)
	require.True(t, ok, "ci.state present")
	comps, ok := state["components"].(map[string]any)
	require.True(t, ok, "ci.state.components present")
	c, ok := comps[comp].(map[string]any)
	require.True(t, ok, "component %s present", comp)
	leaf, ok := c[env].(map[string]any)
	require.True(t, ok, "component %s env %s present", comp, env)
	return leaf
}

// TestWriteConfig_Component_WritesScopedNodeAndKeepsSibling proves the orchestrate
// state-write path records the seeded env under state.components.<name>.<env> and
// leaves an unaddressed sibling component byte-intact. A regression to a flat
// WriteManifestState (whole-state-node rebuild) would drop state.components.web
// and write a flat state.dev instead.
func TestWriteConfig_Component_WritesScopedNodeAndKeepsSibling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(componentSeedManifest), 0o644))

	o, err := NewOrchestrator(path, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	// Model the in-memory state Finalize would carry for its component's env.
	o.cicdFile.State["dev"] = &config.EnvState{
		SHA:         "apisha",
		Version:     "api-0.1.0",
		CommittedBy: "orchestrator",
	}
	require.NoError(t, o.writeConfig())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	m := readManifestTree(t, out)

	// api's dev seed landed under its own subtree.
	got := componentEnvRow(t, m, "api", "dev")
	require.Equal(t, "apisha", got["sha"])
	require.Equal(t, "api-0.1.0", got["version"])
	require.Equal(t, "orchestrator", got["committed_by"])

	// The sibling web component is preserved verbatim.
	sib := componentEnvRow(t, m, "web", "dev")
	require.Equal(t, "websha", sib["sha"])
	require.Equal(t, "web-0.1.0", sib["version"])
	require.Equal(t, "someone", sib["committed_by"])

	// No flat state.<env> leaked alongside the component form.
	ci := m["ci"].(map[string]any)
	state := ci["state"].(map[string]any)
	_, hasFlatDev := state["dev"]
	require.False(t, hasFlatDev, "component orchestrate must not write a flat state.dev node")
}

// TestWriteConfig_SingleComponent_ByteIdentical proves an empty component takes
// the exact original WriteManifestState path, so a single-component manifest
// round-trips byte-for-byte identical to the historical single-component behavior.
func TestWriteConfig_SingleComponent_ByteIdentical(t *testing.T) {
	const flat = `ci:
  config:
    environments: [dev, prod]
  state:
    dev:
      sha: devsha
      version: v1.0.0
    prod: {}
`
	dir := t.TempDir()

	// Path A: the orchestrator writeConfig with no component set.
	pathA := filepath.Join(dir, "a.yaml")
	require.NoError(t, os.WriteFile(pathA, []byte(flat), 0o644))
	oA, err := NewOrchestrator(pathA, "ci", "dev")
	require.NoError(t, err)
	oA.cicdFile.State["dev"].SHA = "newsha"
	oA.cicdFile.State["dev"].Version = "v1.1.0"
	require.NoError(t, oA.writeConfig())
	gotA, err := os.ReadFile(pathA)
	require.NoError(t, err)

	// Path B: the direct WriteManifestState reference, mirroring the exact
	// in-memory mutation, serialized the historical way. Both must be identical.
	pathB := filepath.Join(dir, "b.yaml")
	require.NoError(t, os.WriteFile(pathB, []byte(flat), 0o644))
	oB, err := NewOrchestrator(pathB, "ci", "dev")
	require.NoError(t, err)
	oB.cicdFile.State["dev"].SHA = "newsha"
	oB.cicdFile.State["dev"].Version = "v1.1.0"
	ref, err := config.WriteManifestState([]byte(flat), config.DefaultManifestKey, oB.cicdFile.State, oB.cicdFile.LatestRelease)
	require.NoError(t, err)

	require.Equal(t, string(ref), string(gotA), "single-component orchestrate must be byte-identical to WriteManifestState")
}

// TestWriteConfig_Component_ReapplyPreservesUnmodeledSibling models a concurrent
// sibling orchestrate landing between this component's read and its write (the
// git rebase-retry re-applies the state edit against the winner's committed
// bytes). writeConfig re-derives only this component's leaf from its in-memory
// state and re-reads the on-disk file, so a sibling subtree that gained an
// unmodeled key on the winner's commit survives the re-apply verbatim. A
// regression to a whole-state-node rebuild would delete the sibling on re-apply.
func TestWriteConfig_Component_ReapplyPreservesUnmodeledSibling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(componentSeedManifest), 0o644))

	o, err := NewOrchestrator(path, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)
	o.cicdFile.State["dev"] = &config.EnvState{SHA: "apisha", Version: "api-0.1.0", CommittedBy: "orchestrator"}
	require.NoError(t, o.writeConfig())

	// A concurrent web orchestrate wins the race: rewrite the on-disk file to
	// carry its freshly committed subtree, including a key this binary does not
	// model, exactly as a rebase would leave the tree before the re-apply.
	winner := `ci:
  config:
    environments: [dev, prod]
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-
      web:
        path: services/web
        tag_grammar:
          prefix: web-
  state:
    components:
      api:
        dev:
          sha: apisha
          version: api-0.1.0
          committed_by: orchestrator
      web:
        dev:
          sha: webnewsha
          version: web-0.2.0
          committed_by: web-bot
          unmodeled_key: keep-me
`
	require.NoError(t, os.WriteFile(path, []byte(winner), 0o644))

	// Re-apply this component's edit on top of the winner's tree.
	require.NoError(t, o.writeConfig())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	m := readManifestTree(t, out)

	// api's own leaf is intact.
	api := componentEnvRow(t, m, "api", "dev")
	require.Equal(t, "apisha", api["sha"])
	require.Equal(t, "api-0.1.0", api["version"])

	// The winner's web subtree survived verbatim, including the unmodeled key.
	web := componentEnvRow(t, m, "web", "dev")
	require.Equal(t, "webnewsha", web["sha"])
	require.Equal(t, "web-0.2.0", web["version"])
	require.Equal(t, "web-bot", web["committed_by"])
	require.Equal(t, "keep-me", web["unmodeled_key"], "unmodeled sibling key must survive the re-apply")
}

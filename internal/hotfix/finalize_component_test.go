package hotfix

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/statewrite"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// hotfixComponentManifest is a two-component manifest whose recorded state lives
// entirely under state.components.<name>.<env>. It carries a sibling component
// ("web") the hotfix under test never addresses, so every assertion proves that
// sibling survives verbatim.
const hotfixComponentManifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    components:
      api:
        path: api
        tag_prefix: api-
      web:
        path: web
        tag_prefix: web-
  state:
    components:
      api:
        prod:
          sha: apiprodsha
          version: api-1.4.0-rc.2
      web:
        prod:
          sha: webprodsha
          version: web-1.4.0-rc.2
          committed_by: web-bot
          unmodeled_key: keep-me
`

// readManifestNode parses raw manifest bytes into a generic tree so tests can
// assert on the exact serialized shape, including keys the typed model ignores.
func readManifestNode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

// componentEnvNode digs out ci.state.components.<comp>.<env> from a parsed
// manifest, failing the test when any level is missing.
func componentEnvNode(t *testing.T, m map[string]any, comp, env string) map[string]any {
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

// conflictOnceClient models a concurrent sibling finalize through the real
// optimistic-lock retry loop: it serves trunk bytes, and on the first PutContent
// it (a) rewrites trunk to carry a racing finalizer's freshly committed sibling
// subtree, exactly as the winner would have, and (b) returns a 409 so
// CommitWithRetry re-fetches and re-applies. The second PutContent succeeds.
type conflictOnceClient struct {
	trunk      []byte
	sha        string
	puts       int
	injectYAML string
}

func (c *conflictOnceClient) GetContent(_, _, _ string) ([]byte, string, error) {
	return c.trunk, c.sha, nil
}

func (c *conflictOnceClient) PutContent(_, _, _, _, _ string, content []byte, _ statewrite.Identity) error {
	c.puts++
	if c.puts == 1 {
		c.trunk = []byte(c.injectYAML)
		c.sha = "sha-after-winner"
		return &statewrite.ConflictError{Err: errString("does not match 409")}
	}
	c.trunk = content
	c.sha = "sha-final"
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

// TestHotfixFinalize_ConcurrentFinalize_SiblingSurvivesThrough409Reapply is the
// load-bearing concurrency test. It drives the REAL hotfix finalize write path
// (hotfixMutation -> statewrite.CommitWithRetry) rather than a serializer in
// isolation. Component "api" finalizes a hotfix on prod while a concurrent
// finalize for "web" wins the race and commits state.components.web between api's
// read and its PUT. api's re-applied write must land
// state.components.api.prod AND preserve the web subtree the losing binary never
// modeled, including a sibling env and an unmodeled key. A regression to a
// whole-state-node rebuild (WriteManifestState) would delete web on the re-apply.
func TestHotfixFinalize_ConcurrentFinalize_SiblingSurvivesThrough409Reapply(t *testing.T) {
	const mergeSHA = "apimergesha"
	timestamp := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)

	// The winner's committed trunk: web gains a NEW dev node plus an unmodeled key
	// on prod, and api keeps its original prod row so the loser re-reads its own
	// prior state.
	const winnerTrunk = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    components:
      api:
        path: api
        tag_prefix: api-
      web:
        path: web
        tag_prefix: web-
  state:
    components:
      api:
        prod:
          sha: apiprodsha
          version: api-1.4.0-rc.2
      web:
        dev:
          sha: webdevsha
          version: web-2.1.0
        prod:
          sha: webprodsha
          version: web-1.4.0-rc.2
          committed_by: web-bot
          unmodeled_key: keep-me
`

	f := &Finalizer{
		manifestKey:   "ci",
		actor:         "api-bot",
		component:     "api",
		deployResults: map[string]string{},
		buildResults:  map[string]string{},
	}
	mutate := f.hotfixMutation("prod", mergeSHA, "api-1.4.0-rc.2.hotfix.1", "apibase", timestamp, []string{"fixsha"})

	client := &conflictOnceClient{
		trunk:      []byte(hotfixComponentManifest),
		sha:        "sha-initial",
		injectYAML: winnerTrunk,
	}

	err := statewrite.CommitWithRetry(statewrite.Options{
		Client:  client,
		Repo:    "owner/repo",
		Path:    "manifest.yaml",
		Ref:     "main",
		Message: "chore: record hotfix",
		Mutate:  mutate,
		Sleep:   func(time.Duration) {},
	})
	require.NoError(t, err)
	require.Equal(t, 2, client.puts, "expected exactly one 409 retry")

	final := readManifestNode(t, client.trunk)

	// api's own leaf landed under its component subtree with the component env
	// branch name.
	api := componentEnvNode(t, final, "api", "prod")
	require.Equal(t, mergeSHA, api["sha"])
	require.Equal(t, "api-1.4.0-rc.2.hotfix.1", api["version"])
	require.Equal(t, "env/api/prod", api["ref"], "component hotfix records env/<component>/<env>")
	require.Equal(t, "apibase", api["base_sha"])
	require.Equal(t, "api-bot", api["committed_by"])

	// The winner's web subtree survived verbatim, including the sibling env and
	// the unmodeled key the loser's typed model never carried.
	webProd := componentEnvNode(t, final, "web", "prod")
	require.Equal(t, "webprodsha", webProd["sha"])
	require.Equal(t, "web-1.4.0-rc.2", webProd["version"])
	require.Equal(t, "web-bot", webProd["committed_by"])
	require.Equal(t, "keep-me", webProd["unmodeled_key"], "unmodeled sibling key must survive the re-apply")
	webDev := componentEnvNode(t, final, "web", "dev")
	require.Equal(t, "webdevsha", webDev["sha"])

	// No flat state.prod node leaked alongside the component form.
	ci := final["ci"].(map[string]any)
	state := ci["state"].(map[string]any)
	_, hasFlatProd := state["prod"]
	require.False(t, hasFlatProd, "component hotfix must not write a flat state.prod node")
}

// TestHotfixFinalize_ComponentReapply_Idempotent proves re-running the mutation
// against bytes that already carry the hotfix leaf yields identical bytes: the
// node-patch is idempotent, so a retry never churns or double-applies.
func TestHotfixFinalize_ComponentReapply_Idempotent(t *testing.T) {
	timestamp := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	f := &Finalizer{
		manifestKey:   "ci",
		actor:         "api-bot",
		component:     "api",
		deployResults: map[string]string{},
		buildResults:  map[string]string{},
	}
	mutate := f.hotfixMutation("prod", "apimergesha", "api-1.4.0-rc.2.hotfix.1", "apibase", timestamp, []string{"fixsha"})

	first, err := mutate([]byte(hotfixComponentManifest))
	require.NoError(t, err)
	second, err := mutate(first)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second), "re-applied component hotfix write must be a no-op")
}

// TestHotfixFinalize_SingleComponentMutation_ByteIdentical proves the empty
// component takes the exact original WriteManifestState path, so a
// single-component hotfix mutation is byte-identical to the direct reference.
func TestHotfixFinalize_SingleComponentMutation_ByteIdentical(t *testing.T) {
	const flat = `ci:
  config:
    environments: [dev, prod]
  state:
    dev:
      sha: devsha
      version: v1.0.0
    prod:
      sha: prodsha
      version: v1.4.0
`
	timestamp := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)

	f := &Finalizer{
		manifestKey:   "ci",
		actor:         "tester",
		deployResults: map[string]string{},
		buildResults:  map[string]string{},
	}
	got, err := f.hotfixMutation("prod", "mergesha", "v1.4.1", "basesha", timestamp, []string{"fixsha"})([]byte(flat))
	require.NoError(t, err)

	// Reference: mirror the exact in-memory mutation, then serialize the
	// historical way via WriteManifestState.
	ref := &Finalizer{
		manifestKey:   "ci",
		actor:         "tester",
		deployResults: map[string]string{},
		buildResults:  map[string]string{},
	}
	fresh, err := config.ParseManifestBytes([]byte(flat), "ci")
	require.NoError(t, err)
	require.NoError(t, ref.applyHotfixState(fresh, "prod", "mergesha", "v1.4.1", "basesha", timestamp, []string{"fixsha"}))
	want, err := config.WriteManifestState([]byte(flat), "ci", fresh.State, fresh.LatestRelease)
	require.NoError(t, err)

	require.Equal(t, string(want), string(got), "single-component hotfix mutation must be byte-identical to WriteManifestState")
}

// TestHotfixFinalize_EnvBranchName_ScopedToComponent proves applyHotfixState
// records the component-scoped integration branch env/<component>/<env>, and the
// default (empty) component keeps the byte-identical env/<env> form.
func TestHotfixFinalize_EnvBranchName_ScopedToComponent(t *testing.T) {
	timestamp := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)

	comp := &Finalizer{actor: "t", manifestKey: "ci", component: "api"}
	compCICD := &config.CICDFile{State: map[string]*config.EnvState{"prod": {SHA: "old", Version: "api-1.0.0"}}}
	require.NoError(t, comp.applyHotfixState(compCICD, "prod", "m", "api-1.0.1", "b", timestamp, []string{"x"}))
	require.Equal(t, "env/api/prod", compCICD.State["prod"].Ref)

	flat := &Finalizer{actor: "t", manifestKey: "ci"}
	flatCICD := &config.CICDFile{State: map[string]*config.EnvState{"prod": {SHA: "old", Version: "v1.0.0"}}}
	require.NoError(t, flat.applyHotfixState(flatCICD, "prod", "m", "v1.0.1", "b", timestamp, []string{"x"}))
	require.Equal(t, "env/prod", flatCICD.State["prod"].Ref)
}

// TestHotfixFinalize_AllocateVersion_ScopedToComponentNamespace proves version
// allocation resolves the hotfix version in the component's own tag namespace: a
// sibling component's hotfix tags never block or advance this component's
// allocation, and the taken api hotfix.1 correctly advances to api hotfix.2.
func TestHotfixFinalize_AllocateVersion_ScopedToComponentNamespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(hotfixComponentManifest), 0o600))

	tags := []string{
		"api-1.4.0-rc.2.hotfix.1", // taken in api's namespace
		"web-1.4.0-rc.2.hotfix.1", // sibling namespace, must be ignored
		"web-1.4.0-rc.2.hotfix.2", // sibling namespace, must be ignored
	}
	f := newFinalizer(t, path, WithComponent("api"), WithTagLister(stubTagLister{tags: tags}))

	got, err := f.allocateVersion("api-1.4.0-rc.2")
	require.NoError(t, err)
	require.Equal(t, "api-1.4.0-rc.2.hotfix.2", got,
		"api's next hotfix skips the taken api hotfix.1 and ignores web's hotfix tags")
}

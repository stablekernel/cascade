package promote

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

// componentManifest is a two-component manifest whose state lives entirely under
// state.components.<name>.<env>, the component-scoped form. It carries a sibling
// component ("billing") the writes below never address, so every test asserts it
// survives verbatim.
const componentManifest = `ci:
  config:
    environments: [dev, prod]
  state:
    components:
      billing:
        prod:
          sha: billsha
          version: v2.0.0
          committed_by: someone
`

// fixedClock returns a deterministic clock so emitted audit timestamps are exact.
func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC) }
}

// promotionForComponent builds a single-env promotion result the finalizer turns
// into one scoped state write.
func promotionForComponent(env, sha, version string) *PromotionResult {
	return &PromotionResult{
		Promotions: []EnvPromotion{{Environment: env, SHA: sha, Version: version}},
	}
}

// readManifestNode parses raw manifest bytes into a generic tree so tests can
// assert on the exact serialized shape (including keys the typed model ignores).
func readManifestNode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

// componentEnv digs out ci.state.components.<comp>.<env> from a parsed manifest,
// failing the test when any level is missing.
func componentEnv(t *testing.T, m map[string]any, comp, env string) map[string]any {
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

// TestFinalizer_WriteConfig_Component_WritesScopedNodeAndKeepsSibling proves the
// local-disk WriteConfig path records the promoted env under
// state.components.<name>.<env> and leaves an unaddressed sibling component
// byte-intact. A regression to WriteManifestState (whole-state-node rebuild)
// would drop state.components.billing here.
func TestFinalizer_WriteConfig_Component_WritesScopedNodeAndKeepsSibling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(componentManifest), 0644))

	fin, err := NewFinalizer(path, "prod", WithComponent("checkout"), WithClock(fixedClock()))
	require.NoError(t, err)
	fin.SetActor("promoter")
	fin.SetPromotionResult(promotionForComponent("prod", "checkoutsha", "v1.4.0"))

	require.NoError(t, fin.Run())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	m := readManifestNode(t, out)

	// checkout's prod state was written under its own subtree.
	got := componentEnv(t, m, "checkout", "prod")
	require.Equal(t, "checkoutsha", got["sha"])
	require.Equal(t, "v1.4.0", got["version"])
	require.Equal(t, "promoter", got["committed_by"])

	// The sibling billing component is preserved verbatim.
	sib := componentEnv(t, m, "billing", "prod")
	require.Equal(t, "billsha", sib["sha"])
	require.Equal(t, "v2.0.0", sib["version"])
	require.Equal(t, "someone", sib["committed_by"])

	// No flat state.<env> leaked alongside the component form.
	ci := m["ci"].(map[string]any)
	state := ci["state"].(map[string]any)
	_, hasFlatProd := state["prod"]
	require.False(t, hasFlatProd, "component finalize must not write a flat state.prod node")
}

// TestFinalizer_WriteConfig_SingleComponent_ByteIdentical proves an empty
// component takes the exact original WriteManifestState path, so a
// single-component manifest round-trips byte-for-byte identical to the
// historical single-component behavior.
func TestFinalizer_WriteConfig_SingleComponent_ByteIdentical(t *testing.T) {
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

	// Path A: the new finalizer with no component set.
	pathA := filepath.Join(dir, "a.yaml")
	require.NoError(t, os.WriteFile(pathA, []byte(flat), 0644))
	finA, err := NewFinalizer(pathA, "prod", WithClock(fixedClock()))
	require.NoError(t, err)
	finA.SetActor("promoter")
	finA.SetPromotionResult(promotionForComponent("prod", "prodsha", "v1.0.0"))
	require.NoError(t, finA.Run())
	gotA, err := os.ReadFile(pathA)
	require.NoError(t, err)

	// Path B: the direct WriteManifestState reference, mirroring the exact
	// in-memory mutation the finalizer performs, then serialized the historical
	// way. Both must be byte-identical.
	pathB := filepath.Join(dir, "b.yaml")
	require.NoError(t, os.WriteFile(pathB, []byte(flat), 0644))
	finB, err := NewFinalizer(pathB, "prod", WithClock(fixedClock()))
	require.NoError(t, err)
	finB.SetActor("promoter")
	finB.SetPromotionResult(promotionForComponent("prod", "prodsha", "v1.0.0"))
	finB.updateState()
	ref, err := config.WriteManifestState([]byte(flat), config.DefaultManifestKey, finB.cicdFile.State, finB.cicdFile.LatestRelease)
	require.NoError(t, err)

	require.Equal(t, string(ref), string(gotA), "single-component finalize must be byte-identical to WriteManifestState")
}

// conflictOnceClient is a fake Contents client that models a concurrent sibling
// finalize: it serves trunk bytes, and on the first PutContent it (a) injects a
// brand-new sibling-component env node that the writing binary never modeled,
// exactly as a racing finalizer would have committed, and (b) returns a 409 so
// CommitWithRetry re-fetches and re-applies. The second PutContent succeeds.
type conflictOnceClient struct {
	trunk      []byte
	sha        string
	puts       int
	injectYAML string // sibling subtree spliced in on the conflict
}

func (c *conflictOnceClient) GetContent(_, _, _ string) ([]byte, string, error) {
	return c.trunk, c.sha, nil
}

func (c *conflictOnceClient) PutContent(_, _, _, _, _ string, content []byte, _ statewrite.Identity) error {
	c.puts++
	if c.puts == 1 {
		// A concurrent finalizer won the race: rewrite trunk to carry its freshly
		// committed sibling subtree, then reject this PUT with a 409 so the loser
		// re-reads and re-applies on top.
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

// TestFinalizer_ConcurrentFinalize_SiblingSurvivesThrough409Reapply is the
// keystone concurrency test. It drives the REAL finalize write path (writeStateViaAPI ->
// statewrite.CommitWithRetry -> the finalizer's own Mutate closure), not the
// serializer in isolation. Component "checkout" finalizes (checkout, prod) while
// a concurrent finalizer for "billing" wins the race and commits
// state.components.billing.prod between checkout's read and its PUT. checkout's
// re-applied write must land state.components.checkout.prod AND preserve the
// billing subtree the losing binary never modeled. A regression to a
// whole-state-node rebuild would delete billing on the re-apply.
func TestFinalizer_ConcurrentFinalize_SiblingSurvivesThrough409Reapply(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "owner/repo")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REF", "refs/heads/main")

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	// The finalizer's own manifest carries only its config; the trunk bytes the
	// client serves are the source of truth for the merge.
	require.NoError(t, os.WriteFile(path, []byte(componentManifest), 0644))

	// The winner's committed trunk: billing gains a NEW prod node (with a field
	// the loser's typed model does not carry) plus a sibling env, so the test
	// proves survival of an unmodeled sibling subtree, the #389 class at
	// component depth.
	winnerTrunk := `ci:
  config:
    environments: [dev, prod]
  state:
    components:
      billing:
        dev:
          sha: billdevsha
          version: v2.1.0
        prod:
          sha: billprodsha
          version: v2.0.0
          committed_by: billing-bot
          unmodeled_key: keep-me
`
	client := &conflictOnceClient{
		trunk:      []byte(componentManifest),
		sha:        "sha-initial",
		injectYAML: winnerTrunk,
	}

	fin, err := NewFinalizer(path, "prod",
		WithComponent("checkout"),
		WithClock(fixedClock()),
		withContentsClient(client),
	)
	require.NoError(t, err)
	fin.SetActor("checkout-bot")
	fin.SetPromotionResult(promotionForComponent("prod", "checkoutprodsha", "v1.4.0"))
	fin.updateState()

	require.NoError(t, fin.writeStateViaAPI("chore: update state"))
	require.Equal(t, 2, client.puts, "expected exactly one 409 retry")

	final := readManifestNode(t, client.trunk)

	// checkout's own leaf landed.
	checkout := componentEnv(t, final, "checkout", "prod")
	require.Equal(t, "checkoutprodsha", checkout["sha"])
	require.Equal(t, "v1.4.0", checkout["version"])
	require.Equal(t, "checkout-bot", checkout["committed_by"])

	// The winner's billing subtree survived verbatim, including the sibling env
	// and the unmodeled key.
	billProd := componentEnv(t, final, "billing", "prod")
	require.Equal(t, "billprodsha", billProd["sha"])
	require.Equal(t, "v2.0.0", billProd["version"])
	require.Equal(t, "billing-bot", billProd["committed_by"])
	require.Equal(t, "keep-me", billProd["unmodeled_key"], "unmodeled sibling key must survive the re-apply")
	billDev := componentEnv(t, final, "billing", "dev")
	require.Equal(t, "billdevsha", billDev["sha"])
}

// TestFinalizer_ComponentReapply_Idempotent proves re-running the finalizer's
// Mutate closure against bytes that already carry its target leaf yields
// identical bytes: the node-patch is idempotent, so a retry never churns or
// duplicates the component's env node.
func TestFinalizer_ComponentReapply_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(componentManifest), 0644))

	fin, err := NewFinalizer(path, "prod", WithComponent("checkout"), WithClock(fixedClock()))
	require.NoError(t, err)
	fin.SetActor("promoter")
	fin.SetPromotionResult(promotionForComponent("prod", "checkoutsha", "v1.4.0"))
	fin.updateState()

	mutate := fin.stateMutation(config.DefaultManifestKey)
	first, err := mutate([]byte(componentManifest))
	require.NoError(t, err)
	second, err := mutate(first)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second), "re-applied component write must be a no-op")
}

// TestPromoter_saveConfig_Component_WritesScopedNode proves the Promoter's
// non-dry-run saveConfig (the simulate path) honors its component scope: state is
// serialized under state.components.<component>.<env> and an unaddressed sibling
// component survives. An empty component keeps the flat form (covered elsewhere).
func TestPromoter_saveConfig_Component_WritesScopedNode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(componentManifest), 0644))

	p, err := NewPromoter(PromoterOptions{ConfigPath: path, Actor: "sim", Component: "checkout"})
	require.NoError(t, err)

	// Model the in-memory state the promoter would carry for its component.
	p.cicdFile.State = map[string]*config.EnvState{
		"prod": {SHA: "checkoutsha", Version: "v1.4.0", CommittedBy: "sim"},
	}
	require.NoError(t, p.saveConfig())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	m := readManifestNode(t, out)

	got := componentEnv(t, m, "checkout", "prod")
	require.Equal(t, "checkoutsha", got["sha"])
	require.Equal(t, "v1.4.0", got["version"])

	sib := componentEnv(t, m, "billing", "prod")
	require.Equal(t, "billsha", sib["sha"])
	require.Equal(t, "v2.0.0", sib["version"])
}

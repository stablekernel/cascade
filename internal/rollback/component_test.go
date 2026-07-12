package rollback

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/statewrite"
)

// twoComponentManifest is a component-scoped manifest whose recorded state lives
// entirely under state.components.<name>.<env>. "checkout" carries a
// deploy-history ring the rollback under test resolves against; the sibling
// "billing" component is never addressed, so every assertion proves it survives
// verbatim.
const twoComponentManifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    components:
      checkout:
        path: services/checkout
        tag_grammar:
          prefix: checkout-
      billing:
        path: services/billing
        tag_grammar:
          prefix: billing-
  state:
    components:
      checkout:
        prod:
          sha: checkoutcur
          version: v1.4.0
          committed_by: someone
          previous:
            - sha: checkoutprev
              version: v1.3.0
      billing:
        prod:
          sha: billingcur
          version: v2.0.0
          committed_by: billing-bot
          unmodeled_key: keep-me
          previous:
            - sha: billingprev
              version: v1.9.0
`

// parseManifestTree parses raw manifest bytes into a generic tree so a test can
// assert on the exact serialized shape, including keys the typed model ignores.
func parseManifestTree(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

// componentEnvLeaf digs out ci.state.components.<comp>.<env> from a parsed
// manifest, failing the test when any level is missing.
func componentEnvLeaf(t *testing.T, m map[string]any, comp, env string) map[string]any {
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

// TestApply_Component_WritesScopedNodeAndKeepsSibling proves the local-disk write
// path records the rolled-back env under state.components.<name>.<env>, namespaces
// the divergence ref as rollback/<component>/<env>, and leaves the unaddressed
// sibling component byte-intact. A regression to the flat WriteManifestState path
// would drop state.components.billing here.
func TestApply_Component_WritesScopedNodeAndKeepsSibling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(twoComponentManifest), 0644))

	rb, err := New(Options{ConfigPath: path, Actor: "oncall", Component: "checkout"})
	require.NoError(t, err)

	// Default target resolves the N-1 entry from checkout's own ring.
	plan, err := rb.Plan("prod", "", "")
	require.NoError(t, err)
	require.Equal(t, "checkoutprev", plan.Target.SHA)
	require.Equal(t, "previous-ring", plan.Target.Source)

	require.NoError(t, rb.Apply(plan))

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	m := parseManifestTree(t, out)

	// checkout's prod state was written under its own subtree with the rollback ref
	// namespaced to the component.
	got := componentEnvLeaf(t, m, "checkout", "prod")
	require.Equal(t, "checkoutprev", got["sha"])
	require.Equal(t, "v1.3.0", got["version"])
	require.Equal(t, "oncall", got["committed_by"])
	require.Equal(t, "rollback/checkout/prod", got["ref"])

	// The sibling billing component is preserved verbatim, including the key the
	// binary does not model.
	sib := componentEnvLeaf(t, m, "billing", "prod")
	require.Equal(t, "billingcur", sib["sha"])
	require.Equal(t, "v2.0.0", sib["version"])
	require.Equal(t, "billing-bot", sib["committed_by"])
	require.Equal(t, "keep-me", sib["unmodeled_key"])

	// No flat state.<env> leaked alongside the component form.
	ci := m["ci"].(map[string]any)
	state := ci["state"].(map[string]any)
	_, hasFlatProd := state["prod"]
	require.False(t, hasFlatProd, "component rollback must not write a flat state.prod node")
}

// conflictOnceClient is a fake Contents client that models a concurrent sibling
// rollback: it serves trunk bytes, and on the first PutContent it (a) rewrites
// trunk to carry a racing finalizer's freshly committed sibling subtree that the
// writing binary never modeled, and (b) returns a 409 so CommitWithRetry
// re-fetches and re-applies. The second PutContent succeeds.
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

// TestConcurrentFinalize_SiblingSurvivesThrough409Reapply drives the real rollback
// API write path (writeStateViaAPI -> statewrite.CommitWithRetry -> the
// Rollbacker's own Mutate closure), not the serializer in isolation. Component
// "checkout" rolls back (checkout, prod) while a concurrent finalizer for
// "billing" wins the race and commits state.components.billing.prod between
// checkout's read and its PUT. checkout's re-applied write must land
// state.components.checkout.prod AND preserve the billing subtree the losing
// binary never modeled.
func TestConcurrentFinalize_SiblingSurvivesThrough409Reapply(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "owner/repo")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REF", "refs/heads/main")

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(twoComponentManifest), 0644))

	// The winner's committed trunk: billing gains a NEW dev node plus an unmodeled
	// key on prod, so the test proves survival of an unmodeled sibling subtree.
	winnerTrunk := `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    components:
      checkout:
        path: services/checkout
        tag_grammar:
          prefix: checkout-
      billing:
        path: services/billing
        tag_grammar:
          prefix: billing-
  state:
    components:
      checkout:
        prod:
          sha: checkoutcur
          version: v1.4.0
          previous:
            - sha: checkoutprev
              version: v1.3.0
      billing:
        dev:
          sha: billingdevsha
          version: v2.1.0
        prod:
          sha: billingprodsha
          version: v2.0.0
          committed_by: billing-bot
          unmodeled_key: keep-me
`
	client := &conflictOnceClient{
		trunk:      []byte(twoComponentManifest),
		sha:        "sha-initial",
		injectYAML: winnerTrunk,
	}

	rb, err := New(Options{ConfigPath: path, Actor: "checkout-bot", Component: "checkout"})
	require.NoError(t, err)
	rb.contentsClient = client

	plan, err := rb.Plan("prod", "", "")
	require.NoError(t, err)
	require.NoError(t, rb.Apply(plan))

	require.NoError(t, rb.writeStateViaAPI("chore: update state after rollback of prod [skip ci]"))
	require.Equal(t, 2, client.puts, "expected exactly one 409 retry")

	final := parseManifestTree(t, client.trunk)

	// checkout's own leaf landed the rolled-back SHA.
	checkout := componentEnvLeaf(t, final, "checkout", "prod")
	require.Equal(t, "checkoutprev", checkout["sha"])
	require.Equal(t, "v1.3.0", checkout["version"])
	require.Equal(t, "checkout-bot", checkout["committed_by"])
	require.Equal(t, "rollback/checkout/prod", checkout["ref"])

	// The winner's billing subtree survived verbatim, including the sibling env and
	// the unmodeled key.
	billProd := componentEnvLeaf(t, final, "billing", "prod")
	require.Equal(t, "billingprodsha", billProd["sha"])
	require.Equal(t, "billing-bot", billProd["committed_by"])
	require.Equal(t, "keep-me", billProd["unmodeled_key"])
	billDev := componentEnvLeaf(t, final, "billing", "dev")
	require.Equal(t, "billingdevsha", billDev["sha"])
}

// TestApply_SingleComponent_RefUnchanged proves the empty-component path is
// byte-identical: the rollback records a flat state.<env> node and the historical
// rollback/<env> ref, with no components subtree introduced.
func TestApply_SingleComponent_RefUnchanged(t *testing.T) {
	const flat = `ci:
  config:
    environments: [dev, prod]
  state:
    prod:
      sha: prodcur
      version: v1.4.0
      previous:
        - sha: prodprev
          version: v1.3.0
`
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(flat), 0644))

	rb, err := New(Options{ConfigPath: path, Actor: "oncall"})
	require.NoError(t, err)
	plan, err := rb.Plan("prod", "", "")
	require.NoError(t, err)
	require.NoError(t, rb.Apply(plan))

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	m := parseManifestTree(t, out)

	ci := m["ci"].(map[string]any)
	state := ci["state"].(map[string]any)
	prod, ok := state["prod"].(map[string]any)
	require.True(t, ok, "flat state.prod present")
	require.Equal(t, "prodprev", prod["sha"])
	require.Equal(t, "rollback/prod", prod["ref"])
	_, hasComponents := state["components"]
	require.False(t, hasComponents, "single-component rollback must not introduce a components subtree")
}

// TestFirstEnv_ScopedToComponentSubset proves first-environment eligibility is
// judged against the selected component's own environment subset, not the global
// ladder. checkout's ladder starts at staging; the global ladder starts at dev.
// Rolling back checkout's staging trips the first-env guard, while the same env is
// not first globally.
func TestFirstEnv_ScopedToComponentSubset(t *testing.T) {
	const manifest = `ci:
  config:
    environments: [dev, staging, prod]
    components:
      checkout:
        path: services/checkout
        tag_grammar:
          prefix: checkout-
        environments: [staging, prod]
  state:
    components:
      checkout:
        prod:
          sha: checkoutcur
          version: v1.4.0
`
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0644))

	// Component scope: staging is checkout's first env, so the guard fires.
	rbComp, err := New(Options{ConfigPath: path, Actor: "oncall", Component: "checkout"})
	require.NoError(t, err)
	_, err = rbComp.Plan("staging", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "first environment")

	// Same env under the global (single-component) ladder is NOT first (dev is), so
	// staging is judged on its merits and the error, if any, is not the first-env
	// guard.
	rbSingle, err := New(Options{ConfigPath: path, Actor: "oncall"})
	require.NoError(t, err)
	_, err = rbSingle.Plan("staging", "", "")
	if err != nil {
		require.NotContains(t, err.Error(), "first environment")
	}

	// The global first env (dev) still trips the guard on the single-component path,
	// proving that path is unchanged.
	_, err = rbSingle.Plan("dev", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "first environment")
}

// TestPreviousRing_ScopedToComponent proves the deploy-history ring the default
// target resolves against is the selected component's own ring, not a sibling's or
// a flat one. checkout's ring N-1 is checkoutprev; billing's is billingprev.
func TestPreviousRing_ScopedToComponent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(twoComponentManifest), 0644))

	rb, err := New(Options{ConfigPath: path, Actor: "oncall", Component: "checkout"})
	require.NoError(t, err)

	plan, err := rb.Plan("prod", "", "")
	require.NoError(t, err)
	require.Equal(t, "checkoutprev", plan.Target.SHA, "must resolve checkout's own ring, not a sibling's")
	require.Equal(t, "previous-ring", plan.Target.Source)
	require.NotEqual(t, "billingprev", plan.Target.SHA)
}

// TestComponentReapply_Idempotent proves re-running the Rollbacker's Mutate closure
// against bytes that already carry its target leaf yields identical bytes: the
// node-patch is idempotent, so a retry never churns or duplicates the env node.
func TestComponentReapply_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(twoComponentManifest), 0644))

	rb, err := New(Options{ConfigPath: path, Actor: "oncall", Component: "checkout"})
	require.NoError(t, err)
	plan, err := rb.Plan("prod", "", "")
	require.NoError(t, err)
	require.NoError(t, rb.Apply(plan))

	mutate := rb.stateMutation("ci")
	first, err := mutate([]byte(twoComponentManifest))
	require.NoError(t, err)
	second, err := mutate(first)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second), "re-applied component write must be a no-op")
	require.True(t, strings.Contains(string(first), "rollback/checkout/prod"))
}

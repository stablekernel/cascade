package rollback

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// deploysOverrideManifest declares a repo-global deploy set and a "checkout"
// component that overrides it with its own. The generator emits the component's
// rollback workflow from the resolved component config, so its deploy jobs and
// the finalize gate that reads their results are named "checkout-svc". A runtime
// that keeps the root config instead reports "root-svc", and the two disagree.
// The sibling "billing" component pins a third name so a swap that resolved the
// wrong component is distinguishable from one that resolved none.
const deploysOverrideManifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    deploys:
      - name: root-svc
        uses: ./.github/workflows/deploy.yaml
    components:
      checkout:
        path: services/checkout
        tag_grammar:
          prefix: checkout-
        deploys:
          - name: checkout-svc
            uses: ./.github/workflows/deploy-checkout.yaml
      billing:
        path: services/billing
        tag_grammar:
          prefix: billing-
        deploys:
          - name: billing-svc
            uses: ./.github/workflows/deploy-billing.yaml
  state:
    components:
      checkout:
        prod:
          sha: checkoutcur
          version: checkout-1.4.0
          committed_by: someone
          previous:
            - sha: checkoutprev
              version: checkout-1.3.0
`

// writeResolvedConfigManifest drops raw manifest bytes into a temp dir and returns the path.
func writeResolvedConfigManifest(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade.yaml")
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
	return path
}

// TestNew_Component_DeployNamesComeFromResolvedConfig pins the observable the
// component rollback gate depends on: DeployNames feeds gateOnDeployResults in
// rollback finalize, which reads DEPLOY_RESULT_<NAME> for each name it returns.
// The generated component rollback workflow emits deploy jobs from the resolved
// component config, so a runtime reporting the root deploy name gates on a
// result that never exists while the deploys it did run go unchecked.
func TestNew_Component_DeployNamesComeFromResolvedConfig(t *testing.T) {
	path := writeResolvedConfigManifest(t, deploysOverrideManifest)

	rb, err := New(Options{ConfigPath: path, Component: "checkout"})
	require.NoError(t, err)

	require.Equal(t, []string{"checkout-svc"}, rb.DeployNames(),
		"a component rollback must gate on its own resolved deploys, not the root deploy set")
}

// TestNew_Component_KnownDeployableFollowsResolvedConfig covers the second sink
// of the same class: --deployable validation enumerates the working config's
// deploys, so a component rollback must accept its own deployable and reject a
// root one it never deploys.
func TestNew_Component_KnownDeployableFollowsResolvedConfig(t *testing.T) {
	path := writeResolvedConfigManifest(t, deploysOverrideManifest)

	rb, err := New(Options{ConfigPath: path, Component: "checkout"})
	require.NoError(t, err)

	require.True(t, rb.knownDeployable("checkout-svc"),
		"the component's own deployable must be known")
	require.False(t, rb.knownDeployable("root-svc"),
		"a root deployable the component never declares must not be known to it")
}

// TestNew_Component_SiblingDeploysDoNotLeak proves the swap resolves the named
// component rather than an arbitrary one.
func TestNew_Component_SiblingDeploysDoNotLeak(t *testing.T) {
	path := writeResolvedConfigManifest(t, deploysOverrideManifest)

	rb, err := New(Options{ConfigPath: path, Component: "billing"})
	require.NoError(t, err)

	require.Equal(t, []string{"billing-svc"}, rb.DeployNames())
	require.False(t, rb.knownDeployable("checkout-svc"),
		"a sibling component's deployable must not be visible")
}

// TestNew_SingleComponent_KeepsRootConfig pins the single-component path
// byte-identical: an empty component resolves nothing and keeps the root
// deploys, so the swap is a no-op off the component path.
func TestNew_SingleComponent_KeepsRootConfig(t *testing.T) {
	path := writeResolvedConfigManifest(t, deploysOverrideManifest)

	rb, err := New(Options{ConfigPath: path})
	require.NoError(t, err)

	require.Equal(t, []string{"root-svc"}, rb.DeployNames())
}

// TestNew_UndeclaredComponent_IsRefused pins rollback's existing strictness for
// a component recorded only under state.components without a config.components
// declaration. Rollback refuses it rather than falling back to the root config:
// an undeclared component has no resolved deploys or ladder, so there is no
// config the runtime could roll back against. The config swap under test
// deliberately keeps this behavior instead of adopting the tolerant
// declared-only guard the promote path uses, where an unresolvable component
// merely means no overrides to apply.
func TestNew_UndeclaredComponent_IsRefused(t *testing.T) {
	const raw = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    deploys:
      - name: root-svc
        uses: ./.github/workflows/deploy.yaml
  state:
    components:
      seeded:
        prod:
          sha: seededcur
          version: v1.0.0
`
	path := writeResolvedConfigManifest(t, raw)

	_, err := New(Options{ConfigPath: path, Component: "seeded"})
	require.ErrorContains(t, err, `component "seeded" is not declared`)
}

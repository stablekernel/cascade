package rollback

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// componentManifestAt renders a two-component manifest whose recorded state lives
// entirely under state.components.<name>.<env>. Each component carries its own
// prod sha/version so a git-history rollback can be proven to resolve strictly
// from its own subtree and never a sibling's.
func componentManifestAt(checkoutSHA, checkoutVersion, billingSHA, billingVersion string) string {
	return `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    components:
      checkout:
        path: services/checkout
        tag_prefix: checkout-
      billing:
        path: services/billing
        tag_prefix: billing-
  state:
    components:
      checkout:
        prod:
          sha: ` + checkoutSHA + `
          version: ` + checkoutVersion + `
          committed_at: "2026-04-01T11:00:00Z"
          committed_by: alice
      billing:
        prod:
          sha: ` + billingSHA + `
          version: ` + billingVersion + `
          committed_at: "2026-04-01T11:00:00Z"
          committed_by: bob
`
}

// TestGitHistoryReader_ComponentScoped_IsolatesSiblingHistory proves the
// component-aware git-history reader resolves prior deployments strictly from its
// own state.components.<component>.<env> subtree. The negative assertion is the
// point: checkout's reader must never surface billing's deeper history, and the
// empty-component (flat) reader stays byte-identical, reading only state.<env>.
func TestGitHistoryReader_ComponentScoped_IsolatesSiblingHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)

	rel := "manifest.yaml"
	// Old commit: each component records its own distinct prior deployment.
	gitCommitFile(t, dir, rel,
		componentManifestAt("checkoutold111", "checkout-v1.0.0", "billingdeep22", "billing-secret-v0.0.1"),
		"checkout v1.0.0, billing v0.0.1")
	// New commit: both advance, leaving the priors recoverable only from history.
	gitCommitFile(t, dir, rel,
		componentManifestAt("checkoutnew333", "checkout-v2.0.0", "billingnew444", "billing-v2.0.0"),
		"checkout v2.0.0, billing v2.0.0")

	path := filepath.Join(dir, rel)

	// checkout's reader returns checkout's history and NEVER billing's.
	checkoutStates, err := newGitHistoryReader(path, config.DefaultManifestKey, "checkout").PriorStates("prod")
	require.NoError(t, err)
	require.NotEmpty(t, checkoutStates, "component git-history must recover checkout's own subtree")
	var sawCheckoutPrior bool
	for _, s := range checkoutStates {
		require.NotEqual(t, "billingdeep22", s.SHA, "checkout reader leaked a sibling deployment")
		require.NotEqual(t, "billing-secret-v0.0.1", s.Version, "checkout reader leaked a sibling version")
		require.NotEqual(t, "billingnew444", s.SHA, "checkout reader leaked a sibling deployment")
		if s.SHA == "checkoutold111" && s.Version == "checkout-v1.0.0" {
			sawCheckoutPrior = true
		}
	}
	require.True(t, sawCheckoutPrior, "checkout's own prior deployment not recovered from history")

	// billing's reader returns billing's history and NEVER checkout's.
	billingStates, err := newGitHistoryReader(path, config.DefaultManifestKey, "billing").PriorStates("prod")
	require.NoError(t, err)
	var sawBillingPrior bool
	for _, s := range billingStates {
		require.NotEqual(t, "checkoutold111", s.SHA, "billing reader leaked a sibling deployment")
		if s.SHA == "billingdeep22" && s.Version == "billing-secret-v0.0.1" {
			sawBillingPrior = true
		}
	}
	require.True(t, sawBillingPrior, "billing's own prior deployment not recovered from history")

	// Empty-component reader is byte-identical to the pre-component behaviour: it
	// reads only the flat state.<env>, which this components-only manifest never
	// populates, so it recovers nothing (no cross-over into a component subtree).
	flatStates, err := newGitHistoryReader(path, config.DefaultManifestKey, "").PriorStates("prod")
	require.NoError(t, err)
	require.Empty(t, flatStates, "flat reader must not descend into the components subtree")
}

// TestGitHistoryReader_ComponentScoped_RollbackRecoversOwnPriorDeployment drives
// the full rollback flow: a component whose live manifest has advanced past a
// deployment recovers its prior sha/version from its OWN git history, and a
// target that exists only in a sibling's history is not resolvable under the
// component's scope.
func TestGitHistoryReader_ComponentScoped_RollbackRecoversOwnPriorDeployment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)

	rel := "manifest.yaml"
	gitCommitFile(t, dir, rel,
		componentManifestAt("checkoutold111", "checkout-v1.0.0", "billingdeep22", "billing-secret-v0.0.1"),
		"checkout v1.0.0, billing v0.0.1")
	gitCommitFile(t, dir, rel,
		componentManifestAt("checkoutnew333", "checkout-v2.0.0", "billingnew444", "billing-v2.0.0"),
		"checkout v2.0.0, billing v2.0.0")

	path := filepath.Join(dir, rel)

	rb, err := New(Options{ConfigPath: path, Actor: "oncall", Component: "checkout"})
	require.NoError(t, err)

	// checkout's own prior deployment resolves from git history.
	plan, err := rb.Plan("prod", "checkout-v1.0.0", "")
	require.NoError(t, err)
	require.Equal(t, "checkoutold111", plan.Target.SHA)
	require.Equal(t, "git-history", plan.Target.Source)

	// A version that lives ONLY in billing's history is not resolvable under
	// checkout's scope: the sibling's history is never read.
	_, err = rb.Plan("prod", "billing-secret-v0.0.1", "")
	require.Error(t, err, "checkout rollback must not resolve a sibling's historical deployment")
}

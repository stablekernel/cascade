package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// componentSourceManifest records a component's dev seed only under
// state.components.api.dev, exactly as a per-component orchestrate now writes it.
// No flat state.dev row exists, so a component promotion that read the flat map
// would see "no deployments".
const componentSourceManifest = `ci:
  config:
    environments: [dev, prod]
  state:
    components:
      api:
        dev:
          sha: apidevsha
          version: api-0.1.0-rc.0
      web:
        dev:
          sha: webdevsha
          version: web-0.1.0-rc.0
`

// TestNewPromoter_Component_OverlaysComponentSourceState proves a component-scoped
// promotion reads its source-env seed from state.components.<component>.<env>: the
// dev-to-prod cascade for api finds api's dev deployment (recorded only under the
// component subtree) and promotes it, rather than failing with "source
// environment 'dev' has no deployments". This is the read counterpart to the
// per-component orchestrate seed write; without the overlay the two paths are
// coupled through the flat state.dev row and a component promotion regresses.
func TestNewPromoter_Component_OverlaysComponentSourceState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(componentSourceManifest), 0o644))

	p, err := NewPromoter(PromoterOptions{
		ConfigPath: path,
		DryRun:     true,
		Actor:      "test-actor",
		Component:  "api",
	})
	require.NoError(t, err)

	result, err := p.Promote(ModeCascade, "dev-to-prod")
	require.NoError(t, err)
	require.True(t, result.Success, "component promotion must succeed reading its own dev seed; error: %s", result.Error)
	require.NotEmpty(t, result.Promotions)

	// The promotion carries api's own dev seed SHA forward, proving the source
	// read resolved the component subtree rather than a missing flat row. The
	// promoted version is api's release line (the rc suffix is dropped on the
	// promotion to prod, standard promotion semantics).
	last := result.Promotions[len(result.Promotions)-1]
	require.Equal(t, "prod", last.Environment)
	require.Equal(t, "apidevsha", last.SHA)
	require.Equal(t, "api-0.1.0", last.Version)
}

// TestNewPromoter_Component_IgnoresSiblingSourceState proves the overlay is scoped
// to the promoting component: api's promotion never reads web's dev seed, so a
// sibling's version line cannot leak into api's promotion.
func TestNewPromoter_Component_IgnoresSiblingSourceState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(componentSourceManifest), 0o644))

	p, err := NewPromoter(PromoterOptions{
		ConfigPath: path,
		DryRun:     true,
		Actor:      "test-actor",
		Component:  "api",
	})
	require.NoError(t, err)

	result, err := p.Promote(ModeCascade, "dev-to-prod")
	require.NoError(t, err)
	require.True(t, result.Success)
	last := result.Promotions[len(result.Promotions)-1]
	require.NotEqual(t, "webdevsha", last.SHA, "api promotion must not read web's dev seed")
	require.NotEqual(t, "web-0.1.0-rc.0", last.Version)
}

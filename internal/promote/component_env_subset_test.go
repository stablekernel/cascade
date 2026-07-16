package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// componentEnvSubsetManifest declares a global three-env ladder [dev, staging,
// prod] and two components: "api" narrows its ladder to the strict subset [dev,
// staging] while "web" inherits the full global ladder. Each component seeds its
// own dev deployment under state.components.<name>.dev so a component-scoped
// promotion has a source to advance.
const componentEnvSubsetManifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, staging, prod]
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-
        environments: [dev, staging]
      web:
        path: services/web
        tag_grammar:
          prefix: web-
  state:
    components:
      api:
        dev:
          sha: apidevsha
          version: api-1.0.0-rc.0
      web:
        dev:
          sha: webdevsha
          version: web-1.0.0-rc.0
`

func writeSubsetManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(componentEnvSubsetManifest), 0o644))
	return path
}

// TestNewPromoter_ComponentSubset_CascadeToProdRejected proves the promotion
// runtime respects a component's narrowed environment ladder: "api" declares
// [dev, staging], so a cascade targeting the global-only "prod" env must be
// rejected because prod is not in api's resolved ladder. Before the runtime honored
// the subset it read the global [dev, staging, prod] ladder and would happily
// promote api into prod, an env api never targets.
func TestNewPromoter_ComponentSubset_CascadeToProdRejected(t *testing.T) {
	path := writeSubsetManifest(t)

	p, err := NewPromoter(PromoterOptions{
		ConfigPath: path,
		DryRun:     true,
		Actor:      "test-actor",
		Component:  "api",
	})
	require.NoError(t, err)

	result, err := p.Promote(ModeCascade, "dev-to-prod")
	require.NoError(t, err)
	require.False(t, result.Success, "cascade into prod must fail: prod is outside api's ladder [dev, staging]")
	require.Contains(t, result.Error, "prod")
}

// TestNewPromoter_ComponentSubset_CascadeStaysInLadder proves a cascade within
// api's subset succeeds and never reaches beyond its last env. dev-to-staging is
// valid; staging is api's final env.
func TestNewPromoter_ComponentSubset_CascadeStaysInLadder(t *testing.T) {
	path := writeSubsetManifest(t)

	p, err := NewPromoter(PromoterOptions{
		ConfigPath: path,
		DryRun:     true,
		Actor:      "test-actor",
		Component:  "api",
	})
	require.NoError(t, err)

	result, err := p.Promote(ModeCascade, "dev-to-staging")
	require.NoError(t, err)
	require.True(t, result.Success, "dev-to-staging is inside api's ladder; error: %s", result.Error)
	for _, promo := range result.Promotions {
		require.NotEqual(t, "prod", promo.Environment, "api must never target prod")
	}
	require.Equal(t, "apidevsha", result.Promotions[len(result.Promotions)-1].SHA)
}

// TestNewPromoter_ComponentSubset_DefaultTreatsLastSubsetEnvAsFinal proves default
// (sequential) mode advances api only through its own ladder and treats staging,
// the last env of api's subset, as the final environment. No promotion targets the
// global-only prod env.
func TestNewPromoter_ComponentSubset_DefaultTreatsLastSubsetEnvAsFinal(t *testing.T) {
	path := writeSubsetManifest(t)

	p, err := NewPromoter(PromoterOptions{
		ConfigPath: path,
		DryRun:     true,
		Actor:      "test-actor",
		Component:  "api",
	})
	require.NoError(t, err)

	result, err := p.Promote(ModeDefault, "")
	require.NoError(t, err)
	require.True(t, result.Success, "default promotion must advance api within its ladder; error: %s", result.Error)
	require.NotEmpty(t, result.Promotions)
	for _, promo := range result.Promotions {
		require.NotEqual(t, "prod", promo.Environment, "api must never target the global-only prod env")
	}
}

// TestNewPromoter_ComponentSubset_SiblingKeepsFullLadder proves the narrowing is
// scoped to the addressed component: "web" inherits the full global ladder, so a
// cascade to prod succeeds and lands on prod.
func TestNewPromoter_ComponentSubset_SiblingKeepsFullLadder(t *testing.T) {
	path := writeSubsetManifest(t)

	p, err := NewPromoter(PromoterOptions{
		ConfigPath: path,
		DryRun:     true,
		Actor:      "test-actor",
		Component:  "web",
	})
	require.NoError(t, err)

	result, err := p.Promote(ModeCascade, "dev-to-prod")
	require.NoError(t, err)
	require.True(t, result.Success, "web keeps the full ladder; dev-to-prod must succeed; error: %s", result.Error)
	require.Equal(t, "prod", result.Promotions[len(result.Promotions)-1].Environment)
	require.Equal(t, "webdevsha", result.Promotions[len(result.Promotions)-1].SHA)
}

// TestApplyComponentLadder_EmptyComponentUnchanged proves the single-component
// (empty component) path leaves the global ladder byte-identical: applyResolvedComponentConfig
// is a no-op and the working config still carries the full global ladder.
func TestApplyComponentLadder_EmptyComponentUnchanged(t *testing.T) {
	path := writeSubsetManifest(t)
	cicdFile, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	require.NoError(t, err)

	before := append([]string(nil), cicdFile.Config.EnvironmentNames()...)
	require.NoError(t, applyResolvedComponentConfig(cicdFile, ""))
	require.Equal(t, before, cicdFile.Config.EnvironmentNames(), "empty component must not narrow the ladder")
	require.Equal(t, []string{"dev", "staging", "prod"}, cicdFile.Config.EnvironmentNames())
}

// TestApplyComponentLadder_NarrowsToComponentSubset proves the helper narrows the
// working config's ladder to the addressed component's resolved subset.
func TestApplyComponentLadder_NarrowsToComponentSubset(t *testing.T) {
	path := writeSubsetManifest(t)
	cicdFile, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	require.NoError(t, err)

	require.NoError(t, applyResolvedComponentConfig(cicdFile, "api"))
	require.Equal(t, []string{"dev", "staging"}, cicdFile.Config.EnvironmentNames())
}

package promote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// publishedComponentManifest seeds recorded release markers for two components
// the way real publish history leaves them: api has released before, and web's
// marker carries an unmodeled key so verbatim preservation is provable.
const publishedComponentManifest = `ci:
  config:
    environments: [dev, prod]
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
          sha: apirel2
          version: api-v1.1.0-rc.0
        prod:
          sha: apirel1
          version: api-v1.0.0
  latest_release:
    components:
      api:
        version: api-v1.0.0
        sha: apirel1
        released_on: "2026-01-01T00:00:00Z"
      web:
        version: web-v2.0.0
        sha: webrel1
        custom_marker: keep-me-verbatim
`

// latestComponentsNode digs ci.latest_release.components out of a parsed
// manifest tree, failing the test when any level is missing.
func latestComponentsNode(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	ci, ok := m["ci"].(map[string]any)
	require.True(t, ok, "ci block present")
	latest, ok := ci["latest_release"].(map[string]any)
	require.True(t, ok, "ci.latest_release present")
	comps, ok := latest["components"].(map[string]any)
	require.True(t, ok, "ci.latest_release.components present")
	return comps
}

// TestFinalizer_ComponentPublish_WritesOwnMarkerNotSharedRecord is the
// regression test for the component publish marker leak: the finalizer's
// latest_release directive carried the whole shared record, so every publish
// after the repo's first nested a stale snapshot of ALL components' markers
// (its own prior one included) under latest_release.components.<component>.
// The leaf must hold exactly the publishing component's own marker.
func TestFinalizer_ComponentPublish_WritesOwnMarkerNotSharedRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(publishedComponentManifest), 0644))

	fin, err := NewFinalizer(path, "prod", WithComponent("api"), WithClock(fixedClock()))
	require.NoError(t, err)
	fin.SetActor("octocat")
	fin.SetPromotionResult(&PromotionResult{
		Promotions:    []EnvPromotion{{Environment: "prod", SourceEnv: "dev", SHA: "apirel2", Version: "api-v1.1.0"}},
		ReleaseAction: "publish",
		ReleaseData:   &ReleaseData{SHA: "apirel2", RCVersion: "api-v1.1.0-rc.0", SemVersion: "api-v1.1.0"},
	})

	require.NoError(t, fin.Run())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	comps := latestComponentsNode(t, readManifestNode(t, out))

	api, ok := comps["api"].(map[string]any)
	require.True(t, ok, "latest_release.components.api present")

	// The leaf is the component's own marker: this publish's version/sha plus
	// the audit timestamp, and nothing else.
	require.Equal(t, "api-v1.1.0", api["version"])
	require.Equal(t, "apirel2", api["sha"])
	require.Equal(t, "2026-07-08T12:00:00Z", api["released_on"])

	// The corruption shape: the shared record's per-component map (stale self +
	// sibling markers) must never nest under the component's own leaf.
	_, nested := api["components"]
	require.False(t, nested,
		"publish nested the shared latest_release record (all components' markers) under api's leaf:\n%s", out)
	require.Len(t, api, 3, "api leaf must hold exactly version/sha/released_on, got: %#v", api)

	// The sibling's marker survives verbatim, unmodeled key included.
	web, ok := comps["web"].(map[string]any)
	require.True(t, ok, "sibling web marker present")
	require.Equal(t, "web-v2.0.0", web["version"])
	require.Equal(t, "webrel1", web["sha"])
	require.Contains(t, string(out), "custom_marker: keep-me-verbatim")
}

// TestFinalizer_ComponentPublish_MarkerRoundTripsToChangelogRead proves the
// write shape is exactly the read shape: the marker a component publish
// records must resolve through the typed path
// calculateComponentChangelogRefs consumes (tier 2,
// LatestRelease.Components[component].SHA/Version on a fresh parse).
func TestFinalizer_ComponentPublish_MarkerRoundTripsToChangelogRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(publishedComponentManifest), 0644))

	fin, err := NewFinalizer(path, "prod", WithComponent("api"), WithClock(fixedClock()))
	require.NoError(t, err)
	fin.SetActor("octocat")
	fin.SetPromotionResult(&PromotionResult{
		Promotions:    []EnvPromotion{{Environment: "prod", SourceEnv: "dev", SHA: "apirel2", Version: "api-v1.1.0"}},
		ReleaseAction: "publish",
		ReleaseData:   &ReleaseData{SHA: "apirel2", RCVersion: "api-v1.1.0-rc.0", SemVersion: "api-v1.1.0"},
	})
	require.NoError(t, fin.Run())

	reparsed, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	require.NoError(t, err)
	require.NotNil(t, reparsed.LatestRelease, "latest_release must parse back")

	marker := reparsed.LatestRelease.Components["api"]
	require.NotNil(t, marker, "the component's recorded marker must resolve on the typed read")
	require.Equal(t, "apirel2", marker.SHA, "changelog tier-2 read must see this publish's sha")
	require.Equal(t, "api-v1.1.0", marker.Version, "changelog tier-2 read must see this publish's version")
	require.Equal(t, "2026-07-08T12:00:00Z", marker.ReleasedOn)

	// The sibling's typed marker is untouched.
	sibling := reparsed.LatestRelease.Components["web"]
	require.NotNil(t, sibling)
	require.Equal(t, "webrel1", sibling.SHA)
	require.Equal(t, "web-v2.0.0", sibling.Version)
}

// TestPromoter_ComponentSave_DoesNotTouchLatestRelease covers the simulate
// (non-dry-run promote) save: promotion never mutates latest_release, and the
// component form node-patches only addressed leaves, so the save must leave
// latest_release byte-verbatim. Before the fix it wrote the parsed shared
// record whole under the promoting component's leaf, synthesizing a phantom
// marker (carrying the sibling's marker nested inside it) for a component that
// has never released.
func TestPromoter_ComponentSave_DoesNotTouchLatestRelease(t *testing.T) {
	const manifest = `ci:
  config:
    environments: [dev, prod]
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
          sha: apidev1
          version: api-v0.1.0-rc.0
  latest_release:
    components:
      web:
        version: web-v2.0.0
        sha: webrel1
        custom_marker: keep-me-verbatim
`
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0644))

	promoter, err := NewPromoter(PromoterOptions{
		ConfigPath: path,
		DryRun:     false,
		Actor:      "promoter",
		Component:  "api",
	})
	require.NoError(t, err)

	result, err := promoter.Promote(ModeDefault, "")
	require.NoError(t, err)
	require.True(t, result.Success, "promote failed: %s", result.Error)

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	comps := latestComponentsNode(t, readManifestNode(t, out))

	// api has never released: the save must not synthesize a marker for it.
	_, phantom := comps["api"]
	require.False(t, phantom,
		"promotion save synthesized a phantom latest_release marker for a component that never released:\n%s", out)

	// The sibling's recorded marker survives verbatim.
	web, ok := comps["web"].(map[string]any)
	require.True(t, ok, "web marker must survive an api promotion save")
	require.Equal(t, "web-v2.0.0", web["version"])
	require.Equal(t, "webrel1", web["sha"])
	require.True(t, strings.Contains(string(out), "custom_marker: keep-me-verbatim"),
		"web's unmodeled marker key must survive verbatim:\n%s", out)
}

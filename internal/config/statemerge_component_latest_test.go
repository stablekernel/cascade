package config

import (
	"reflect"
	"strings"
	"testing"
)

// componentLatestManifest carries recorded release markers for two components.
// Component web's marker holds an unmodeled key (custom_marker) the binary's
// typed shapes do not model, so preservation is provable, not incidental.
const componentLatestManifest = `ci:
  config:
    trunk_branch: main
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-v
      web:
        path: services/web
        tag_grammar:
          prefix: web-v
  latest_release:
    components:
      api:
        version: api-v1.0.0
        sha: apirel1
      web:
        version: web-v2.0.0
        sha: webrel1
        custom_marker: keep-me-verbatim
`

// latestComponents digs latest_release.components out of written manifest
// bytes, or nil when the container is absent.
func latestComponents(t *testing.T, data []byte) map[string]any {
	t.Helper()
	top := parseTop(t, data)
	section, ok := top["ci"].(map[string]any)
	if !ok {
		t.Fatalf("manifest key ci missing:\n%s", data)
	}
	latest, ok := section["latest_release"].(map[string]any)
	if !ok {
		return nil
	}
	comps, _ := latest["components"].(map[string]any)
	return comps
}

// TestWriteScopedState_ComponentLatestSetWritesOwnLeafOnly drives the
// production shape of a component publish (promote.go serializeState /
// finalize.go componentStateWrites): a StateWrite{Component, Latest} must land
// under latest_release.components.<component>, leave the sibling component's
// marker (including its unmodeled key) verbatim, and never surface as a flat
// top-level latest_release version/sha.
func TestWriteScopedState_ComponentLatestSetWritesOwnLeafOnly(t *testing.T) {
	got, err := WriteScopedState([]byte(componentLatestManifest), "ci",
		StateWrite{Component: "api", Latest: &LatestReleaseState{
			Version:    "api-v1.1.0",
			SHA:        "apirel2",
			ReleasedOn: "2026-01-01T00:00:00Z",
			ReleasedBy: "octocat",
		}})
	if err != nil {
		t.Fatalf("WriteScopedState: %v", err)
	}

	comps := latestComponents(t, got)
	if comps == nil {
		t.Fatalf("latest_release.components missing after component Latest set:\n%s", got)
	}

	api, ok := comps["api"].(map[string]any)
	if !ok {
		t.Fatalf("latest_release.components.api missing:\n%s", got)
	}
	if api["version"] != "api-v1.1.0" || api["sha"] != "apirel2" {
		t.Errorf("api marker = %v, want version api-v1.1.0 / sha apirel2", api)
	}
	if api["released_on"] != "2026-01-01T00:00:00Z" {
		t.Errorf("api marker metadata = %v, want released_on carried through", api)
	}
	// released_by is a top-level-only field of the shared record; the
	// ComponentReleaseState leaf shape does not carry it.
	if _, has := api["released_by"]; has {
		t.Errorf("api marker carries top-level-only released_by: %v", api)
	}

	// The sibling's marker survives verbatim, unmodeled key included.
	web, ok := comps["web"].(map[string]any)
	if !ok {
		t.Fatalf("sibling latest_release.components.web dropped:\n%s", got)
	}
	if web["version"] != "web-v2.0.0" || web["sha"] != "webrel1" {
		t.Errorf("web marker = %v, want verbatim web-v2.0.0 / webrel1", web)
	}
	if !strings.Contains(string(got), "custom_marker: keep-me-verbatim") {
		t.Errorf("sibling's unmodeled marker key was dropped:\n%s", got)
	}

	// The write must stay component-scoped: no flat version/sha directly under
	// latest_release.
	top := parseTop(t, got)
	latest := top["ci"].(map[string]any)["latest_release"].(map[string]any)
	if _, flat := latest["version"]; flat {
		t.Errorf("component Latest set leaked a flat latest_release.version:\n%s", got)
	}
	if _, flat := latest["sha"]; flat {
		t.Errorf("component Latest set leaked a flat latest_release.sha:\n%s", got)
	}
}

// TestWriteScopedState_ComponentLatestLeafIsComponentShaped drives the exact
// directive shape production hands over: the finalizer passes the SHARED
// LatestReleaseState, whose flat fields hold this publish and whose Components
// map holds every component's recorded marker. The written leaf must be the
// component's OWN marker only (the ComponentReleaseState shape the typed reader
// consumes: version/sha/released_on). It must never nest a components map, a
// sibling's marker, or the top-level-only released_by under the leaf.
func TestWriteScopedState_ComponentLatestLeafIsComponentShaped(t *testing.T) {
	got, err := WriteScopedState([]byte(componentLatestManifest), "ci",
		StateWrite{Component: "api", Latest: &LatestReleaseState{
			Version:    "api-v1.1.0",
			SHA:        "apirel2",
			ReleasedOn: "2026-01-01T00:00:00Z",
			ReleasedBy: "octocat",
			Components: map[string]*ComponentReleaseState{
				"api": {Version: "api-v1.0.0", SHA: "apirel1"},
				"web": {Version: "web-v2.0.0", SHA: "webrel1"},
			},
		}})
	if err != nil {
		t.Fatalf("WriteScopedState: %v", err)
	}

	comps := latestComponents(t, got)
	api, ok := comps["api"].(map[string]any)
	if !ok {
		t.Fatalf("latest_release.components.api missing:\n%s", got)
	}
	if _, nested := api["components"]; nested {
		t.Errorf("component leaf nests a components map (shared record written whole):\n%s", got)
	}
	want := map[string]any{
		"version":     "api-v1.1.0",
		"sha":         "apirel2",
		"released_on": "2026-01-01T00:00:00Z",
	}
	if !reflect.DeepEqual(api, want) {
		t.Errorf("api leaf = %v, want exactly the component marker %v", api, want)
	}

	// The sibling's own top-level marker is untouched.
	web, ok := comps["web"].(map[string]any)
	if !ok {
		t.Fatalf("sibling latest_release.components.web dropped:\n%s", got)
	}
	if web["version"] != "web-v2.0.0" || web["sha"] != "webrel1" {
		t.Errorf("web marker = %v, want verbatim web-v2.0.0 / webrel1", web)
	}
}

// TestWriteScopedState_ComponentLatestSetSynthesizesContainer covers the first
// publish of a component on a manifest with no latest_release node at all: the
// latest_release.components.<component> path is created.
func TestWriteScopedState_ComponentLatestSetSynthesizesContainer(t *testing.T) {
	base := `ci:
  config:
    trunk_branch: main
`
	got, err := WriteScopedState([]byte(base), "ci",
		StateWrite{Component: "api", Latest: &LatestReleaseState{Version: "api-v1.0.0", SHA: "apirel1"}})
	if err != nil {
		t.Fatalf("WriteScopedState: %v", err)
	}
	comps := latestComponents(t, got)
	api, ok := comps["api"].(map[string]any)
	if !ok {
		t.Fatalf("latest_release.components.api not synthesized:\n%s", got)
	}
	if api["version"] != "api-v1.0.0" || api["sha"] != "apirel1" {
		t.Errorf("api marker = %v, want api-v1.0.0 / apirel1", api)
	}
}

// TestWriteScopedState_ComponentLatestDeleteRemovesOwnLeafOnly drives the
// delete branch: Latest == nil removes exactly the addressed component's
// marker; the sibling's marker and its unmodeled key survive.
func TestWriteScopedState_ComponentLatestDeleteRemovesOwnLeafOnly(t *testing.T) {
	got, err := WriteScopedState([]byte(componentLatestManifest), "ci",
		StateWrite{Component: "api", Latest: nil})
	if err != nil {
		t.Fatalf("WriteScopedState: %v", err)
	}

	comps := latestComponents(t, got)
	if comps == nil {
		t.Fatalf("latest_release.components dropped entirely; only api's leaf may go:\n%s", got)
	}
	if _, still := comps["api"]; still {
		t.Errorf("latest_release.components.api survived its delete:\n%s", got)
	}
	web, ok := comps["web"].(map[string]any)
	if !ok {
		t.Fatalf("sibling web marker deleted alongside api:\n%s", got)
	}
	if web["sha"] != "webrel1" {
		t.Errorf("web marker sha = %v, want verbatim webrel1", web["sha"])
	}
	if !strings.Contains(string(got), "custom_marker: keep-me-verbatim") {
		t.Errorf("sibling's unmodeled marker key was dropped on api's delete:\n%s", got)
	}
}

// TestWriteScopedState_ComponentLatestDeleteLastLeafCollapses deletes the only
// recorded marker and asserts the emptied components mapping and the
// latest_release container both collapse, so the manifest never carries a
// dangling latest_release: {}.
func TestWriteScopedState_ComponentLatestDeleteLastLeafCollapses(t *testing.T) {
	base := `ci:
  config:
    trunk_branch: main
  latest_release:
    components:
      api:
        version: api-v1.0.0
        sha: apirel1
`
	got, err := WriteScopedState([]byte(base), "ci",
		StateWrite{Component: "api", Latest: nil})
	if err != nil {
		t.Fatalf("WriteScopedState: %v", err)
	}
	top := parseTop(t, got)
	section := top["ci"].(map[string]any)
	if _, still := section["latest_release"]; still {
		t.Errorf("emptied latest_release container must collapse:\n%s", got)
	}
}

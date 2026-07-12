package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// referenceWholeNodeReplace reproduces the pre-scoped-serializer WriteManifestState
// algorithm verbatim: it replaces the whole `state` node with a fresh marshal of
// the typed map (sorted keys, delete-by-omission) and the `latest_release` node
// with the typed release (nil deletes). The scoped wrapper must match its bytes
// exactly for every single-component case, so this is the byte-identity oracle.
func referenceWholeNodeReplace(t *testing.T, current []byte, manifestKey string, state map[string]*EnvState, latest *LatestReleaseState) []byte {
	t.Helper()
	if manifestKey == "" {
		manifestKey = DefaultManifestKey
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		t.Fatalf("reference unmarshal: %v", err)
	}
	root := documentMapping(&doc)
	if root == nil {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	}
	section := mappingValue(root, manifestKey)
	if section == nil || section.Kind != yaml.MappingNode {
		section = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMappingValue(root, manifestKey, section)
	}
	if len(state) == 0 {
		deleteMappingKey(section, "state")
	} else {
		node, err := valueNode(state)
		if err != nil {
			t.Fatalf("reference state node: %v", err)
		}
		setMappingValue(section, "state", node)
	}
	if latest == nil {
		deleteMappingKey(section, "latest_release")
	} else {
		node, err := valueNode(latest)
		if err != nil {
			t.Fatalf("reference latest node: %v", err)
		}
		setMappingValue(section, "latest_release", node)
	}
	data, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("reference marshal: %v", err)
	}
	return data
}

// publishManifest carries a prerelease marker and several envs in sorted order,
// as a prior single-component write would have produced.
const publishManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - staging
      - prod
  state:
    dev:
      sha: devsha
      version: v1.2.0
    prerelease:
      sha: rcsha
      version: v1.2.0-rc.1
    prod:
      sha: prodsha
      version: v1.1.0
    staging:
      sha: stagingsha
      version: v1.2.0
`

// TestWriteManifestState_PublishDropsPrereleaseByteIdentical is the load-bearing
// delete-aware golden. The publish transition sets state["release"] and deletes
// state["prerelease"] from the typed map (mirroring overlayOwnedState). Fed bytes
// that still carry a prerelease node, the wrapper must drop prerelease AND match
// the whole-state-node-replace oracle byte for byte. A naive patch-only wrapper
// (patch present keys, never delete) would leave the stale prerelease node and
// fail this test.
func TestWriteManifestState_PublishDropsPrereleaseByteIdentical(t *testing.T) {
	// The overlay result: prerelease deleted, release added, envs retained.
	final := map[string]*EnvState{
		"dev":     {SHA: "devsha", Version: "v1.2.0"},
		"staging": {SHA: "stagingsha", Version: "v1.2.0"},
		"prod":    {SHA: "prodsha", Version: "v1.1.0"},
		"release": {SHA: "rcsha", Version: "v1.2.0"},
	}
	latest := &LatestReleaseState{Version: "v1.2.0", SHA: "rcsha", ReleasedOn: "2026-01-01T00:00:00Z"}

	got, err := WriteManifestState([]byte(publishManifest), "ci", final, latest)
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}

	if strings.Contains(string(got), "prerelease") {
		t.Fatalf("publish transition left a stale prerelease node:\n%s", got)
	}

	want := referenceWholeNodeReplace(t, []byte(publishManifest), "ci", final, latest)
	if string(got) != string(want) {
		t.Fatalf("publish output not byte-identical to whole-node-replace oracle\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestWriteManifestState_ByteIdenticalAcrossCases pins the wrapper to the
// whole-node-replace oracle across the ordinary single-component cases: a normal
// env update, the empty-state reset (state key removed), and a latest_release
// write. These lock byte-identity beyond the publish-delete path.
func TestWriteManifestState_ByteIdenticalAcrossCases(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		key    string
		state  map[string]*EnvState
		latest *LatestReleaseState
	}{
		{
			name:  "env update",
			src:   publishManifest,
			key:   "ci",
			state: map[string]*EnvState{"dev": {SHA: "newsha", Version: "v2.0.0"}},
		},
		{
			name:  "empty state reset removes key",
			src:   publishManifest,
			key:   "ci",
			state: nil,
		},
		{
			name:   "write latest release",
			src:    publishManifest,
			key:    "ci",
			state:  map[string]*EnvState{"prod": {SHA: "s"}},
			latest: &LatestReleaseState{Version: "v3.0.0", SHA: "rsha"},
		},
		{
			name:   "new env inserted out of order",
			src:    publishManifest,
			key:    "ci",
			state:  map[string]*EnvState{"aaa": {SHA: "a"}, "zzz": {SHA: "z"}, "dev": {SHA: "d"}},
			latest: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WriteManifestState([]byte(tc.src), tc.key, tc.state, tc.latest)
			if err != nil {
				t.Fatalf("WriteManifestState: %v", err)
			}
			want := referenceWholeNodeReplace(t, []byte(tc.src), tc.key, tc.state, tc.latest)
			if string(got) != string(want) {
				t.Fatalf("not byte-identical\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// multiComponentManifest carries two components under state.components. Component
// B's staging leaf holds an unmodeled key (custom_leaf_marker) that the binary's
// typed EnvState does not model.
const multiComponentManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - staging
      - prod
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
        staging:
          sha: apistaging
          version: api-v1.0.0
      web:
        staging:
          sha: webstaging
          version: web-v2.0.0
          custom_leaf_marker: keep-me-verbatim
`

// TestWriteScopedState_PreservesSiblingComponentUnmodeledKey is the #389-class
// regression at component depth: a scoped write for (api, staging) must leave
// component web's subtree, including the unmodeled custom_leaf_marker key, present
// byte for byte. The serializer must never re-marshal state.components from a
// typed map.
func TestWriteScopedState_PreservesSiblingComponentUnmodeledKey(t *testing.T) {
	got, err := WriteScopedState([]byte(multiComponentManifest), "ci",
		StateWrite{Component: "api", Env: "staging", State: &EnvState{SHA: "apistaging-new", Version: "api-v1.1.0"}})
	if err != nil {
		t.Fatalf("WriteScopedState: %v", err)
	}

	if !strings.Contains(string(got), "custom_leaf_marker: keep-me-verbatim") {
		t.Fatalf("sibling component web's unmodeled key was dropped:\n%s", got)
	}

	top := parseTop(t, got)
	state := top["ci"].(map[string]any)["state"].(map[string]any)
	comps := state["components"].(map[string]any)
	web := comps["web"].(map[string]any)["staging"].(map[string]any)
	if web["custom_leaf_marker"] != "keep-me-verbatim" {
		t.Errorf("web.staging.custom_leaf_marker = %v, want keep-me-verbatim", web["custom_leaf_marker"])
	}
	if web["sha"] != "webstaging" {
		t.Errorf("web.staging.sha = %v, want webstaging (verbatim)", web["sha"])
	}
	api := comps["api"].(map[string]any)["staging"].(map[string]any)
	if api["sha"] != "apistaging-new" {
		t.Errorf("api.staging.sha = %v, want apistaging-new", api["sha"])
	}
}

// TestWriteScopedState_MultiComponentRoundTripStable writes several
// (component, env) leaves and asserts re-applying the identical writes produces
// stable bytes (idempotent), and that a fresh write is order-preserving.
func TestWriteScopedState_MultiComponentRoundTripStable(t *testing.T) {
	base := `ci:
  config:
    trunk_branch: main
  state:
    components:
      api:
        staging:
          sha: s1
`
	writes := []StateWrite{
		{Component: "api", Env: "prod", State: &EnvState{SHA: "p1", Version: "api-v1.0.0"}},
		{Component: "web", Env: "staging", State: &EnvState{SHA: "w1"}},
	}
	first, err := WriteScopedState([]byte(base), "ci", writes...)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	second, err := WriteScopedState(first, "ci", writes...)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("re-applying identical writes is not idempotent\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}

	top := parseTop(t, first)
	comps := top["ci"].(map[string]any)["state"].(map[string]any)["components"].(map[string]any)
	if comps["api"].(map[string]any)["staging"].(map[string]any)["sha"] != "s1" {
		t.Errorf("api.staging.sha not preserved: %#v", comps["api"])
	}
	if comps["api"].(map[string]any)["prod"].(map[string]any)["sha"] != "p1" {
		t.Errorf("api.prod.sha not written: %#v", comps["api"])
	}
	if comps["web"].(map[string]any)["staging"].(map[string]any)["sha"] != "w1" {
		t.Errorf("web.staging.sha not written: %#v", comps["web"])
	}
}

// TestWriteScopedState_EmptyCollapse asserts that deleting the last env of a
// component removes components.<name>, and deleting the last component removes the
// components mapping and then the state key, so a manifest returning to no
// component state collapses cleanly.
func TestWriteScopedState_EmptyCollapse(t *testing.T) {
	// Delete web's only env: web disappears, api survives.
	afterWeb, err := WriteScopedState([]byte(multiComponentManifest), "ci",
		StateWrite{Component: "web", Env: "staging", State: nil})
	if err != nil {
		t.Fatalf("delete web.staging: %v", err)
	}
	top := parseTop(t, afterWeb)
	comps := top["ci"].(map[string]any)["state"].(map[string]any)["components"].(map[string]any)
	if _, ok := comps["web"]; ok {
		t.Errorf("empty component web should have collapsed: %#v", comps)
	}
	if _, ok := comps["api"]; !ok {
		t.Errorf("api must survive web deletion: %#v", comps)
	}

	// Now delete api's only env too: components and state collapse away entirely.
	afterAll, err := WriteScopedState(afterWeb, "ci",
		StateWrite{Component: "api", Env: "staging", State: nil})
	if err != nil {
		t.Fatalf("delete api.staging: %v", err)
	}
	top2 := parseTop(t, afterAll)
	ci := top2["ci"].(map[string]any)
	if _, ok := ci["state"]; ok {
		t.Errorf("state key should collapse when no component state remains: %#v", ci["state"])
	}
	// config must survive the collapse.
	if _, ok := ci["config"]; !ok {
		t.Error("config dropped during collapse")
	}
}

// TestWriteScopedState_RejectsMixedForms guards decision 7.1: a single manifest
// never carries both single-component and component-scoped state, so a call that
// mixes the forms is a programming error and must be rejected rather than silently
// dropping the whole state node.
func TestWriteScopedState_RejectsMixedForms(t *testing.T) {
	_, err := WriteScopedState([]byte(publishManifest), "ci",
		StateWrite{Env: "dev", State: &EnvState{SHA: "x"}},
		StateWrite{Component: "api", Env: "staging", State: &EnvState{SHA: "y"}})
	if err == nil {
		t.Fatal("expected an error when mixing single-component and component-scoped writes")
	}
}

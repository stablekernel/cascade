package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// manifestWithUnmodeledKeys is a manifest that carries keys the CICDFile struct
// does not model: a sibling under config (config.future_knob), a top-level key
// outside the manifest key (custom_top_level), and a nested unmodeled block. A
// state write must preserve all of them.
const manifestWithUnmodeledKeys = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    future_knob:
      enabled: true
      nested:
        - a
        - b
  state:
    dev:
      sha: oldsha
      version: v1.0.0
custom_top_level:
  retained: true
`

func parseTop(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	return m
}

func TestWriteManifestState_PreservesUnmodeledKeys(t *testing.T) {
	state := map[string]*EnvState{
		"prod": {SHA: "newsha", Version: "v1.1.0"},
	}

	out, err := WriteManifestState([]byte(manifestWithUnmodeledKeys), "ci", state, nil)
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}

	top := parseTop(t, out)

	// The unmodeled top-level key must survive.
	custom, ok := top["custom_top_level"].(map[string]any)
	if !ok {
		t.Fatalf("custom_top_level dropped or wrong type: %#v", top["custom_top_level"])
	}
	if custom["retained"] != true {
		t.Errorf("custom_top_level.retained = %v, want true", custom["retained"])
	}

	ci, ok := top["ci"].(map[string]any)
	if !ok {
		t.Fatalf("ci key missing: %#v", top)
	}
	cfg, ok := ci["config"].(map[string]any)
	if !ok {
		t.Fatalf("ci.config missing: %#v", ci)
	}

	// The unmodeled config-level key (mirrors how drift_check would look to an
	// older binary) must survive.
	knob, ok := cfg["future_knob"].(map[string]any)
	if !ok {
		t.Fatalf("ci.config.future_knob dropped: %#v", cfg)
	}
	if knob["enabled"] != true {
		t.Errorf("future_knob.enabled = %v, want true", knob["enabled"])
	}
	if nested, ok := knob["nested"].([]any); !ok || len(nested) != 2 {
		t.Errorf("future_knob.nested not preserved: %#v", knob["nested"])
	}

	// Modeled config still present.
	if cfg["trunk_branch"] != "main" {
		t.Errorf("config.trunk_branch = %v, want main", cfg["trunk_branch"])
	}

	// The new state must be written and replace the old.
	st, ok := ci["state"].(map[string]any)
	if !ok {
		t.Fatalf("ci.state missing: %#v", ci)
	}
	prod, ok := st["prod"].(map[string]any)
	if !ok {
		t.Fatalf("state.prod missing: %#v", st)
	}
	if prod["sha"] != "newsha" {
		t.Errorf("state.prod.sha = %v, want newsha", prod["sha"])
	}
	if _, stale := st["dev"]; stale {
		t.Errorf("stale state.dev should have been replaced: %#v", st)
	}
}

func TestWriteManifestState_EmptyStateRemovesKeyButKeepsConfig(t *testing.T) {
	// reset clears state by passing a nil/empty map and nil latest release. The
	// state and latest_release keys must disappear while config and unmodeled
	// keys survive.
	out, err := WriteManifestState([]byte(manifestWithUnmodeledKeys), "ci", nil, nil)
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}

	top := parseTop(t, out)
	ci := top["ci"].(map[string]any)
	if _, ok := ci["state"]; ok {
		t.Errorf("state key should be removed on empty state, got: %#v", ci["state"])
	}
	if _, ok := ci["custom_top_level"]; ok {
		t.Error("unexpected key promotion")
	}
	if _, ok := top["custom_top_level"]; !ok {
		t.Error("custom_top_level dropped on reset write")
	}
	cfg := ci["config"].(map[string]any)
	if _, ok := cfg["future_knob"]; !ok {
		t.Error("config.future_knob dropped on reset write")
	}
}

func TestWriteManifestState_WritesLatestRelease(t *testing.T) {
	out, err := WriteManifestState([]byte(manifestWithUnmodeledKeys), "ci",
		map[string]*EnvState{"prod": {SHA: "s"}},
		&LatestReleaseState{Version: "v2.0.0", SHA: "rsha"})
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}
	if !strings.Contains(string(out), "latest_release") {
		t.Fatalf("latest_release not written: %s", out)
	}
	ci := parseTop(t, out)["ci"].(map[string]any)
	lr, ok := ci["latest_release"].(map[string]any)
	if !ok {
		t.Fatalf("latest_release missing: %#v", ci)
	}
	if lr["version"] != "v2.0.0" {
		t.Errorf("latest_release.version = %v, want v2.0.0", lr["version"])
	}
}

func TestWriteManifestState_CustomManifestKey(t *testing.T) {
	src := `pipeline:
  config:
    trunk_branch: main
    unknown_field: keep-me
  state:
    dev:
      sha: old
`
	out, err := WriteManifestState([]byte(src), "pipeline",
		map[string]*EnvState{"dev": {SHA: "new"}}, nil)
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}
	pipeline := parseTop(t, out)["pipeline"].(map[string]any)
	cfg := pipeline["config"].(map[string]any)
	if cfg["unknown_field"] != "keep-me" {
		t.Errorf("unknown_field dropped under custom key: %#v", cfg)
	}
	st := pipeline["state"].(map[string]any)
	if st["dev"].(map[string]any)["sha"] != "new" {
		t.Errorf("state.dev.sha not updated: %#v", st)
	}
}

package config

import (
	"strings"
	"testing"
)

// TestWriteScopedState_SingleComponentFormByteIdentical drives the
// single-component (Component == "") branch of WriteScopedState directly, so the
// call lands in applySingleComponentWrites. Every production single-component
// writer reaches that branch only through the WriteManifestState wrapper, so the
// wrapper goldens exercise a related but distinct entry point; a regression in the
// direct WriteScopedState single-component path ships green against them.
//
// The test pins two invariants: the emitted state keeps the flat state.<env>
// shape with no components.<name> nesting (the pre-multi-component leaf), and the
// bytes are identical both to the WriteManifestState wrapper for the equivalent
// map and to the historical whole-node-replace oracle.
func TestWriteScopedState_SingleComponentFormByteIdentical(t *testing.T) {
	latest := &LatestReleaseState{Version: "v1.2.0", SHA: "rcsha", ReleasedOn: "2026-01-01T00:00:00Z"}

	// Drive WriteScopedState directly with single-component (Component == "")
	// writes. publishManifest still carries a prerelease node; the single-component
	// rebuild drops it by omission, matching the wrapper.
	got, err := WriteScopedState([]byte(publishManifest), "ci",
		StateWrite{Env: "dev", State: &EnvState{SHA: "devsha", Version: "v1.2.0"}},
		StateWrite{Env: "staging", State: &EnvState{SHA: "stagingsha", Version: "v1.2.0"}},
		StateWrite{Env: "prod", State: &EnvState{SHA: "prodsha", Version: "v1.1.0"}},
		StateWrite{Env: "release", State: &EnvState{SHA: "rcsha", Version: "v1.2.0"}},
		StateWrite{Latest: latest},
	)
	if err != nil {
		t.Fatalf("WriteScopedState single-component: %v", err)
	}

	// The single-component write must keep the flat state.<env> shape: no
	// components.<name> nesting ever appears on this path.
	if strings.Contains(string(got), "components:") {
		t.Fatalf("single-component write leaked component nesting:\n%s", got)
	}
	for _, env := range []string{"dev:", "staging:", "prod:", "release:"} {
		if !strings.Contains(string(got), env) {
			t.Fatalf("single-component write dropped the flat %s leaf:\n%s", env, got)
		}
	}
	if strings.Contains(string(got), "prerelease") {
		t.Fatalf("single-component rebuild left a stale prerelease node:\n%s", got)
	}

	// Byte-identity against the WriteManifestState wrapper for the equivalent map:
	// the direct single-component write and the wrapper must converge on the same
	// bytes.
	final := map[string]*EnvState{
		"dev":     {SHA: "devsha", Version: "v1.2.0"},
		"staging": {SHA: "stagingsha", Version: "v1.2.0"},
		"prod":    {SHA: "prodsha", Version: "v1.1.0"},
		"release": {SHA: "rcsha", Version: "v1.2.0"},
	}
	want, err := WriteManifestState([]byte(publishManifest), "ci", final, latest)
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("single-component WriteScopedState not byte-identical to WriteManifestState\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// And identical to the historical whole-node-replace oracle, pinning the flat
	// shape to the pre-multi-component byte output.
	oracle := referenceWholeNodeReplace(t, []byte(publishManifest), "ci", final, latest)
	if string(got) != string(oracle) {
		t.Fatalf("single-component WriteScopedState not byte-identical to whole-node-replace oracle\n--- got ---\n%s\n--- want ---\n%s", got, oracle)
	}
}

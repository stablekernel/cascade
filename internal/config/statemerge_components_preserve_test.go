package config

import (
	"strings"
	"testing"
)

// componentStateManifest is a component-scoped manifest carrying recorded state
// under state.components.<name>.<env>. Parsing it into the flat CICDFile.State
// map (map[string]*EnvState) lifts state.components into that map as a bogus,
// empty EnvState keyed "components".
const componentStateManifest = `ci:
  config:
    schema_version: 1
    trunk_branch: main
    components:
      api:
        environments:
          - name: staging
          - name: prod
  state:
    components:
      api:
        staging:
          sha: aaa111
          version: v1.0.0
        prod:
          sha: bbb222
          version: v0.9.0
`

// TestWriteManifestState_FlatWriteOnComponentManifest_PreservesComponents drives
// the real production flat-write path (ParseManifestBytes then WriteManifestState,
// exactly what a finalize invoked without --component runs) against a manifest
// that carries per-component state. Before the fix, the flat rebuild replaced the
// whole state node from the typed map, whose bogus "components" EnvState collapsed
// state.components to {} and silently destroyed every recorded component row while
// the write returned success. The write must now preserve the subtree verbatim.
func TestWriteManifestState_FlatWriteOnComponentManifest_PreservesComponents(t *testing.T) {
	file, err := ParseManifestBytes([]byte(componentStateManifest), "ci")
	if err != nil {
		t.Fatalf("ParseManifestBytes: %v", err)
	}
	// The flat parse lifts state.components into State as a bogus "components" key.
	if _, lifted := file.State["components"]; !lifted {
		t.Fatalf("precondition: expected flat parse to lift a bogus 'components' key, got keys %v", keysOfState(file.State))
	}

	got, err := WriteManifestState([]byte(componentStateManifest), "ci", file.State, file.LatestRelease)
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}
	out := string(got)

	for _, want := range []string{"aaa111", "bbb222", "v1.0.0", "v0.9.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("flat write destroyed component state (missing %q):\n%s", want, out)
		}
	}
	if strings.Contains(out, "components: {}") {
		t.Errorf("flat write collapsed state.components to an empty mapping:\n%s", out)
	}
}

// TestWriteManifestState_SingleComponentManifest_ByteIdentical pins that a genuine
// single-component manifest (no components: subtree) round-trips byte-identically
// through the fixed flat path, so the reserved-key handling adds nothing when no
// components subtree exists.
func TestWriteManifestState_SingleComponentManifest_ByteIdentical(t *testing.T) {
	const flat = `ci:
  config:
    schema_version: 1
    trunk_branch: main
    environments:
      - name: staging
      - name: prod
  state:
    staging:
      sha: aaa111
      version: v1.0.0
    prod:
      sha: bbb222
      version: v0.9.0
`
	final := map[string]*EnvState{
		"staging": {SHA: "aaa111", Version: "v1.0.0"},
		"prod":    {SHA: "bbb222", Version: "v0.9.0"},
	}
	got, err := WriteManifestState([]byte(flat), "ci", final, nil)
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}
	oracle := referenceWholeNodeReplace(t, []byte(flat), "ci", final, nil)
	if string(got) != string(oracle) {
		t.Fatalf("single-component flat write drifted from whole-node-replace oracle\n--- got ---\n%s\n--- want ---\n%s", got, oracle)
	}
	if strings.Contains(string(got), "components") {
		t.Fatalf("single-component write leaked a components key:\n%s", got)
	}
}

// TestWriteScopedState_ComponentWrite_PreservesSiblingComponents pins the
// #614-class invariant on the component-scoped path: writing one component's env
// leaf must not disturb a sibling component's recorded rows.
func TestWriteScopedState_ComponentWrite_PreservesSiblingComponents(t *testing.T) {
	const twoComponents = `ci:
  config:
    schema_version: 1
    trunk_branch: main
  state:
    components:
      api:
        staging:
          sha: aaa111
      web:
        staging:
          sha: ccc333
`
	got, err := WriteScopedState([]byte(twoComponents), "ci",
		StateWrite{Component: "api", Env: "staging", State: &EnvState{SHA: "updated"}},
	)
	if err != nil {
		t.Fatalf("WriteScopedState: %v", err)
	}
	out := string(got)
	if !strings.Contains(out, "updated") {
		t.Errorf("component write did not apply its own leaf:\n%s", out)
	}
	if !strings.Contains(out, "ccc333") {
		t.Errorf("component write destroyed sibling component 'web':\n%s", out)
	}
}

func keysOfState(m map[string]*EnvState) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

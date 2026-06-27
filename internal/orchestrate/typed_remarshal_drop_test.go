package orchestrate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"gopkg.in/yaml.v3"
)

// remarshalManifest is a pin_mode:sha manifest whose config carries both a
// modeled SHA-pin field (cli_version_sha) and a key the running binary does not
// model (future_field). future_field stands in for the older-binary case: any
// cascade build cut before a config field was added sees that field as an
// unmodeled key, exactly as builds predating cli_version_sha saw it.
const remarshalManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
    cli_version: v0.6.0
    pin_mode: sha
    cli_version_sha: abc123deadbeefabc123deadbeefabc123deadbe
    future_field: keep-me
  state:
    dev:
      version: v0.1.0-rc.0
`

// TestTypedRemarshal_UnmodeledConfig_IsDropped documents the lossy state-write
// mechanism behind the SHA-pin drift (#372/#390). The original finalize write
// re-serialized the whole manifest through the typed CICDFile struct. That
// round-trip omits any key the running binary does not model and discards the
// top-level manifest-key wrapper, so a pin_mode:sha repo loses its pin on the
// next state commit and reads as permanent drift. This test pins the failing
// behavior so the contrast with the WriteManifestState fix is explicit.
func TestTypedRemarshal_UnmodeledConfig_IsDropped(t *testing.T) {
	file, err := config.ParseManifestBytes([]byte(remarshalManifest), "ci")
	if err != nil {
		t.Fatalf("ParseManifestBytes: %v", err)
	}

	out, err := yaml.Marshal(file)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(out)

	if strings.Contains(got, "future_field") {
		t.Errorf("typed remarshal was expected to drop the unmodeled future_field, but it survived:\n%s", got)
	}
	if strings.Contains(got, "ci:") {
		t.Errorf("typed remarshal was expected to drop the 'ci:' wrapper, but it survived:\n%s", got)
	}
}

// TestWriteManifestState_UnmodeledConfig_IsPreserved is the fix half: routing the
// same state write through config.WriteManifestState performs YAML-node surgery,
// touching only the state subtree. Unmodeled config (future_field), the modeled
// SHA-pin field (cli_version_sha), and the manifest-key wrapper all survive
// verbatim, while the state update is still applied.
func TestWriteManifestState_UnmodeledConfig_IsPreserved(t *testing.T) {
	state := map[string]*config.EnvState{
		"dev": {SHA: "newsha", Version: "v0.2.0-rc.0"},
	}

	out, err := config.WriteManifestState([]byte(remarshalManifest), "ci", state, nil)
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "future_field: keep-me") {
		t.Errorf("WriteManifestState dropped the unmodeled future_field:\n%s", got)
	}
	if !strings.Contains(got, "cli_version_sha: abc123deadbeefabc123deadbeefabc123deadbe") {
		t.Errorf("WriteManifestState dropped cli_version_sha:\n%s", got)
	}
	if !strings.Contains(got, "ci:") {
		t.Errorf("WriteManifestState dropped the 'ci:' wrapper:\n%s", got)
	}
	if !strings.Contains(got, "newsha") {
		t.Errorf("WriteManifestState did not apply the state update:\n%s", got)
	}
}

package orchestrate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// TestWriteConfig_TopLevelCliVersionSHA_Preserved guards the node-level state
// write against dropping cli_version_sha regardless of where the field sits in
// the manifest tree.
//
// The existing TestWriteConfig_PreservesFullManifest covers the modeled
// placement under ci.config. This case pins the other arrangement: a
// cli_version_sha scalar at the top level of the document, a sibling of the
// manifest-key section rather than a child of it. The state write replaces only
// the state and latest_release keys inside the manifest-key section, so every
// top-level sibling, including a top-level cli_version_sha, must survive the
// round-trip byte-for-byte.
func TestWriteConfig_TopLevelCliVersionSHA_Preserved(t *testing.T) {
	const sha = "9dc69a1f66753a3865c38c34eca5a931f677c803"
	orig := `schema_version: 1
pin_mode: sha
cli_version_sha: ` + sha + `
manifest_file: .github/manifest.yaml
ci:
  config:
    trunk_branch: main
    environments:
      - dev
  state:
    dev:
      sha: oldsha
`

	out, err := config.WriteManifestState(
		[]byte(orig),
		"ci",
		map[string]*config.EnvState{"dev": {SHA: "newsha"}},
		nil,
	)
	if err != nil {
		t.Fatalf("WriteManifestState: %v", err)
	}
	got := string(out)

	// State must be updated.
	if !strings.Contains(got, "newsha") {
		t.Errorf("state not updated; missing newsha:\n%s", got)
	}
	// Top-level cli_version_sha sibling must survive untouched.
	if !strings.Contains(got, "cli_version_sha: "+sha) {
		t.Errorf("top-level cli_version_sha dropped:\n%s", got)
	}
	// Other top-level siblings must survive too.
	if !strings.Contains(got, "pin_mode: sha") {
		t.Errorf("top-level pin_mode dropped:\n%s", got)
	}
	if !strings.Contains(got, "schema_version: 1") {
		t.Errorf("top-level schema_version dropped:\n%s", got)
	}
}

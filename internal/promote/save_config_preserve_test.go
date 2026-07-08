package promote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// TestSaveConfig_NonDryRunPromote_PreservesUnmodeledManifestKeys proves that a
// non-dry-run promotion (the path cascade simulate exercises) rewrites only the
// mutable state subtree and preserves manifest configuration this binary does not
// model. Before saveConfig routed through config.WriteManifestState it re-marshaled
// the typed CICDFile, which silently dropped any unmodeled key on the round-trip.
func TestSaveConfig_NonDryRunPromote_PreservesUnmodeledManifestKeys(t *testing.T) {
	// Raw manifest with keys the typed CICDFile does not model: an unmodeled
	// setting nested under config and an unmodeled section beside state. A typed
	// re-marshal would drop both; the state-subtree write must keep them.
	const manifest = `ci:
  config:
    trunk_branch: master
    environments:
      - dev
      - test
      - uat
      - prod
    future_setting: forward-compatible
  experimental_section:
    keep: this-value
    nested:
      also: preserved
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.0
`

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cicd.yaml")
	if err := os.WriteFile(configPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	promoter, err := NewPromoter(PromoterOptions{
		ConfigPath: configPath,
		DryRun:     false,
		Actor:      "test-actor",
	})
	if err != nil {
		t.Fatalf("failed to create promoter: %v", err)
	}

	result, err := promoter.Promote(ModeDefault, "")
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Promote did not succeed: %s", result.Error)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read manifest back: %v", err)
	}
	out := string(got)

	// Unmodeled keys must survive the state write verbatim.
	for _, want := range []string{
		"future_setting: forward-compatible",
		"experimental_section:",
		"keep: this-value",
		"also: preserved",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unmodeled content %q was dropped by saveConfig; manifest now:\n%s", want, out)
		}
	}

	// The state write must still have taken effect: dev promoted into test.
	reparsed, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("failed to reparse manifest: %v", err)
	}
	testState := reparsed.State["test"]
	if testState == nil {
		t.Fatalf("expected state.test to be written, got nil; manifest:\n%s", out)
		return
	}
	if testState.SHA != "abc123" {
		t.Errorf("state.test.sha = %q, want %q", testState.SHA, "abc123")
	}
	if testState.Version != "v1.0.0-rc.0" {
		t.Errorf("state.test.version = %q, want %q", testState.Version, "v1.0.0-rc.0")
	}
}

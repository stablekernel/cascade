package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteConfig_PreservesFullManifest guards against the state-write that
// silently dropped manifest configuration on the round-trip (issue #372).
//
// The finalize state write must touch only the mutable state subtree and leave
// every other manifest key byte-for-byte intact: the top-level manifest key
// wrapper, modeled config fields (cli_version_sha, which a SHA-pin repo relies
// on), config keys this binary does not model (a newer cascade release may add
// fields an older pinned binary cannot round-trip), and the internal runtime
// fields (manifest_file/manifest_key) must never leak into the committed file.
func TestWriteConfig_PreservesFullManifest(t *testing.T) {
	dir := t.TempDir()
	mfDir := filepath.Join(dir, ".github")
	if err := os.MkdirAll(mfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(mfDir, "manifest.yaml")

	const sha = "5702d41a1234567890abcdef1234567890abcdef"
	orig := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
    cli_version: v0.6.0
    pin_mode: sha
    cli_version_sha: ` + sha + `
    future_field: keep-me
    deploys:
      - name: app
        run: echo deploy
  state:
    dev:
      sha: oldsha
`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	o, err := NewOrchestrator(path, "ci", "dev")
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	// Mutate state exactly as Finalize does, then persist.
	o.cicdFile.State["dev"].SHA = "newsha"
	o.cicdFile.State["dev"].Version = "v0.6.0-rc.1"
	if err := o.writeConfig(); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	// State must be updated.
	if !strings.Contains(got, "newsha") {
		t.Errorf("state not updated; missing newsha:\n%s", got)
	}
	// Top-level manifest key wrapper must survive.
	if !strings.Contains(got, "ci:") {
		t.Errorf("top-level 'ci:' wrapper dropped:\n%s", got)
	}
	// Modeled SHA-pin field must survive (the drift that started this).
	if !strings.Contains(got, "cli_version_sha: "+sha) {
		t.Errorf("cli_version_sha dropped:\n%s", got)
	}
	// Sibling config the round-trip should never touch.
	if !strings.Contains(got, "cli_version: v0.6.0") {
		t.Errorf("cli_version dropped:\n%s", got)
	}
	if !strings.Contains(got, "pin_mode: sha") {
		t.Errorf("pin_mode dropped:\n%s", got)
	}
	// Config keys this binary does not model must survive (older-binary case).
	if !strings.Contains(got, "future_field: keep-me") {
		t.Errorf("unmodeled config field dropped:\n%s", got)
	}
	// Internal runtime fields must never leak into the committed manifest.
	if strings.Contains(got, "manifest_file:") || strings.Contains(got, "manifest_key:") {
		t.Errorf("internal runtime field leaked into manifest:\n%s", got)
	}
}

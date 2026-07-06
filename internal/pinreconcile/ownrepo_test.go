package pinreconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/generate"
	"github.com/stretchr/testify/require"
)

const (
	ownRepoHeader = "# SINGLE SOURCE OF TRUTH for every third-party action version cascade pins.\n" +
		"#\n" +
		"# This header must survive a full re-marshal of the file.\n"

	ownRepoOldCheckoutSHA = "1111111111111111111111111111111111111111"
	ownRepoNewCheckoutSHA = "2222222222222222222222222222222222222222"
	ownRepoOldUploadSHA   = "3333333333333333333333333333333333333333"
	ownRepoNewUploadSHA   = "4444444444444444444444444444444444444444"
)

// writeOwnRepoTestRepo lays out a minimal cascade-shaped repo: a disk
// action_pins.yaml (with a header comment block) carrying stale SHAs for
// actions/checkout and actions/upload-artifact, a build workflow stub, and a
// two-environment manifest so regenerate produces both orchestrate.yaml and
// promote.yaml. It returns the repo root and the action_pins.yaml path.
func writeOwnRepoTestRepo(t *testing.T) (dir, pinsPath string) {
	t.Helper()
	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "internal", "generate"), 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".github", "workflows", "build.yaml"),
		[]byte("name: Build\non:\n  workflow_call:\n"), 0o644))

	manifest := "ci:\n" +
		"  config:\n" +
		"    trunk_branch: main\n" +
		"    environments: [dev, prod]\n" +
		"    pin_mode: sha\n" +
		"    builds:\n" +
		"      - name: app\n" +
		"        workflow: .github/workflows/build.yaml\n" +
		"        triggers: [\"src/**\"]\n" +
		"    deploys: []\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".github", "manifest.yaml"), []byte(manifest), 0o644))

	pinsPath = filepath.Join(dir, "internal", "generate", "action_pins.yaml")
	pinsYAML := ownRepoHeader +
		"actions:\n" +
		"  actions/checkout:        { tag: v7, sha: " + ownRepoOldCheckoutSHA + ", version: v7.0.0, emit: true }\n" +
		"  actions/upload-artifact: { tag: v7, sha: " + ownRepoOldUploadSHA + ", version: v7.0.1, emit: true }\n"
	require.NoError(t, os.WriteFile(pinsPath, []byte(pinsYAML), 0o644))

	return dir, pinsPath
}

// TestRunOwnRepo_AdoptsWorkflowAndCompositeActionBumps proves the own-repo
// mode: a bump landing in a hand-written workflow AND one landing only in a
// composite action are both adopted into the on-disk action_pins.yaml (a full
// re-marshal, since cascade owns that file), its header survives, and every
// regenerated file (not just orchestrate.yaml) agrees with the adopted pins.
func TestRunOwnRepo_AdoptsWorkflowAndCompositeActionBumps(t *testing.T) {
	dir, pinsPath := writeOwnRepoTestRepo(t)

	handWritten := filepath.Join(".github", "workflows", "hand-written.yaml")
	require.NoError(t, os.WriteFile(filepath.Join(dir, handWritten),
		[]byte("      - uses: actions/checkout@"+ownRepoNewCheckoutSHA+" # v8.0.0\n"), 0o644))

	compositeAction := filepath.Join(".github", "actions", "setup-cli", "action.yaml")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github", "actions", "setup-cli"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, compositeAction),
		[]byte("runs:\n  using: composite\n  steps:\n    - uses: actions/upload-artifact@"+ownRepoNewUploadSHA+" # v8.0.1\n"), 0o644))

	changed, err := RunOwnRepo(OwnRepoOptions{
		Root:           dir,
		ActionPinsPath: pinsPath,
		ChangedFiles:   []string{handWritten, compositeAction},
	})
	require.NoError(t, err)
	require.True(t, changed)

	pinsBytes, err := os.ReadFile(pinsPath)
	require.NoError(t, err)
	pins := string(pinsBytes)

	require.Contains(t, pins, ownRepoNewCheckoutSHA, "the workflow bump must be adopted")
	require.Contains(t, pins, ownRepoNewUploadSHA, "the composite-action bump must be adopted, not just the workflow one")
	require.Contains(t, pins, "This header must survive a full re-marshal of the file.",
		"the header comment block must survive the re-marshal")

	for _, workflow := range []string{"orchestrate.yaml", "promote.yaml"} {
		content, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", workflow))
		require.NoError(t, err)

		want := map[string]generate.ActionPinEntry{
			"actions/checkout": {SHA: ownRepoNewCheckoutSHA, Version: "v8.0.0", Emit: true},
		}
		mismatches := generate.ScanUsesForPinDrift(workflow, string(content), want)
		require.Emptyf(t, mismatches, "%s must carry the adopted pin cleanly", workflow)
	}
}

// TestRunOwnRepo_ConvergedTreeIsNoOp mirrors Run's idempotency guarantee for
// the own-repo mode: re-running against an already-adopted tree changes nothing.
func TestRunOwnRepo_ConvergedTreeIsNoOp(t *testing.T) {
	dir, pinsPath := writeOwnRepoTestRepo(t)

	handWritten := filepath.Join(".github", "workflows", "hand-written.yaml")
	require.NoError(t, os.WriteFile(filepath.Join(dir, handWritten),
		[]byte("      - uses: actions/checkout@"+ownRepoNewCheckoutSHA+" # v8.0.0\n"), 0o644))

	opts := OwnRepoOptions{Root: dir, ActionPinsPath: pinsPath, ChangedFiles: []string{handWritten}}

	changed, err := RunOwnRepo(opts)
	require.NoError(t, err)
	require.True(t, changed)

	before, err := os.ReadFile(pinsPath)
	require.NoError(t, err)

	changed, err = RunOwnRepo(opts)
	require.NoError(t, err)
	require.False(t, changed, "a converged tree must report no change")

	after, err := os.ReadFile(pinsPath)
	require.NoError(t, err)
	require.Equal(t, before, after, "a no-op pass must not rewrite action_pins.yaml")
}

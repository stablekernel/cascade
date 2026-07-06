package pinreconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/generate"
	"github.com/stretchr/testify/require"
)

const (
	testOldCheckoutSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testNewCheckoutSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// writeReconcileTestRepo lays out a minimal single-environment repo: a build
// workflow stub the orchestrate generator's callback introspection reads, and
// a sha-mode manifest carrying a stale checkout pin. It returns the repo root.
func writeReconcileTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".github", "workflows", "build.yaml"),
		[]byte("name: Build\non:\n  workflow_call:\n"), 0o644))

	manifest := "ci:\n" +
		"  config:\n" +
		"    trunk_branch: main\n" +
		"    environments: [prod]\n" +
		"    pin_mode: sha\n" +
		"    action_pins:\n" +
		"      actions/checkout: " + testOldCheckoutSHA + " # v5.0.0\n" +
		"    builds:\n" +
		"      - name: app\n" +
		"        workflow: .github/workflows/build.yaml\n" +
		"        triggers: [\"src/**\"]\n"
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".github", "manifest.yaml"), []byte(manifest), 0o644))

	return dir
}

// TestRun_AdoptsShaBumpAndRegeneratesClean proves the end-to-end user-repo
// path: a sha bump observed in a changed source file lands verbatim (with its
// version comment) in the manifest's action_pins, and the resulting regenerate
// carries that same pin cleanly, with the manifest's untouched keys intact.
func TestRun_AdoptsShaBumpAndRegeneratesClean(t *testing.T) {
	dir := writeReconcileTestRepo(t)

	changed := "dependabot-bump.yaml"
	require.NoError(t, os.WriteFile(filepath.Join(dir, changed),
		[]byte("      - uses: actions/checkout@"+testNewCheckoutSHA+" # v6.0.0\n"), 0o644))

	ok, err := Run(Options{Root: dir, ChangedFiles: []string{changed}})
	require.NoError(t, err)
	require.True(t, ok)

	manifestBytes, err := os.ReadFile(filepath.Join(dir, ".github", "manifest.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(manifestBytes), testNewCheckoutSHA+" # v6.0.0")
	require.Contains(t, string(manifestBytes), "trunk_branch: main", "untouched keys survive")

	orchestrate, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "orchestrate.yaml"))
	require.NoError(t, err)

	// The regenerated file must carry exactly the adopted checkout sha/version
	// pair; scanning it against that same pair must report zero drift. Other
	// governed actions the file may carry are absent from this table and are
	// therefore skipped by ScanUsesForPinDrift, not flagged.
	want := map[string]generate.ActionPinEntry{
		"actions/checkout": {SHA: testNewCheckoutSHA, Version: "v6.0.0", Emit: true},
	}
	mismatches := generate.ScanUsesForPinDrift("orchestrate.yaml", string(orchestrate), want)
	require.Empty(t, mismatches, "regenerated orchestrate.yaml must carry the adopted pin cleanly")
}

// TestRun_IdempotentSecondPass proves a converged tree is a safe no-op: running
// Run again against a manifest that already carries the adopted pin reports no
// change and leaves the manifest byte-identical, which is the loop-termination
// guarantee a reconcile companion relies on to avoid committing forever.
func TestRun_IdempotentSecondPass(t *testing.T) {
	dir := writeReconcileTestRepo(t)

	changed := "dependabot-bump.yaml"
	require.NoError(t, os.WriteFile(filepath.Join(dir, changed),
		[]byte("      - uses: actions/checkout@"+testNewCheckoutSHA+" # v6.0.0\n"), 0o644))

	ok, err := Run(Options{Root: dir, ChangedFiles: []string{changed}})
	require.NoError(t, err)
	require.True(t, ok, "the first pass must adopt the bump")

	manifestPath := filepath.Join(dir, ".github", "manifest.yaml")
	afterFirst, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	ok, err = Run(Options{Root: dir, ChangedFiles: []string{changed}})
	require.NoError(t, err)
	require.False(t, ok, "a converged tree must report no change")

	afterSecond, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	require.Equal(t, afterFirst, afterSecond, "a no-op pass must not rewrite the manifest")
}

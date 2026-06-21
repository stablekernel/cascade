package initcmd

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
	"github.com/stablekernel/cascade/internal/verify"
)

// verifyShape runs the full init -> generate -> verify pipeline for the given
// topology and asserts verify returns nil (no drift, no orphans).
func verifyShape(t *testing.T, topology string) {
	t.Helper()

	dir := t.TempDir()

	// Step 1: run the real init command into dir.
	_, err := runInit(t, "--topology", topology, "--name", "demo", "--dir", dir)
	require.NoError(t, err, "init must succeed for topology %s", topology)

	configPath := filepath.Join(dir, config.DefaultManifestFile)

	// Step 2: resolve the manifest so we can call Plan and write its output.
	planned, err := generate.Plan(generate.PlanOptions{
		ConfigPath:  configPath,
		ManifestKey: config.DefaultManifestKey,
	})
	require.NoError(t, err, "Plan must succeed for topology %s", topology)

	// Step 3: write every planned file into dir so verify can read them.
	baseDir := generate.ResolveBaseDir(configPath)
	for _, p := range planned {
		writePath := p.Path
		if !filepath.IsAbs(writePath) {
			writePath = filepath.Join(baseDir, writePath)
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(writePath), 0o755))
		require.NoError(t, os.WriteFile(writePath, []byte(p.Content), 0o644))
	}

	// Step 4: run verify and assert it is clean (nil return).
	err = verify.Run(verify.Options{
		ConfigPath:  configPath,
		ManifestKey: config.DefaultManifestKey,
	}, io.Discard, io.Discard)
	require.NoError(t, err, "verify must be clean after init+generate for topology %s", topology)
}

// TestVerifyClean_NoEnv confirms that for a release-only (no-env) scaffold,
// running generate-workflow and then verify produces a clean result.
func TestVerifyClean_NoEnv(t *testing.T) {
	verifyShape(t, "no-env")
}

// TestVerifyClean_FourEnv confirms that for a four-environment scaffold,
// running generate-workflow and then verify produces a clean result.
func TestVerifyClean_FourEnv(t *testing.T) {
	verifyShape(t, "four-env")
}

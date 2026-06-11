package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConsistencyManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	content := `ci:
  config:
    environments: [dev, test, uat, prod]
  state:
    dev:
      sha: trunkhead
    test:
      sha: mergesha
      ref: env/test
      base_sha: basesha
      patches: [patchX]
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestConsistency_OrphanEnvBranchFlagged(t *testing.T) {
	path := writeConsistencyManifest(t)

	// env/test matches the diverged "test" env (healthy); env/staging has no
	// matching divergence (orphan).
	brancher := func() ([]string, error) {
		return []string{"main", "env/test", "env/staging"}, nil
	}

	out := captureOutput(t, func() {
		err := runConsistency(path, "ci", false, brancher)
		require.NoError(t, err)
	})

	assert.Contains(t, out, "env/staging", "orphan branch must be reported")
	assert.NotContains(t, out, "env/test", "healthy diverged branch must not be reported")
}

func TestConsistency_NoOrphans(t *testing.T) {
	path := writeConsistencyManifest(t)

	brancher := func() ([]string, error) {
		return []string{"main", "env/test"}, nil
	}

	out := captureOutput(t, func() {
		err := runConsistency(path, "ci", false, brancher)
		require.NoError(t, err)
	})

	assert.Contains(t, strings.ToLower(out), "no orphan")
}

func TestConsistency_JSON(t *testing.T) {
	path := writeConsistencyManifest(t)

	brancher := func() ([]string, error) {
		return []string{"env/test", "env/staging"}, nil
	}

	out := captureOutput(t, func() {
		err := runConsistency(path, "ci", true, brancher)
		require.NoError(t, err)
	})

	assert.Contains(t, out, `"orphan_env_branches"`)
	assert.Contains(t, out, "env/staging")
	assert.NotContains(t, out, "env/test")
}

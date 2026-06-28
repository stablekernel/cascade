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

// failingDeleter fails the test if called: report-only runs must never delete.
func failingDeleter(t *testing.T) branchDeleter {
	t.Helper()
	return func(remote, branch string) error {
		t.Fatalf("deleter must not be called without --fix (remote=%s branch=%s)", remote, branch)
		return nil
	}
}

func TestConsistency_OrphanEnvBranchFlagged(t *testing.T) {
	path := writeConsistencyManifest(t)

	// env/test matches the diverged "test" env (healthy); env/staging has no
	// matching divergence (orphan).
	brancher := func() ([]string, error) {
		return []string{"main", "env/test", "env/staging"}, nil
	}

	out := captureOutput(t, func() {
		err := runConsistency(consistencyOptions{
			configPath: path,
			key:        "ci",
			remote:     "origin",
			lister:     brancher,
			deleter:    failingDeleter(t),
		})
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
		err := runConsistency(consistencyOptions{
			configPath: path,
			key:        "ci",
			remote:     "origin",
			lister:     brancher,
			deleter:    failingDeleter(t),
		})
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
		err := runConsistency(consistencyOptions{
			configPath: path,
			key:        "ci",
			jsonOutput: true,
			remote:     "origin",
			lister:     brancher,
			deleter:    failingDeleter(t),
		})
		require.NoError(t, err)
	})

	assert.Contains(t, out, `"orphan_env_branches"`)
	assert.Contains(t, out, "env/staging")
	assert.NotContains(t, out, "env/test")
	// Report-only default is unchanged: no healed key is emitted.
	assert.NotContains(t, out, "healed_env_branches")
}

func TestConsistency_Fix_DeletesOnlyOrphans(t *testing.T) {
	path := writeConsistencyManifest(t)

	brancher := func() ([]string, error) {
		return []string{"main", "env/test", "env/staging"}, nil
	}

	var deleted []string
	deleter := func(remote, branch string) error {
		assert.Equal(t, "origin", remote, "deletion must target the inspected remote")
		deleted = append(deleted, branch)
		return nil
	}

	out := captureOutput(t, func() {
		err := runConsistency(consistencyOptions{
			configPath: path,
			key:        "ci",
			fix:        true,
			remote:     "origin",
			lister:     brancher,
			deleter:    deleter,
		})
		require.NoError(t, err)
	})

	assert.Equal(t, []string{"env/staging"}, deleted, "only the orphan is deleted")
	assert.NotContains(t, deleted, "env/test", "the diverged env's live branch must never be deleted")
	assert.Contains(t, out, "healed env/* branches")
	assert.Contains(t, out, "env/staging")
}

func TestConsistency_Fix_Idempotent(t *testing.T) {
	path := writeConsistencyManifest(t)

	brancher := func() ([]string, error) {
		return []string{"env/test", "env/staging"}, nil
	}

	// A deleter that no-ops on an absent branch mirrors git.DeleteRemoteBranch,
	// so re-running --fix (or fixing an already-gone orphan) stays a clean no-op.
	calls := 0
	deleter := func(remote, branch string) error {
		calls++
		return nil
	}

	run := func() {
		err := runConsistency(consistencyOptions{
			configPath: path,
			key:        "ci",
			fix:        true,
			remote:     "origin",
			lister:     brancher,
			deleter:    deleter,
		})
		require.NoError(t, err)
	}

	captureOutput(t, run)
	captureOutput(t, run)

	assert.Equal(t, 2, calls, "each run heals the same single orphan without error")
}

func TestConsistency_Fix_JSONReflectsDeletions(t *testing.T) {
	path := writeConsistencyManifest(t)

	brancher := func() ([]string, error) {
		return []string{"env/test", "env/staging"}, nil
	}
	deleter := func(remote, branch string) error { return nil }

	out := captureOutput(t, func() {
		err := runConsistency(consistencyOptions{
			configPath: path,
			key:        "ci",
			jsonOutput: true,
			fix:        true,
			remote:     "origin",
			lister:     brancher,
			deleter:    deleter,
		})
		require.NoError(t, err)
	})

	assert.Contains(t, out, `"orphan_env_branches"`)
	assert.Contains(t, out, `"healed_env_branches"`)
	assert.Contains(t, out, "env/staging")
	assert.NotContains(t, out, "env/test", "the diverged env branch is neither orphaned nor healed")
}

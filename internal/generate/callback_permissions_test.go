package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// topLevelPermissions extracts the top-level permissions: block from a generated
// workflow. It returns the lines indented under the first "permissions:" line
// that begins at column zero (a top-level key), stopping at the next top-level
// key or blank line. Job-level permissions blocks are indented and therefore
// never match.
func topLevelPermissions(t *testing.T, content string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	var out []string
	inBlock := false
	for _, line := range lines {
		if line == "permissions:" {
			inBlock = true
			out = append(out, line)
			continue
		}
		if inBlock {
			if strings.HasPrefix(line, "  ") {
				out = append(out, line)
				continue
			}
			break
		}
	}
	require.True(t, inBlock, "no top-level permissions block found")
	return strings.Join(out, "\n")
}

// writeCallWorkflow writes a minimal reusable workflow file so the generator can
// resolve a callback's workflow path.
func writeCallWorkflow(t *testing.T, dir, rel string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte("on:\n  workflow_call:\n"), 0644))
}

// TestOrchestrate_TopLevelPermissions_UnionIncludesCallbackScopes asserts that a
// build callback declaring id-token: write propagates into the calling
// workflow's top-level permissions union, alongside the base contents/actions
// scopes. A reusable-workflow caller job cannot set job-level permissions, so the
// top-level block must grant the union.
func TestOrchestrate_TopLevelPermissions_UnionIncludesCallbackScopes(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	writeCallWorkflow(t, tmpDir, ".github/workflows/build.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:        "app",
				Workflow:    ".github/workflows/build.yaml",
				Triggers:    []string{"src/**"},
				Permissions: map[string]string{"id-token": "write"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	perms := topLevelPermissions(t, result)
	assert.Contains(t, perms, "contents: write")
	assert.Contains(t, perms, "actions: read")
	assert.Contains(t, perms, "id-token: write")
}

// TestOrchestrate_TopLevelPermissions_Deterministic asserts the appended
// callback-only scopes are emitted in stable sorted order and that repeated
// generation is byte-identical.
func TestOrchestrate_TopLevelPermissions_Deterministic(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	writeCallWorkflow(t, tmpDir, ".github/workflows/build.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				Permissions: map[string]string{
					"id-token": "write",
					"packages": "read",
				},
			},
		},
	}

	var first string
	for i := 0; i < 25; i++ {
		gen := NewGenerator(cfg, tmpDir)
		result, err := gen.Generate()
		require.NoError(t, err)
		if i == 0 {
			first = result
			continue
		}
		assert.Equal(t, first, result, "generation must be byte-identical across runs")
	}

	perms := topLevelPermissions(t, first)
	// Base scopes keep their existing order; callback-only scopes are appended
	// sorted alphabetically: id-token before packages.
	idIdx := strings.Index(perms, "id-token: write")
	pkgIdx := strings.Index(perms, "packages: read")
	require.NotEqual(t, -1, idIdx)
	require.NotEqual(t, -1, pkgIdx)
	assert.Less(t, idIdx, pkgIdx, "callback-only scopes must be sorted alphabetically")
}

// TestOrchestrate_TopLevelPermissions_NoCallbackPermsByteIdentical asserts that
// when no callback declares permissions, the top-level block is exactly the
// historical base block (no churn, existing golden assertions preserved).
func TestOrchestrate_TopLevelPermissions_NoCallbackPermsByteIdentical(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	writeCallWorkflow(t, tmpDir, ".github/workflows/build.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)
	assert.Contains(t, result, "permissions:\n  contents: write\n  actions: read\n")
}

// TestPromote_TopLevelPermissions_UnionIncludesCallbackScopes asserts deploy
// callback OIDC permissions propagate into the promote workflow's top-level
// union, alongside its base contents/actions scopes.
func TestPromote_TopLevelPermissions_UnionIncludesCallbackScopes(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:        "services",
				Workflow:    ".github/workflows/deploy.yaml",
				Permissions: map[string]string{"id-token": "write"},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	perms := topLevelPermissions(t, result)
	assert.Contains(t, perms, "contents: write")
	assert.Contains(t, perms, "actions: write")
	assert.Contains(t, perms, "id-token: write")
}

// TestRollback_TopLevelPermissions_UnionIncludesCallbackScopes asserts deploy
// callback OIDC permissions propagate into the rollback workflow's top-level
// union, alongside its base contents/actions scopes.
func TestRollback_TopLevelPermissions_UnionIncludesCallbackScopes(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:        "services",
				Workflow:    ".github/workflows/deploy.yaml",
				Permissions: map[string]string{"id-token": "write"},
			},
		},
	}

	gen := NewRollbackGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	perms := topLevelPermissions(t, result)
	assert.Contains(t, perms, "contents: write")
	assert.Contains(t, perms, "actions: write")
	assert.Contains(t, perms, "id-token: write")
}

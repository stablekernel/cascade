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

// TestOrchestrate_TopLevelPermissions_ExcludesCallbackScopes asserts that a build
// callback declaring id-token: write does NOT widen the calling workflow's
// top-level permissions block. The callback scope is scoped to the caller job
// (least privilege); the top-level block carries only cascade's own
// orchestration scopes.
func TestOrchestrate_TopLevelPermissions_ExcludesCallbackScopes(t *testing.T) {
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
	assert.Contains(t, perms, "contents: read")
	assert.Contains(t, perms, "actions: read")
	assert.NotContains(t, perms, "contents: write",
		"the top-level block must stay least privilege (reads only)")
	assert.NotContains(t, perms, "id-token: write",
		"callback-only scopes must not leak into the top-level block")
}

// TestOrchestrate_CallbackJobPermissions_CarryDeclaredScopes asserts that the
// build callback's declared scopes are rendered on the caller job's own
// job-level permissions: block, in deterministic sorted order, and that
// repeated generation is byte-identical.
func TestOrchestrate_CallbackJobPermissions_CarryDeclaredScopes(t *testing.T) {
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

	job := jobSection(first, "build-app:")
	assert.Contains(t, job, "permissions:")
	assert.Contains(t, job, "id-token: write")
	assert.Contains(t, job, "packages: read")
	// Scopes render in sorted order so output is deterministic (id-token before
	// packages).
	idIdx := strings.Index(job, "id-token: write")
	pkgIdx := strings.Index(job, "packages: read")
	require.NotEqual(t, -1, idIdx)
	require.NotEqual(t, -1, pkgIdx)
	assert.Less(t, idIdx, pkgIdx, "callback scopes must be emitted in sorted order")

	// The top-level block must not carry the callback-only scopes.
	perms := topLevelPermissions(t, first)
	assert.NotContains(t, perms, "id-token: write")
	assert.NotContains(t, perms, "packages: read")
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
	assert.Contains(t, result, "permissions:\n  contents: read\n  actions: read\n")
}

// TestPromote_TopLevelPermissions_ExcludesCallbackScopes asserts deploy callback
// OIDC permissions do NOT widen the promote workflow's top-level block; they are
// scoped to the caller job instead.
func TestPromote_TopLevelPermissions_ExcludesCallbackScopes(t *testing.T) {
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
	assert.Contains(t, perms, "contents: read")
	assert.NotContains(t, perms, "contents: write",
		"the top-level block must stay least privilege (reads only)")
	assert.NotContains(t, perms, "actions: write",
		"actions: write is scoped to the finalize job, not the top level")
	assert.NotContains(t, perms, "id-token: write",
		"callback-only scopes must not leak into the top-level block")
}

// TestRollback_TopLevelPermissions_ExcludesCallbackScopes asserts deploy callback
// OIDC permissions do NOT widen the rollback workflow's top-level block; they are
// scoped to the caller job instead.
func TestRollback_TopLevelPermissions_ExcludesCallbackScopes(t *testing.T) {
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
	assert.Contains(t, perms, "contents: read")
	assert.NotContains(t, perms, "contents: write",
		"the top-level block must stay least privilege (reads only)")
	assert.NotContains(t, perms, "actions: write",
		"rollback has no release dispatch, so actions: write is never granted")
	assert.NotContains(t, perms, "id-token: write",
		"callback-only scopes must not leak into the top-level block")
}

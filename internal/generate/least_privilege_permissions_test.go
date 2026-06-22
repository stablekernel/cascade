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

// jobPermissionsBlock returns the rendered job-level permissions: block that
// immediately follows the target job's "runs-on: ubuntu-latest" line, up to the
// next line that is not part of the permissions block (a six-space entry).
func jobPermissionsBlock(t *testing.T, job string) string {
	t.Helper()
	lines := strings.Split(job, "\n")
	var out []string
	inBlock := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "permissions:" && strings.HasPrefix(line, "    ") {
			inBlock = true
			out = append(out, line)
			continue
		}
		if inBlock {
			if strings.HasPrefix(line, "      ") {
				out = append(out, line)
				continue
			}
			break
		}
	}
	require.True(t, inBlock, "no job-level permissions block found in job:\n%s", job)
	return strings.Join(out, "\n")
}

// TestOrchestrate_TopLevelLeastPrivilege asserts the orchestrate workflow's
// top-level permissions default to reads only, with no write scope, and that the
// finalize job carries the contents: write needed to commit state.
func TestOrchestrate_TopLevelLeastPrivilege(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0o755))
	writeCallWorkflow(t, tmpDir, ".github/workflows/build.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	out, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	top := topLevelPermissions(t, out)
	assert.Equal(t, "permissions:\n  contents: read\n  actions: read", top)
	assert.NotContains(t, top, "contents: write")
	assert.NotContains(t, top, "deployments: write")

	finalize := jobSection(out, "finalize:")
	block := jobPermissionsBlock(t, finalize)
	assert.Contains(t, block, "contents: write")
}

// TestOrchestrate_NativeDeployments_JobLevelDeploymentsWrite asserts that with
// the GitHub Deployments API enabled, deployments: write lands on the finalize
// job, not the top-level block.
func TestOrchestrate_NativeDeployments_JobLevelDeploymentsWrite(t *testing.T) {
	cfg, tmpDir := nativeDeploymentsConfig(t)

	out, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	top := topLevelPermissions(t, out)
	assert.NotContains(t, top, "deployments: write",
		"deployments: write must be scoped to the finalize job, not the top level")
	assert.NotContains(t, top, "contents: write")

	finalize := jobSection(out, "finalize:")
	block := jobPermissionsBlock(t, finalize)
	assert.Contains(t, block, "contents: write")
	assert.Contains(t, block, "deployments: write")
}

// TestPromote_TopLevelLeastPrivilege asserts the promote workflow's top-level
// permissions default to contents: read only, and that the finalize job carries
// the contents: write and actions: write it needs to commit state and dispatch
// the Release workflow.
func TestPromote_TopLevelLeastPrivilege(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "services", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	out, err := NewPromoteGenerator(cfg, "").Generate()
	require.NoError(t, err)

	top := topLevelPermissions(t, out)
	assert.Equal(t, "permissions:\n  contents: read", top)
	assert.NotContains(t, top, "contents: write")
	assert.NotContains(t, top, "actions: write")

	finalize := jobSection(out, "finalize:")
	block := jobPermissionsBlock(t, finalize)
	assert.Contains(t, block, "contents: write")
	assert.Contains(t, block, "actions: write")
}

// TestPromote_NativeDeployments_JobLevelDeploymentsWrite asserts deployments:
// write lands on the promote finalize job, not the top-level block.
func TestPromote_NativeDeployments_JobLevelDeploymentsWrite(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0o755))
	writeCallWorkflow(t, tmpDir, ".github/workflows/deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"production"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
		Deployments: &config.DeploymentsConfig{Enabled: true},
		EnvironmentConfig: map[string]config.EnvironmentConfig{
			"production": {EnvironmentURL: "https://app.example.com"},
		},
	}

	out, err := NewPromoteGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	top := topLevelPermissions(t, out)
	assert.NotContains(t, top, "deployments: write")

	finalize := jobSection(out, "finalize:")
	block := jobPermissionsBlock(t, finalize)
	assert.Contains(t, block, "deployments: write")
}

// TestHotfix_TopLevelLeastPrivilege asserts the hotfix workflow's top-level
// permissions default to reads only, with the write scopes pushed down to the
// apply and finalize jobs that need them.
func TestHotfix_TopLevelLeastPrivilege(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
	}

	out, err := NewHotfixGenerator(cfg, "").Generate()
	require.NoError(t, err)

	top := topLevelPermissions(t, out)
	assert.Equal(t, "permissions:\n  contents: read\n  actions: read", top)
	assert.NotContains(t, top, "contents: write")
	assert.NotContains(t, top, "issues: write")
	assert.NotContains(t, top, "pull-requests: write")

	apply := jobSection(out, "apply:")
	applyBlock := jobPermissionsBlock(t, apply)
	assert.Contains(t, applyBlock, "contents: write")
	assert.Contains(t, applyBlock, "issues: write")
	assert.Contains(t, applyBlock, "pull-requests: write")

	finalize := jobSection(out, "finalize:")
	finalizeBlock := jobPermissionsBlock(t, finalize)
	assert.Contains(t, finalizeBlock, "contents: write")
}

// TestRollback_TopLevelLeastPrivilege asserts the rollback workflow's top-level
// permissions default to contents: read only, with contents: write pushed down
// to the finalize job.
func TestRollback_TopLevelLeastPrivilege(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "services", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	out, err := NewRollbackGenerator(cfg, "").Generate()
	require.NoError(t, err)

	top := topLevelPermissions(t, out)
	assert.Equal(t, "permissions:\n  contents: read", top)
	assert.NotContains(t, top, "contents: write")
	assert.NotContains(t, top, "actions: write")

	finalize := jobSection(out, "finalize:")
	block := jobPermissionsBlock(t, finalize)
	assert.Contains(t, block, "contents: write")
}

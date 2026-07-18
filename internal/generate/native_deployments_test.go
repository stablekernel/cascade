package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// nativeDeploymentsConfig returns a minimal manifest with the GitHub Deployments
// API integration enabled and a per-environment environment_url, plus the
// build/deploy callbacks and on-disk deploy workflow the generator needs so the
// finalize job can reference a real deploy job result.
func nativeDeploymentsConfig(t *testing.T) (*config.TrunkConfig, string) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0o755))

	deployWorkflow := `name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        required: true
        type: string
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte(deployWorkflow), 0o644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Environments: []config.EnvironmentEntry{
			{Name: "production", EnvironmentConfig: config.EnvironmentConfig{EnvironmentURL: "https://app.example.com"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
		Deployments: &config.DeploymentsConfig{Enabled: boolPtr(true)},
	}
	return cfg, tmpDir
}

// TestNativeDeployments_Enabled proves the finalize job creates a GitHub
// Deployment, reports in_progress, then reports a terminal status, all guarded
// to real GitHub, with deployments: write scope and the environment_url wired in.
func TestNativeDeployments_Enabled(t *testing.T) {
	cfg, tmpDir := nativeDeploymentsConfig(t)

	out, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, out, "deployments: write",
		"finalize must request deployments: write when deployments is enabled")
	assert.Contains(t, out, "/deployments \\",
		"must POST to the deployments collection to create a Deployment")
	assert.Contains(t, out, "state=in_progress",
		"must report an in_progress deployment status")
	assert.Contains(t, out, "/statuses \\",
		"must POST to the deployment statuses collection")
	assert.Contains(t, out, "https://app.example.com",
		"the configured environment_url must be wired into the status update")
	assert.Contains(t, out, "github.server_url == 'https://github.com'",
		"deployment steps must be guarded to real GitHub via the server_url guard")
	// A dry-run orchestrate skips its deploys, so it must not create a real
	// Deployment: every lifecycle step is also gated on a non-dry-run run.
	assert.Contains(t, out, "github.server_url == 'https://github.com' && github.event.inputs.dry_run != 'true'",
		"deployment steps must not run on a dry-run orchestrate")
	// The terminal status reflects the deploy job result, not a hardcoded value.
	assert.Contains(t, out, "needs.deploy-app.result",
		"terminal status must derive from the deploy job result")
}

// TestNativeDeployments_Disabled proves none of the Deployments API wiring is
// emitted when the toggle is absent, keeping the OFF-state output unchanged.
func TestNativeDeployments_Disabled(t *testing.T) {
	cfg, tmpDir := nativeDeploymentsConfig(t)
	cfg.Deployments = nil

	out, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	for _, marker := range []string{
		"deployments: write",
		"/deployments \\",
		"state=in_progress",
	} {
		assert.NotContains(t, out, marker,
			"disabled deployments must not emit %q", marker)
	}
}

// TestNativeDeployments_Deterministic proves byte-stability across repeated
// generation, which the verify/no-drift contract depends on.
func TestNativeDeployments_Deterministic(t *testing.T) {
	cfg, tmpDir := nativeDeploymentsConfig(t)

	first, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	second, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

// TestNativeDeployments_Actionlint runs actionlint over the generated workflow.
// Skipped when actionlint is not installed so the suite stays hermetic.
func TestNativeDeployments_Actionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	cfg, tmpDir := nativeDeploymentsConfig(t)
	out, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0o755))
	wfPath := filepath.Join(wfDir, "orchestrate.yaml")
	require.NoError(t, os.WriteFile(wfPath, []byte(out), 0o644))

	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")
	writeDeployReusableStub(t, dir, out)

	// Disable shellcheck: inline run: bodies trip style nits orthogonal to the
	// Deployments API step structure this test governs.
	cmd := exec.Command(bin, "-shellcheck=", wfPath)
	cmd.Dir = dir
	output, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues:\n%s", string(output))
}

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

// ---- orchestrate generator tests ----

// TestEnvGates_Orchestrate_DeployJob_WithGHAEnvironment asserts that when
// environment_config carries a gha_environment for an env, the orchestrate
// deploy job (always a reusable-workflow uses: caller) threads the environment
// name via the with: environment: input using the dynamic input expression, and
// never carries a job-level environment: key (GHA forbids it on a uses: job).
func TestEnvGates_Orchestrate_DeployJob_WithGHAEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-svc")
	require.NotEmpty(t, block, "deploy-svc job not found in generated orchestrate workflow")

	// Reusable caller: environment is threaded via the with: input using the
	// dynamic input expression (env is chosen at runtime).
	assert.Contains(t, block, "uses:", "deploy job must call the reusable workflow via uses:")
	assert.Contains(t, block, "github.event.inputs.environment", "orchestrate deploy must pass environment via the workflow input expression")
	// No job-level environment: key on a uses: caller job.
	lines := strings.Split(block, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "    environment:") && !strings.HasPrefix(line, "      environment:") {
			t.Errorf("reusable deploy caller must not carry a job-level environment: key; line: %q", line)
		}
	}
}

// TestEnvGates_Orchestrate_DeployJob_WithoutGHAEnvironment asserts that when
// no environment has a gha_environment configured, no job-level environment:
// key is emitted on deploy jobs (non-breaking default).
func TestEnvGates_Orchestrate_DeployJob_WithoutGHAEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/deploy.yaml"),
		[]byte("on:\n  workflow_call:\n"),
		0644,
	))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
		// No EnvironmentConfig; no gha_environment.
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-svc")
	require.NotEmpty(t, block, "deploy-svc job not found in generated orchestrate workflow")

	// No job-level environment: should appear. The job-level key is at exactly
	// 4-space indentation; the reusable-workflow with: input is at 6 spaces.
	lines := strings.Split(block, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "    environment:") && !strings.HasPrefix(line, "      environment:") {
			t.Errorf("unexpected job-level environment: key in deploy job when gha_environment is not configured; line: %q", line)
		}
	}
}

// TestEnvGates_Orchestrate_BuildJob_NoEnvironmentKey asserts that build jobs
// never receive a job-level environment: key even when gha_environment is set.
func TestEnvGates_Orchestrate_BuildJob_NoEnvironmentKey(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/build.yaml"),
		[]byte("on:\n  workflow_call:\n"),
		0644,
	))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "staging", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "staging-gate"}},
		},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "build-app")
	require.NotEmpty(t, block, "build-app job not found in generated orchestrate workflow")

	// Build jobs must NOT carry a job-level environment: key. The job-level key
	// appears at exactly 4-space indentation ("    environment: ..."), while the
	// reusable-workflow with: input is at 6 spaces ("      environment: ...").
	lines := strings.Split(block, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "    environment:") && !strings.HasPrefix(line, "      environment:") {
			t.Errorf("build jobs must not carry job-level environment: key; found line: %q", line)
		}
	}
}

// ---- promote generator tests ----

// TestEnvGates_Promote_SingleDeployJob_WithGHAEnvironment asserts that the
// promote single-mode deploy job (a reusable-workflow uses: caller) threads the
// environment via the with: environment: input referencing preflight's
// target_env output when any environment has gha_environment configured.
func TestEnvGates_Promote_SingleDeployJob_WithGHAEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-svc")
	require.NotEmpty(t, block, "deploy-svc job not found in generated promote workflow")

	assert.Contains(t, block, "environment:", "promote deploy job must thread the environment via the with: input")
	assert.Contains(t, block, "needs.preflight.outputs.target_env", "promote deploy environment input must reference preflight's target_env output")
}

// TestEnvGates_Promote_SingleDeployJob_WithoutGHAEnvironment asserts that the
// promote generator does NOT emit a job-level environment: on the single-mode
// deploy job when no environment has gha_environment configured.
func TestEnvGates_Promote_SingleDeployJob_WithoutGHAEnvironment(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
		// No EnvironmentConfig.
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-svc")
	require.NotEmpty(t, block, "deploy-svc job not found in generated promote workflow")

	lines := strings.Split(block, "\n")
	for _, line := range lines {
		if line == "    environment: ${{ needs.preflight.outputs.target_env }}" {
			t.Errorf("unexpected job-level environment: key in promote deploy job when gha_environment is not configured; line: %q", line)
		}
	}
}

// TestEnvGates_Promote_ProdDeployJob_WithGHAEnvironment asserts that the promote
// prod deploy job (deploy-<n>-prod, always a reusable-workflow uses: caller)
// threads the target environment via the with: environment: input statically
// (the prod env is known at generate time) and never carries a job-level
// environment: key, which GHA forbids on a uses: job.
func TestEnvGates_Promote_ProdDeployJob_WithGHAEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-svc-prod")
	require.NotEmpty(t, block, "deploy-svc-prod job not found in generated promote workflow")

	// Reusable caller: the target environment is threaded via the with: input.
	assert.Contains(t, block, "uses:", "prod deploy job must call the reusable workflow via uses:")
	assert.Contains(t, block, "      environment: prod", "prod deploy must pass the target environment via the with: input")
	// No job-level environment: key on a uses: caller job.
	lines := strings.Split(block, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "    environment:") && !strings.HasPrefix(line, "      environment:") {
			t.Errorf("reusable prod deploy caller must not carry a job-level environment: key; line: %q", line)
		}
	}
}

// TestEnvGates_Promote_ProdDeployJob_WithoutGHAEnvironment asserts that the
// promote generator does NOT emit a job-level environment: on the prod deploy
// job when the final env has no gha_environment configured.
func TestEnvGates_Promote_ProdDeployJob_WithoutGHAEnvironment(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
		// No EnvironmentConfig.
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-svc-prod")
	require.NotEmpty(t, block, "deploy-svc-prod job not found in generated promote workflow")

	lines := strings.Split(block, "\n")
	for _, line := range lines {
		if line == "    environment: prod" {
			t.Errorf("unexpected job-level environment: key in prod deploy job when gha_environment is not configured; line: %q", line)
		}
	}
}

// TestEnvGates_Promote_ProdDeployJob_OnlyFinalEnvGated asserts that when only
// an intermediate env (not the final env) has gha_environment, the prod deploy
// job does NOT receive a job-level environment: key (since we use a static
// per-env lookup for the prod job).
func TestEnvGates_Promote_ProdDeployJob_OnlyFinalEnvGated(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "dev"},
			// Only the intermediate env has gha_environment; prod does not.
			{Name: "staging", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "staging-gate"}},
			{Name: "prod"},
		},
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-svc-prod")
	require.NotEmpty(t, block, "deploy-svc-prod job not found in generated promote workflow")

	lines := strings.Split(block, "\n")
	for _, line := range lines {
		if line == "    environment: prod" || line == "    environment: staging-gate" {
			t.Errorf("unexpected job-level environment: key in prod deploy job; only staging has gha_environment; line: %q", line)
		}
	}

	// The single deploy job (non-prod) SHOULD thread environment: because staging has gha_environment.
	singleBlock := jobBlock(t, result, "deploy-svc")
	// deploy-svc-prod starts with "deploy-svc-prod:", so we need to stop at that
	// boundary. jobBlock already handles that by stopping at the next "  <id>:" line.
	assert.Contains(t, singleBlock, "environment:", "deploy-svc single job must carry job-level environment: since staging has gha_environment")
}

// TestEnvGates_Helper_envGHAName verifies the envGHAName helper returns the
// gha_environment value when configured and falls back to the cascade env name.
func TestEnvGates_Helper_envGHAName(t *testing.T) {
	cfg := &config.TrunkConfig{
		Environments: []config.EnvironmentEntry{
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
	}

	assert.Equal(t, "production", envGHAName(cfg, "prod"), "should return gha_environment when configured")
	assert.Equal(t, "dev", envGHAName(cfg, "dev"), "should fall back to cascade env name when not configured")
	assert.Equal(t, "staging", envGHAName(cfg, "staging"), "should fall back to cascade env name for unconfigured env")
}

// TestEnvGates_Helper_anyEnvHasGHAConfig verifies anyEnvHasGHAConfig reports
// correctly for configs with and without gha_environment.
func TestEnvGates_Helper_anyEnvHasGHAConfig(t *testing.T) {
	withConfig := &config.TrunkConfig{
		Environments: []config.EnvironmentEntry{
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
	}
	assert.True(t, anyEnvHasGHAConfig(withConfig), "should return true when at least one env has gha_environment")

	withoutConfig := &config.TrunkConfig{}
	assert.False(t, anyEnvHasGHAConfig(withoutConfig), "should return false when no env has gha_environment")

	emptyValue := &config.TrunkConfig{
		Environments: []config.EnvironmentEntry{
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: ""}},
		},
	}
	assert.False(t, anyEnvHasGHAConfig(emptyValue), "should return false when gha_environment is empty string")
}

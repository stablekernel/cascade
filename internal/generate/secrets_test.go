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

// minimalWorkflowYAML is reused across secrets tests as a stub reusable workflow.
const minimalWorkflowYAML = "on:\n  workflow_call:\n"

// orchestrateCfgWithDeploySecrets returns a minimal TrunkConfig whose single
// deploy callback carries the given SecretsConfig.
func orchestrateCfgWithDeploySecrets(secrets *config.SecretsConfig) (*config.TrunkConfig, string) {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				Triggers: []string{"src/**"},
				Secrets:  secrets,
			},
		},
	}, ".github/workflows/deploy.yaml"
}

// orchestrateCfgWithBuildSecrets returns a minimal TrunkConfig whose single
// build callback carries the given SecretsConfig.
func orchestrateCfgWithBuildSecrets(secrets *config.SecretsConfig) (*config.TrunkConfig, string) {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				Secrets:  secrets,
			},
		},
	}, ".github/workflows/build.yaml"
}

// setupWorkflowDir creates a temp dir with a stub workflow file at the given
// relative path and returns the temp dir path.
func setupWorkflowDir(t *testing.T, relPath string) string {
	t.Helper()
	tmpDir := t.TempDir()
	full := filepath.Join(tmpDir, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte(minimalWorkflowYAML), 0644))
	return tmpDir
}

// TestWriteSecretsBlock_NilNoOp verifies that an unset (nil) secrets config
// emits NO secrets block at all. Only a single blank line is written to
// preserve YAML job separation. secrets: inherit is now opt-in.
func TestWriteSecretsBlock_NilNoOp(t *testing.T) {
	var sb strings.Builder
	writeSecretsBlock(&sb, nil)
	got := sb.String()
	assert.Equal(t, "\n", got)
	assert.NotContains(t, got, "secrets:")
}

// TestWriteSecretsBlock_Inherit verifies that an explicit Inherit:true config
// still emits the "secrets: inherit" scalar form (opt-in).
func TestWriteSecretsBlock_Inherit(t *testing.T) {
	var sb strings.Builder
	writeSecretsBlock(&sb, &config.SecretsConfig{Inherit: true})
	got := sb.String()
	assert.Equal(t, "    secrets: inherit\n\n", got)
}

// TestWriteSecretsBlock_ExplicitMap verifies that an explicit map emits the
// per-entry secrets: block with ${{ secrets.CALLER_NAME }} expressions, sorted
// deterministically.
func TestWriteSecretsBlock_ExplicitMap(t *testing.T) {
	s := &config.SecretsConfig{
		Map: map[string]string{
			"DEPLOY_TOKEN": "MY_DEPLOY_TOKEN",
			"NPM_TOKEN":    "NPM_PUBLISH_TOKEN",
		},
	}
	var sb strings.Builder
	writeSecretsBlock(&sb, s)
	got := sb.String()

	assert.Contains(t, got, "    secrets:\n")
	assert.Contains(t, got, "      DEPLOY_TOKEN: ${{ secrets.MY_DEPLOY_TOKEN }}\n")
	assert.Contains(t, got, "      NPM_TOKEN: ${{ secrets.NPM_PUBLISH_TOKEN }}\n")
	// Must NOT contain the scalar inherit form.
	assert.NotContains(t, got, "secrets: inherit")

	// Entries are emitted in sorted order.
	deployIdx := strings.Index(got, "DEPLOY_TOKEN")
	npmIdx := strings.Index(got, "NPM_TOKEN")
	assert.Less(t, deployIdx, npmIdx, "keys must be sorted alphabetically")
}

// TestOrchestrateCallbackJob_NoSecretsByDefault verifies that a reusable-workflow
// build with no explicit secrets config emits NO secrets block at all. secrets:
// inherit is opt-in, never the default.
func TestOrchestrateCallbackJob_NoSecretsByDefault(t *testing.T) {
	cfg, wfPath := orchestrateCfgWithBuildSecrets(nil)
	tmpDir := setupWorkflowDir(t, wfPath)

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.NotContains(t, result, "secrets:")
}

// TestOrchestrateCallbackJob_InheritSecrets verifies that a reusable-workflow
// build with explicit Inherit:true emits "secrets: inherit".
func TestOrchestrateCallbackJob_InheritSecrets(t *testing.T) {
	cfg, wfPath := orchestrateCfgWithBuildSecrets(&config.SecretsConfig{Inherit: true})
	tmpDir := setupWorkflowDir(t, wfPath)

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "    secrets: inherit\n")
	assert.NotContains(t, result, "    secrets:\n      ")
}

// TestOrchestrateCallbackJob_ExplicitSecretsMap verifies that a reusable-
// workflow build with an explicit secrets map emits the per-entry block instead
// of "secrets: inherit".
func TestOrchestrateCallbackJob_ExplicitSecretsMap(t *testing.T) {
	cfg, wfPath := orchestrateCfgWithBuildSecrets(&config.SecretsConfig{
		Map: map[string]string{
			"BUILD_TOKEN": "GH_PACKAGES_TOKEN",
		},
	})
	tmpDir := setupWorkflowDir(t, wfPath)

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "    secrets:\n      BUILD_TOKEN: ${{ secrets.GH_PACKAGES_TOKEN }}\n")
	assert.NotContains(t, result, "    secrets: inherit\n")
}

// TestOrchestrateDeployCallbackJob_NoSecretsByDefault verifies that a
// reusable-workflow deploy with no explicit secrets config emits NO secrets
// block at all.
func TestOrchestrateDeployCallbackJob_NoSecretsByDefault(t *testing.T) {
	cfg, wfPath := orchestrateCfgWithDeploySecrets(nil)
	tmpDir := setupWorkflowDir(t, wfPath)

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.NotContains(t, result, "secrets:")
}

// TestOrchestrateDeployCallbackJob_InheritSecrets verifies that a reusable-workflow
// deploy with explicit Inherit:true emits "secrets: inherit".
func TestOrchestrateDeployCallbackJob_InheritSecrets(t *testing.T) {
	cfg, wfPath := orchestrateCfgWithDeploySecrets(&config.SecretsConfig{Inherit: true})
	tmpDir := setupWorkflowDir(t, wfPath)

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "    secrets: inherit\n")
	assert.NotContains(t, result, "    secrets:\n      ")
}

// TestOrchestrateDeployCallbackJob_ExplicitSecretsMap verifies that a reusable-
// workflow deploy with an explicit secrets map emits the per-entry block instead
// of "secrets: inherit".
func TestOrchestrateDeployCallbackJob_ExplicitSecretsMap(t *testing.T) {
	cfg, wfPath := orchestrateCfgWithDeploySecrets(&config.SecretsConfig{
		Map: map[string]string{
			"DEPLOY_TOKEN": "MY_DEPLOY_TOKEN",
		},
	})
	tmpDir := setupWorkflowDir(t, wfPath)

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "    secrets:\n      DEPLOY_TOKEN: ${{ secrets.MY_DEPLOY_TOKEN }}\n")
	assert.NotContains(t, result, "    secrets: inherit\n")
}

// TestOrchestrateCallbackJob_InlineRunUnaffected verifies that an inline-run
// callback (run: ...) does not emit any secrets: key at all; inline jobs are
// cascade-owned, not reusable-workflow calls.
func TestOrchestrateCallbackJob_InlineRunUnaffected(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "smoke",
				Run:      "go test ./...",
				Triggers: []string{"**/*.go"},
				// Secrets field set on an inline-run callback; must be ignored.
				Secrets: &config.SecretsConfig{
					Map: map[string]string{"SOME_TOKEN": "MY_TOKEN"},
				},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	// Inline run: jobs are cascade-owned steps; no secrets: key of any form.
	assert.NotContains(t, result, "secrets:")
}

// TestPromoteDeployJob_ExplicitSecretsMap verifies that a promote-workflow
// deploy callback with an explicit secrets map carries the per-entry block in
// the generated promote workflow.
func TestPromoteDeployJob_ExplicitSecretsMap(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				Triggers: []string{"src/**"},
				Secrets: &config.SecretsConfig{
					Map: map[string]string{
						"DEPLOY_TOKEN": "MY_DEPLOY_TOKEN",
					},
				},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	// Both deploy-app (intermediate) and deploy-app-prod must carry the explicit map.
	assert.Contains(t, result, "      DEPLOY_TOKEN: ${{ secrets.MY_DEPLOY_TOKEN }}\n",
		"explicit secrets map must appear in promote workflow deploy job")
	assert.NotContains(t, result, "    secrets: inherit\n",
		"secrets: inherit must not appear when explicit map is configured")
}

// TestPromoteDeployJob_NoSecretsByDefault verifies that a promote deploy with no
// explicit secrets config (nil) emits NO secrets block at all.
func TestPromoteDeployJob_NoSecretsByDefault(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				Triggers: []string{"src/**"},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.NotContains(t, result, "secrets:")
}

// TestPromoteDeployJob_InheritSecrets verifies that a promote deploy with
// explicit Inherit:true emits "secrets: inherit".
func TestPromoteDeployJob_InheritSecrets(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				Triggers: []string{"src/**"},
				Secrets:  &config.SecretsConfig{Inherit: true},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "    secrets: inherit\n")
	assert.NotContains(t, result, "    secrets:\n      ")
}

// hotfixCfgWithBuildSecrets returns a 2-environment hotfix-capable config whose
// single reusable-workflow build carries the given SecretsConfig.
func hotfixCfgWithBuildSecrets(secrets *config.SecretsConfig) *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Secrets:  secrets,
			},
		},
	}
}

// TestHotfixBuildJob_NoSecretsByDefault verifies the hotfix build job honors the
// opt-in model: a reusable-workflow build with no explicit secrets config emits
// NO secrets block, matching the orchestrate and promote callbacks.
func TestHotfixBuildJob_NoSecretsByDefault(t *testing.T) {
	gen := NewHotfixGenerator(hotfixCfgWithBuildSecrets(nil), "")
	result, err := gen.Generate()
	require.NoError(t, err)

	// The hotfix build job is the only reusable-workflow (uses:) job in the
	// hotfix workflow, so no secrets: block should appear anywhere.
	assert.NotContains(t, result, "secrets:")
}

// TestHotfixBuildJob_InheritSecrets verifies that an explicit Inherit:true on the
// hotfix build emits "secrets: inherit".
func TestHotfixBuildJob_InheritSecrets(t *testing.T) {
	gen := NewHotfixGenerator(hotfixCfgWithBuildSecrets(&config.SecretsConfig{Inherit: true}), "")
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "    secrets: inherit\n")
	assert.NotContains(t, result, "    secrets:\n      ")
}

// TestHotfixBuildJob_ExplicitSecretsMap verifies that an explicit secrets map on
// the hotfix build emits the per-entry least-privilege form.
func TestHotfixBuildJob_ExplicitSecretsMap(t *testing.T) {
	gen := NewHotfixGenerator(hotfixCfgWithBuildSecrets(&config.SecretsConfig{
		Map: map[string]string{"BUILD_TOKEN": "GH_PACKAGES_TOKEN"},
	}), "")
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "    secrets:\n      BUILD_TOKEN: ${{ secrets.GH_PACKAGES_TOKEN }}\n")
	assert.NotContains(t, result, "    secrets: inherit\n")
}

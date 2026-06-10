package promote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

func TestReadDeployResultsFromEnv(t *testing.T) {
	// readDeployResultsFromEnv reads DEPLOY_RESULT_<NAME> env vars; deploy
	// names with hyphens are converted to underscores in the env key.
	t.Setenv("DEPLOY_RESULT_CDK", "success")
	t.Setenv("DEPLOY_RESULT_DEPLOY_APP", "skipped")
	// Non-matching env vars should not appear in results.
	t.Setenv("DEPLOY_RESULT_OTHER", "success")

	got := readDeployResultsFromEnv([]string{"cdk", "deploy-app", "missing"})

	require.Equal(t, "success", got["cdk"], "cdk should map to success")
	require.Equal(t, "skipped", got["deploy-app"], "hyphenated deploy-app maps to DEPLOY_RESULT_DEPLOY_APP")
	_, hasMissing := got["missing"]
	require.False(t, hasMissing, "missing deploy with no env var should be absent")
	require.Len(t, got, 2)
}

func TestFinalizeCommand_UpdatesState(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write initial config
	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
      - name: app
        workflow: .github/workflows/deploy-app.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-1
    test: {}
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	// Create promotion result
	promotionResult := PromotionResult{
		Success:  true,
		FinalEnv: "test",
		Promotions: []EnvPromotion{
			{Environment: "test", SHA: "abc123", Version: "v1.0.0-2"},
		},
	}
	promotionJSON, err := json.Marshal(promotionResult)
	require.NoError(t, err)

	// Create command and execute
	cmd := NewCommand()
	cmd.SetArgs([]string{
		"finalize",
		"--config", configPath,
		"--promotion-result", string(promotionJSON),
		"--dry-run",
	})

	err = cmd.Execute()
	require.NoError(t, err)

	// Verify state was NOT written (dry-run)
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// In dry-run, original state should remain unchanged
	// The test state should still be empty (not updated)
	testState := cicdFile.State["test"]
	if testState == nil || testState.SHA == "" {
		// Good - state was not persisted
		return
	}
	// If state exists, it should be the original (empty) state
	require.Empty(t, testState.SHA)
}

func TestFinalizeCommand_RealUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write initial config
	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
  state:
    dev:
      sha: abc123
      version: v1.0.0-1
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	// Create promotion result
	promotionResult := PromotionResult{
		Success:  true,
		FinalEnv: "test",
		Promotions: []EnvPromotion{
			{Environment: "test", SHA: "abc123", Version: "v1.0.0-2"},
		},
	}
	promotionJSON, err := json.Marshal(promotionResult)
	require.NoError(t, err)

	// Create command and execute (no dry-run)
	cmd := NewCommand()
	cmd.SetArgs([]string{
		"finalize",
		"--config", configPath,
		"--promotion-result", string(promotionJSON),
	})

	err = cmd.Execute()
	require.NoError(t, err)

	// Verify state was written using proper parsing
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// State should be updated
	require.NotNil(t, cicdFile.State["test"])
	require.Equal(t, "abc123", cicdFile.State["test"].SHA)
	require.Equal(t, "v1.0.0-2", cicdFile.State["test"].Version)
}

func TestFinalizeCommand_RequiresPromotionResult(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write minimal config
	initialConfig := `ci:
  config:
    environments: [dev, test]
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	// Create command without promotion-result flag
	cmd := NewCommand()
	cmd.SetArgs([]string{
		"finalize",
		"--config", configPath,
	})

	err = cmd.Execute()
	// Should fail because --promotion-result is required
	require.Error(t, err)
}

func TestFinalizeCommand_WithJobQuery(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write config with deploys
	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
      - name: app
        workflow: .github/workflows/deploy-app.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-1
    test: {}
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	// Create promotion result
	promotionResult := PromotionResult{
		Success:  true,
		FinalEnv: "test",
		Promotions: []EnvPromotion{
			{Environment: "test", SHA: "abc123", Version: "v1.0.0-2"},
		},
	}
	promotionJSON, err := json.Marshal(promotionResult)
	require.NoError(t, err)

	// Note: This test will try to call the real gh CLI
	// For a unit test, we'd need to refactor runFinalize to accept a githubAPIClient
	// For now, this is an integration test that verifies the flow
	// It will fail if gh CLI is not available or if the repo/runID don't exist

	// Create command with repo and run-id (simulating workflow environment)
	cmd := NewCommand()
	cmd.SetArgs([]string{
		"finalize",
		"--config", configPath,
		"--promotion-result", string(promotionJSON),
		"--repo", "stablekernel/cascade-test",
		"--run-id", "123456", // Invalid run ID - will fail gracefully
		"--dry-run", // Don't actually write
	})

	// Execute - should handle error gracefully and default to skipped
	err = cmd.Execute()
	// Should succeed even if job query fails (with warning)
	require.NoError(t, err)
}

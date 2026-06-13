package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPreflightCommand_WithCIKeyManifest tests that the preflight command
// correctly parses manifest files with the required ci: key at top level.
// This test catches the nil pointer panic that occurred when using direct yaml.Unmarshal.
func TestPreflightCommand_WithCIKeyManifest(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write manifest with required ci: key at top level (standard format)
	// State has dev populated but test is empty -> standard promotion should work
	manifestContent := `ci:
  config:
    trunk_branch: master
    environments:
      - dev
      - test
      - uat
      - prod
    cli_version: v0.3.3
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
        triggers:
          - infra/**
      - name: app
        workflow: .github/workflows/deploy-app.yaml
        triggers: []
  state:
    dev:
      sha: abc123def456
      version: v1.0.0-rc.1
      committed_at: "2025-12-17T10:00:00Z"
      committed_by: test-user
    test: {}
    uat: {}
    prod: {}
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	// Execute preflight command
	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", configPath,
	})

	err = cmd.Execute()
	require.NoError(t, err, "preflight should succeed with ci: key manifest")
}

// TestPreflightCommand_WithEmptyDeploys tests that preflight works when
// the deploys array is empty or nil.
func TestPreflightCommand_WithEmptyDeploys(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// State has dev populated but test is empty -> standard promotion should work
	manifestContent := `ci:
  config:
    environments: [dev, test, uat, prod]
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.1
    test: {}
    uat: {}
    prod: {}
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", configPath,
	})

	err = cmd.Execute()
	require.NoError(t, err, "preflight should handle empty deploys")
}

// TestPreflightCommand_AllPromotionModes tests each promotion mode
// to ensure the preflight command handles all of them correctly.
func TestPreflightCommand_AllPromotionModes(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Setup state where default promotion works (dev->test)
	baseConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.1
    test:
      sha: def456
      version: v0.9.0-rc.1
    uat:
      sha: ghi789
      version: v0.8.0-rc.0
    prod: {}
`
	err := os.WriteFile(configPath, []byte(baseConfig), 0644)
	require.NoError(t, err)

	// Mode can be "default" or a cascade target like "dev-to-test"
	testCases := []string{
		"default",
		"dev-to-test",
		"dev-to-uat",
		"test-to-uat",
		"uat-to-prod",
	}

	for _, mode := range testCases {
		t.Run(mode, func(t *testing.T) {
			args := []string{
				"preflight",
				"--mode", mode,
				"--config", configPath,
			}

			cmd := NewCommand()
			cmd.SetArgs(args)

			// Some modes may fail due to state conditions,
			// but they should NOT panic
			err := cmd.Execute()
			// We're testing for panics, not success/failure
			// The command should return an error, not panic
			if err != nil {
				// Expected for some modes given the state
				t.Logf("mode %s returned expected error: %v", mode, err)
			}
		})
	}
}

// TestPreflightCommand_DefaultMode tests that --mode defaults to "default".
func TestPreflightCommand_DefaultMode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	manifestContent := `ci:
  config:
    environments: [dev, test]
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.1
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--config", configPath,
		// No --mode specified, should default to "default"
	})

	err = cmd.Execute()
	require.NoError(t, err, "preflight should succeed with default mode")
}

// TestPreflightCommand_InvalidConfig tests error handling for invalid configs.
func TestPreflightCommand_InvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write invalid YAML
	err := os.WriteFile(configPath, []byte("invalid: yaml: content:"), 0644)
	require.NoError(t, err)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", configPath,
	})

	err = cmd.Execute()
	require.Error(t, err, "preflight should fail with invalid config")
}

// TestPreflightCommand_NonexistentConfig tests error handling for missing config file.
func TestPreflightCommand_NonexistentConfig(t *testing.T) {
	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", "/nonexistent/path/manifest.yaml",
	})

	err := cmd.Execute()
	require.Error(t, err, "preflight should fail with nonexistent config")
}

// TestPreflightCommand_GHAOutput tests --gha-output flag.
func TestPreflightCommand_GHAOutput(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	outputPath := filepath.Join(tmpDir, "GITHUB_OUTPUT")

	// State has dev populated but test is empty -> standard promotion should work
	manifestContent := `ci:
  config:
    environments: [dev, test, uat, prod]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.1
    test: {}
    uat: {}
    prod: {}
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	// Create empty GITHUB_OUTPUT file
	err = os.WriteFile(outputPath, []byte(""), 0644)
	require.NoError(t, err)

	// Set GITHUB_OUTPUT env var (t.Setenv auto-cleans up)
	t.Setenv("GITHUB_OUTPUT", outputPath)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", configPath,
		"--gha-output",
	})

	err = cmd.Execute()
	require.NoError(t, err)

	// Verify GITHUB_OUTPUT was written
	output, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	require.Contains(t, string(output), "source_env=")
	require.Contains(t, string(output), "target_env=")
	// Deploy jobs consume source_image_tag for their image_tag input. The promoted
	// artifact version is the canonical image tag, so it must mirror source_version.
	require.Contains(t, string(output), "source_image_tag=v1.0.0-rc.1",
		"preflight must emit source_image_tag set to the source version")
}

// TestPreflightCommand_AllowBreaking tests --allow-breaking flag.
func TestPreflightCommand_AllowBreaking(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// State has dev populated but test is empty -> standard promotion should work
	manifestContent := `ci:
  config:
    environments: [dev, test, uat, prod]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.1
    test: {}
    uat: {}
    prod: {}
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", configPath,
		"--allow-breaking",
	})

	err = cmd.Execute()
	require.NoError(t, err, "preflight should succeed with --allow-breaking")
}

// TestPreflightCommand_JSONOutput tests --json flag.
func TestPreflightCommand_JSONOutput(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// State has dev populated but test is empty -> standard promotion should work
	manifestContent := `ci:
  config:
    environments: [dev, test, uat, prod]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.1
    test: {}
    uat: {}
    prod: {}
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", configPath,
		"--json",
	})

	err = cmd.Execute()
	require.NoError(t, err, "preflight should succeed with --json")
}

// TestPreflightCommand_DeployEnvVars tests that deploy check flags from
// environment variables are properly processed.
func TestPreflightCommand_DeployEnvVars(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// State has dev populated but test is empty -> standard promotion should work
	manifestContent := `ci:
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
      version: v1.0.0-rc.1
    test: {}
    uat: {}
    prod: {}
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	// Set deploy check environment variables (t.Setenv auto-cleans up)
	t.Setenv("DEPLOY_INFRA", "false")
	t.Setenv("DEPLOY_APP", "true")

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", configPath,
	})

	err = cmd.Execute()
	require.NoError(t, err, "preflight should handle deploy env vars")
}

// TestPreflightCommand_RealWorldManifest tests with a production-like manifest
// to ensure all fields are properly handled.
func TestPreflightCommand_RealWorldManifest(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// This matches the real-world manifest structure that caused the bug
	// State has dev populated but test is empty -> standard promotion should work
	manifestContent := `ci:
  config:
    trunk_branch: master
    environments:
      - dev
      - test
      - uat
      - prod
    cli_version: v0.3.3
    # Use default GITHUB_TOKEN for release operations
    validate:
      workflow: .github/workflows/validate.yaml
      inputs:
        lint_enabled: true
    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers:
          - src/**
          - go.mod
        artifacts:
          - name: binary
            path: '*.txt'
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
        triggers:
          - infra/**
      - name: app
        workflow: .github/workflows/deploy-app.yaml
        triggers: []
        depends_on:
          - app
    changelog:
      contributors: true
  state:
    dev:
      sha: 5cb540d63e94b158f798a3b5af3caee80e6a1290
      version: v0.1.0-rc.2
      committed_at: "2025-12-17T16:50:51Z"
      committed_by: octocat
      deploys:
        app:
          sha: 5cb540d63e94b158f798a3b5af3caee80e6a1290
          deployed_at: "2025-12-17T16:50:51Z"
          deployed_by: octocat
    test: {}
    uat: {}
    prod: {}
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", configPath,
	})

	err = cmd.Execute()
	require.NoError(t, err, "preflight should handle real-world manifest")
}

// TestPreflightCommand_TwoEnvCascade tests cascade mode with 2 environments.
func TestPreflightCommand_TwoEnvCascade(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	manifestContent := `ci:
  config:
    environments: [dev, prod]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.1
    prod: {}
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "dev-to-prod", // Cascade target passed directly as mode
		"--config", configPath,
	})

	err = cmd.Execute()
	require.NoError(t, err, "preflight cascade should work with 2 envs")
}

// TestPreflightCommand_NoPromotionNeeded tests when all environments
// including prod are already up to date.
func TestPreflightCommand_NoPromotionNeeded(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// ALL envs including prod have the same SHA
	manifestContent := `ci:
  config:
    environments: [dev, test, uat, prod]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.1
    test:
      sha: abc123
      version: v1.0.0-rc.1
    uat:
      sha: abc123
      version: v1.0.0-rc.1
    prod:
      sha: abc123
      version: v1.0.0
`
	err := os.WriteFile(configPath, []byte(manifestContent), 0644)
	require.NoError(t, err)

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"preflight",
		"--mode", "default",
		"--config", configPath,
	})

	err = cmd.Execute()
	// This should fail because all environments are in sync
	require.Error(t, err, "preflight should fail when all environments are up to date")
	require.Contains(t, err.Error(), "up to date")
}

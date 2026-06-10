package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

func TestFinalize_UpdatesStateForSuccessfulDeploys(t *testing.T) {
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

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	fin.SetDeployResult("infra", "success")
	fin.SetDeployResult("app", "failure")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "abc123",
			Version:     "v1.0.0-1",
		}},
	})

	err = fin.Run()
	require.NoError(t, err)

	// Read back config using proper parsing
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// Check state was updated
	testState := cicdFile.State["test"]
	require.NotNil(t, testState)
	require.Equal(t, "abc123", testState.SHA)
	require.Equal(t, "v1.0.0-1", testState.Version)

	// Check deploy states - success should be updated, failure should not
	require.NotNil(t, testState.Deploys)
	require.NotNil(t, testState.Deploys["infra"], "infra deploy should be recorded (success)")
	require.Equal(t, "abc123", testState.Deploys["infra"].SHA)
	require.Equal(t, "v1.0.0-1", testState.Deploys["infra"].Version, "per-deployable version should be recorded on finalize")
	require.Nil(t, testState.Deploys["app"], "app deploy should not be recorded (failure)")
}

func TestFinalize_RecordsPerDeployableVersion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
    deploys:
      - name: svc-a
        workflow: .github/workflows/deploy-svc-a.yaml
      - name: svc-b
        workflow: .github/workflows/deploy-svc-b.yaml
  state:
    dev:
      sha: abc123
      version: v1.2.0
    test: {}
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)
	fin.SetActor("deployer")

	fin.SetDeployResult("svc-a", "success")
	fin.SetDeployResult("svc-b", "success")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "abc123",
			Version:     "v1.2.0",
		}},
	})

	require.NoError(t, fin.Run())

	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	testState := cicdFile.State["test"]
	require.NotNil(t, testState)

	// Each deployable records the version it deployed, not just the env SHA.
	require.NotNil(t, testState.Deploys["svc-a"])
	require.Equal(t, "v1.2.0", testState.Deploys["svc-a"].Version)
	require.Equal(t, "abc123", testState.Deploys["svc-a"].SHA)
	require.Equal(t, "deployer", testState.Deploys["svc-a"].DeployedBy)

	require.NotNil(t, testState.Deploys["svc-b"])
	require.Equal(t, "v1.2.0", testState.Deploys["svc-b"].Version)
}

// TestFinalize_NonVersionedPromotionPreservesPriorDeployVersion verifies that a
// promotion carrying no version (older/non-versioned flows) does not clobber a
// previously recorded per-deployable version, keeping the field non-breaking.
func TestFinalize_NonVersionedPromotionPreservesPriorDeployVersion(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
    deploys:
      - name: svc-a
        workflow: .github/workflows/deploy-svc-a.yaml
  state:
    test:
      sha: oldsha
      deploys:
        svc-a:
          sha: oldsha
          version: v1.0.0
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)
	fin.SetActor("deployer")

	fin.SetDeployResult("svc-a", "success")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "newsha",
			// No Version on this promotion.
		}},
	})

	require.NoError(t, fin.Run())

	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	ds := cicdFile.State["test"].Deploys["svc-a"]
	require.NotNil(t, ds)
	require.Equal(t, "newsha", ds.SHA, "SHA should advance")
	require.Equal(t, "v1.0.0", ds.Version, "prior version should be preserved when promotion carries none")
}

func TestFinalize_UpdatesLatestReleaseOnPublish(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write initial config
	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
  state:
    uat:
      sha: def456
      version: v1.2.0-rc.0
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	fin, err := NewFinalizer(configPath, "prod")
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		ReleaseAction: "publish",
		ReleaseData: &ReleaseData{
			SHA:        "def456",
			SemVersion: "v1.2.0",
		},
		Promotions: []EnvPromotion{{
			Environment: "prod",
			SHA:         "def456",
			Version:     "v1.2.0",
		}},
	})

	err = fin.Run()
	require.NoError(t, err)

	// Read back config using proper parsing
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// Check latest_release was updated
	require.NotNil(t, cicdFile.LatestRelease)
	require.Equal(t, "v1.2.0", cicdFile.LatestRelease.Version)
	require.Equal(t, "def456", cicdFile.LatestRelease.SHA)
	require.NotEmpty(t, cicdFile.LatestRelease.ReleasedOn)
	require.NotEmpty(t, cicdFile.LatestRelease.ReleasedBy)
}

func TestFinalize_UpdatesMultipleEnvironments(t *testing.T) {
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

	fin, err := NewFinalizer(configPath, "uat")
	require.NoError(t, err)

	// Cascade promotion: dev -> test -> uat
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{
			{Environment: "test", SHA: "abc123", Version: "v1.0.0-1"},
			{Environment: "uat", SHA: "abc123", Version: "v1.0.0-rc.0"},
		},
	})

	err = fin.Run()
	require.NoError(t, err)

	// Read back config using proper parsing
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// Check both environments were updated
	require.NotNil(t, cicdFile.State["test"])
	require.Equal(t, "abc123", cicdFile.State["test"].SHA)
	require.Equal(t, "v1.0.0-1", cicdFile.State["test"].Version)

	require.NotNil(t, cicdFile.State["uat"])
	require.Equal(t, "abc123", cicdFile.State["uat"].SHA)
	require.Equal(t, "v1.0.0-rc.0", cicdFile.State["uat"].Version)
}

func TestFinalize_HandlesNoPromotionResult(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write initial config
	initialConfig := `ci:
  config:
    environments: [dev, test]
  state:
    dev:
      sha: abc123
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	// No promotion result set - should not fail
	err = fin.Run()
	require.NoError(t, err)

	// Config should still be valid
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)
	require.NotNil(t, cicdFile)
}

// TestFinalize_WithCIKeyManifest tests that finalize correctly parses
// manifest files with the required ci: key at top level.
func TestFinalize_WithCIKeyManifest(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write manifest with required ci: key at top level (standard format)
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
      - name: app
        workflow: .github/workflows/deploy-app.yaml
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

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err, "NewFinalizer should succeed with nested ci: key manifest")

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "abc123def456",
			Version:     "v1.0.0-rc.1",
		}},
	})
	fin.SetDeployResult("infra", "success")
	fin.SetDeployResult("app", "success")

	err = fin.Run()
	require.NoError(t, err)

	// Read back config using proper parsing
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// Verify test state was updated
	require.NotNil(t, cicdFile.State["test"])
	require.Equal(t, "abc123def456", cicdFile.State["test"].SHA)
	require.Equal(t, "v1.0.0-rc.1", cicdFile.State["test"].Version)
}

// TestFinalize_InvalidConfig tests error handling for invalid configs.
func TestFinalize_InvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Write invalid YAML
	err := os.WriteFile(configPath, []byte("invalid: yaml: content:"), 0644)
	require.NoError(t, err)

	_, err = NewFinalizer(configPath, "test")
	require.Error(t, err, "NewFinalizer should fail with invalid config")
}

// TestFinalize_NonexistentConfig tests error handling for missing config file.
func TestFinalize_NonexistentConfig(t *testing.T) {
	_, err := NewFinalizer("/nonexistent/path/manifest.yaml", "test")
	require.Error(t, err, "NewFinalizer should fail with nonexistent config")
}

// TestFinalize_SetActor tests the actor setter.
func TestFinalize_SetActor(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    environments: [dev, test]
  state:
    dev:
      sha: abc123
      version: v1.0.0-1
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	// Set custom actor
	fin.SetActor("custom-user")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "abc123",
			Version:     "v1.0.0-1",
		}},
	})

	err = fin.Run()
	require.NoError(t, err)

	// Verify the actor was used
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	require.Equal(t, "custom-user", cicdFile.State["test"].CommittedBy)
}

// TestFinalize_SkippedDeploys tests that skipped deploys don't update deploy state.
func TestFinalize_SkippedDeploys(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    environments: [dev, test]
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
      - name: app
        workflow: .github/workflows/deploy-app.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-1
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "abc123",
			Version:     "v1.0.0-1",
		}},
	})

	// Set mixed results: success, skipped, cancelled
	fin.SetDeployResult("infra", "success")
	fin.SetDeployResult("app", "skipped")

	err = fin.Run()
	require.NoError(t, err)

	// Read back config using proper parsing
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// Only successful deploys should have state
	require.NotNil(t, cicdFile.State["test"].Deploys["infra"])
	require.Nil(t, cicdFile.State["test"].Deploys["app"], "skipped deploy should not be recorded")
}

// TestFinalize_CancelledDeploys tests that cancelled deploys don't update deploy state.
func TestFinalize_CancelledDeploys(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    environments: [dev, test]
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-1
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "abc123",
			Version:     "v1.0.0-1",
		}},
	})

	fin.SetDeployResult("infra", "cancelled")

	err = fin.Run()
	require.NoError(t, err)

	// Read back config using proper parsing
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// Cancelled deploy should not have state
	require.Nil(t, cicdFile.State["test"].Deploys["infra"], "cancelled deploy should not be recorded")
}

// TestFinalize_NoReleaseActionDoesNotUpdateLatestRelease ensures that
// non-publish release actions don't update latest_release.
func TestFinalize_NoReleaseActionDoesNotUpdateLatestRelease(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

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

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		ReleaseAction: "prerelease", // Not "publish"
		ReleaseData: &ReleaseData{
			SHA:        "abc123",
			SemVersion: "v1.0.0-rc.0",
		},
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "abc123",
			Version:     "v1.0.0-1",
		}},
	})

	err = fin.Run()
	require.NoError(t, err)

	// Read back config using proper parsing
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// latest_release should NOT be set for non-publish actions
	require.Nil(t, cicdFile.LatestRelease)
}

// TestFinalize_WriteConfigValidYAML ensures the output is valid YAML.
func TestFinalize_WriteConfigValidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    trunk_branch: master
    environments: [dev, test, uat, prod]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
  state:
    dev:
      sha: abc123
      version: v1.0.0-1
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "abc123",
			Version:     "v1.0.0-1",
		}},
	})
	fin.SetDeployResult("app", "success")

	err = fin.Run()
	require.NoError(t, err)

	// Read and validate YAML by parsing with config.ParseManifestFile
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err, "output should be valid YAML with ci: key")
	require.NotNil(t, cicdFile)
}

// TestFinalize_RealWorldManifest tests with a production-like manifest
// to ensure all fields are properly handled.
func TestFinalize_RealWorldManifest(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	manifestContent := `ci:
  config:
    trunk_branch: master
    environments:
      - dev
      - test
      - uat
      - prod
    cli_version: v0.3.3
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

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "5cb540d63e94b158f798a3b5af3caee80e6a1290",
			Version:     "v0.1.0-rc.2",
		}},
	})
	fin.SetDeployResult("infra", "success")
	fin.SetDeployResult("app", "success")

	err = fin.Run()
	require.NoError(t, err, "finalize should handle real-world manifest")

	// Verify config is still valid by parsing with config.ParseManifestFile
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	require.NotNil(t, cicdFile.State["test"])
	require.Equal(t, "5cb540d63e94b158f798a3b5af3caee80e6a1290", cicdFile.State["test"].SHA)
}

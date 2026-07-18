package promote

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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

	// The env pointer must NOT advance: app terminally failed, so recording the
	// promotion at the new sha would assert a version the environment is not
	// running. The env holds at its prior (empty) pointer. Previously this test
	// asserted testState.SHA == "abc123", which encoded the state/reality
	// inversion this fix closes.
	testState := cicdFile.State["test"]
	require.NotNil(t, testState)
	require.Equal(t, "", testState.SHA, "env pointer held: an in-scope deploy failed")
	require.Equal(t, "", testState.Version, "env version held: an in-scope deploy failed")

	// The per-deploy SUCCESS row is still recorded (a partial success is not
	// lost); the failed deploy is not.
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

// TestFinalize_DeployResultWithoutPromotionResult reproduces the nil-pointer
// dereference that occurs when a deploy result is recorded but no promotion
// result was set. The per-deploy state update reads
// f.promotionResult.Promotions, so it must be skipped (not panic) when the
// promotion result is absent. The exported SetDeployResult/Run API permits this
// ordering even though the CLI always sets a promotion result first.
func TestFinalize_DeployResultWithoutPromotionResult(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

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

	// Record a successful deploy result but never set a promotion result.
	fin.SetDeployResult("app", "success")

	require.NotPanics(t, func() {
		require.NoError(t, fin.Run())
	})

	// Config should still be valid and unchanged in its deploy state.
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)
	require.NotNil(t, cicdFile)
}

// TestFinalize_WithClockInjectsDeterministicTimestamps verifies that an
// injected clock supplies the audit timestamps stamped into state, so tests and
// golden output can assert an exact value rather than a loose "recent" bound.
func TestFinalize_WithClockInjectsDeterministicTimestamps(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    environments: [dev, test]
  state:
    dev:
      sha: abc123
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	wantTS := fixed.Format(time.RFC3339)

	fin, err := NewFinalizer(configPath, "test", WithClock(func() time.Time { return fixed }))
	require.NoError(t, err)

	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "abc123",
			Version:     "v1.0.0",
		}},
	})

	err = fin.Run()
	require.NoError(t, err)

	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	testState := cicdFile.State["test"]
	require.NotNil(t, testState)
	require.Equal(t, wantTS, testState.CommittedAt, "CommittedAt should come from the injected clock")
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

// TestIsRealGitHub verifies the act/gitea vs real GitHub detection that decides
// whether CommitAndPush writes via the REST API or plain git push.
func TestIsRealGitHub(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	require.True(t, isRealGitHub(), "github.com must be detected as real GitHub")

	t.Setenv("GITHUB_SERVER_URL", "")
	require.True(t, isRealGitHub(), "unset server URL defaults to real GitHub")

	t.Setenv("GITHUB_SERVER_URL", "http://gitea:3000")
	require.False(t, isRealGitHub(), "gitea server URL must take the git-push path")
}

// TestTrunkBranchFromEnv verifies branch resolution for the API state write.
func TestTrunkBranchFromEnv(t *testing.T) {
	t.Setenv("GITHUB_REF", "refs/heads/main")
	require.Equal(t, "main", trunkBranchFromEnv())

	t.Setenv("GITHUB_REF", "develop")
	require.Equal(t, "develop", trunkBranchFromEnv())

	t.Setenv("GITHUB_REF", "")
	require.Equal(t, "main", trunkBranchFromEnv())
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

	// The only in-scope deploy was cancelled, so nothing landed: the env pointer
	// is held at its prior (empty) value and the cancelled deploy is not recorded.
	testState := cicdFile.State["test"]
	require.NotNil(t, testState)
	require.Equal(t, "", testState.SHA, "env pointer held: the only in-scope deploy was cancelled")
	require.Equal(t, "", testState.Version)
	require.Nil(t, testState.Deploys["infra"], "cancelled deploy should not be recorded")
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

// gitRun runs a git command in dir and fails the test on error, surfacing the
// combined output for diagnosis.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// TestCommitAndPushGit_DetachedHeadPushesToTrunk reproduces the finalize-job
// environment: the working tree is checked out at the triggering SHA, so HEAD is
// detached. A bare "git push" fails there with exit 128 (no upstream tracking
// branch). This verifies commitAndPushGit instead pushes HEAD explicitly to the
// trunk branch resolved from GITHUB_REF, so the state commit lands on trunk.
func TestCommitAndPushGit_DetachedHeadPushesToTrunk(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	work := filepath.Join(tmp, "work")

	// Bare remote acts as origin's trunk host.
	gitRun(t, tmp, "init", "--bare", "--initial-branch=main", bare)

	// Working clone with an initial commit on main, then push to seed origin/main.
	gitRun(t, tmp, "clone", bare, work)
	gitRun(t, work, "config", "user.name", "seed")
	gitRun(t, work, "config", "user.email", "seed@example.com")
	manifest := filepath.Join(work, "manifest.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte("ci:\n  state: {}\n"), 0644))
	gitRun(t, work, "add", "manifest.yaml")
	gitRun(t, work, "commit", "-m", "seed")
	gitRun(t, work, "push", "origin", "main")

	// Detach HEAD onto the seed commit, mirroring the finalize checkout.
	gitRun(t, work, "checkout", "--detach", "HEAD")

	// Sanity: a bare push from detached HEAD must fail, proving the scenario.
	bareDetached := exec.Command("git", "push")
	bareDetached.Dir = work
	require.Error(t, bareDetached.Run(),
		"bare git push from detached HEAD is expected to fail; the explicit ref is what fixes it")

	// Mutate the manifest so commitAndPushGit has something to commit.
	require.NoError(t, os.WriteFile(manifest, []byte("ci:\n  state:\n    test: {}\n"), 0644))

	t.Setenv("GITHUB_SERVER_URL", "http://gitea:3000") // force the plain-git path
	t.Setenv("GITHUB_REF", "refs/heads/main")

	f := &Finalizer{configPath: manifest, targetEnv: "test"}

	// Run from the working tree so the relative configPath resolves.
	restore, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(work))
	defer func() { _ = os.Chdir(restore) }()

	require.NoError(t, f.commitAndPushGit("chore: update state after promotion to test [skip ci]"))

	// origin/main on the bare remote must now point at the new state commit.
	logCmd := exec.Command("git", "--git-dir", bare, "log", "-1", "--pretty=%s", "main")
	out, err := logCmd.CombinedOutput()
	require.NoError(t, err, "reading remote main log: %s", out)
	require.Contains(t, string(out), "update state after promotion to test")
}

// TestFinalizeOverlayMerge verifies that overlaying this finalizer's owned env
// state onto a freshly fetched trunk manifest preserves a concurrent writer's
// untouched env keys while applying this finalizer's promoted env.
func TestFinalizeOverlayMerge(t *testing.T) {
	f := &Finalizer{
		cicdFile: &config.CICDFile{
			State: map[string]*config.EnvState{
				"staging": {SHA: "stg-sha", Version: "v2.0.0"},
			},
		},
		promotionResult: &PromotionResult{
			Promotions: []EnvPromotion{{Environment: "staging"}},
		},
	}

	current := &config.CICDFile{
		State: map[string]*config.EnvState{
			"dev": {SHA: "dev-sha"},
		},
	}

	f.overlayOwnedState(current)

	require.NotNil(t, current.State["dev"], "concurrent writer's env must survive")
	require.Equal(t, "dev-sha", current.State["dev"].SHA, "untouched env must be preserved")
	require.NotNil(t, current.State["staging"], "promoted env must be overlaid")
	require.Equal(t, "stg-sha", current.State["staging"].SHA)
}

func TestUpdateState_PushesPriorSnapshotOnTransition(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// test already holds a prior state; promoting a new SHA into it records the
	// outgoing state in the deploy-history ring.
	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
  state:
    test:
      sha: oldsha111
      version: v1.0.0
      committed_at: "2026-01-01T10:00:00Z"
      committed_by: alice
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)
	fin.SetActor("bob")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "newsha222",
			Version:     "v1.1.0",
		}},
	})

	require.NoError(t, fin.Run())

	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	testState := cicdFile.State["test"]
	require.NotNil(t, testState)
	require.Equal(t, "newsha222", testState.SHA)
	require.Len(t, testState.Previous, 1)
	require.Equal(t, "oldsha111", testState.Previous[0].SHA)
	require.Equal(t, "v1.0.0", testState.Previous[0].Version)
	require.Equal(t, "alice", testState.Previous[0].CommittedBy)
}

func TestUpdateState_NoSnapshotOnSameSHA(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Promoting the same SHA the env already records is not a transition, so no
	// snapshot is pushed.
	initialConfig := `ci:
  config:
    environments: [dev, test, uat, prod]
  state:
    test:
      sha: samesha111
      version: v1.0.0
      committed_at: "2026-01-01T10:00:00Z"
      committed_by: alice
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))

	fin, err := NewFinalizer(configPath, "test")
	require.NoError(t, err)
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SHA:         "samesha111",
			Version:     "v1.0.0",
		}},
	})

	require.NoError(t, fin.Run())

	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	testState := cicdFile.State["test"]
	require.NotNil(t, testState)
	require.Empty(t, testState.Previous)
}

// TestFinalize_HoldsEnvPointerOnInScopeDeployFailure proves the env pointer
// (state.<env>.sha/version) is NOT advanced when an in-scope deploy terminally
// failed. Advancing it would record the new commit as live while
// rollback_on_failure (default true) redeploys the OLD sha, inverting state and
// reality. The prior deploy that succeeded is still recorded per-deployable, so
// a re-dispatch retries only the failed one.
func TestFinalize_HoldsEnvPointerOnInScopeDeployFailure(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    environments: [dev, uat, prod]
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
      - name: app
        workflow: .github/workflows/deploy-app.yaml
  state:
    prod:
      sha: oldsha111
      version: v1.0.0
      committed_at: "2026-01-01T10:00:00Z"
      committed_by: alice
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))

	fin, err := NewFinalizer(configPath, "prod")
	require.NoError(t, err)
	fin.SetActor("bob")
	// infra succeeded, app terminally failed: a partial success.
	fin.SetDeployResult("infra", "success")
	fin.SetDeployResult("app", "failure")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "prod",
			SHA:         "newsha222",
			Version:     "v1.1.0",
		}},
	})

	require.NoError(t, fin.Run())

	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	prodState := cicdFile.State["prod"]
	require.NotNil(t, prodState)
	// The env pointer must stay at the OLD sha/version: the deploy did not land.
	require.Equal(t, "oldsha111", prodState.SHA,
		"env pointer must not advance when an in-scope deploy failed")
	require.Equal(t, "v1.0.0", prodState.Version,
		"env version must not advance when an in-scope deploy failed")
	require.Equal(t, "alice", prodState.CommittedBy,
		"a held env keeps its prior audit attribution")
	require.Empty(t, prodState.Previous,
		"no transition happened, so nothing is pushed to the deploy-history ring")

	// The per-deploy SUCCESS row is still recorded; the partial success is not lost.
	require.NotNil(t, prodState.Deploys["infra"],
		"a deploy that succeeded is still recorded even when a sibling failed")
	require.Equal(t, "newsha222", prodState.Deploys["infra"].SHA)
	require.Nil(t, prodState.Deploys["app"],
		"the failed deploy is not recorded")
}

// TestFinalize_HoldsEnvPointerRollbackOnFailureDisabled proves the env pointer
// is held on a failed deploy regardless of rollback_on_failure: the recorded
// state must match what the environment is actually running, whether or not an
// auto-rollback also fires.
func TestFinalize_HoldsEnvPointerRollbackOnFailureDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    environments: [dev, prod]
    deploys:
      - name: app
        workflow: .github/workflows/deploy-app.yaml
        rollback_on_failure: false
  state:
    prod:
      sha: oldsha111
      version: v1.0.0
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))

	fin, err := NewFinalizer(configPath, "prod")
	require.NoError(t, err)
	fin.SetDeployResult("app", "failure")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "prod",
			SHA:         "newsha222",
			Version:     "v1.1.0",
		}},
	})

	require.NoError(t, fin.Run())

	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	prodState := cicdFile.State["prod"]
	require.NotNil(t, prodState)
	require.Equal(t, "oldsha111", prodState.SHA,
		"env pointer held at old sha even without auto-rollback")
	require.Equal(t, "v1.0.0", prodState.Version)
}

// TestFinalize_AdvancesEnvPointerWhenAllInScopeSucceed proves the fix does not
// change the happy path: with every in-scope deploy successful, the env pointer
// advances exactly as before.
func TestFinalize_AdvancesEnvPointerWhenAllInScopeSucceed(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	initialConfig := `ci:
  config:
    environments: [dev, prod]
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
      - name: app
        workflow: .github/workflows/deploy-app.yaml
  state:
    prod:
      sha: oldsha111
      version: v1.0.0
`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0644))

	fin, err := NewFinalizer(configPath, "prod")
	require.NoError(t, err)
	fin.SetDeployResult("infra", "success")
	fin.SetDeployResult("app", "success")
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "prod",
			SHA:         "newsha222",
			Version:     "v1.1.0",
		}},
	})

	require.NoError(t, fin.Run())

	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	prodState := cicdFile.State["prod"]
	require.NotNil(t, prodState)
	require.Equal(t, "newsha222", prodState.SHA)
	require.Equal(t, "v1.1.0", prodState.Version)
}

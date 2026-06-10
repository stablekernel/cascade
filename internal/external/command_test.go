package external

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()
	assert.NotNil(t, cmd)
	assert.Equal(t, "external", cmd.Use)
	assert.Equal(t, "Handle external repository operations", cmd.Short)

	// Should have update subcommand
	subCmds := cmd.Commands()
	assert.NotEmpty(t, subCmds)

	var hasUpdate bool
	for _, sub := range subCmds {
		if sub.Use == "update" {
			hasUpdate = true
		}
	}
	assert.True(t, hasUpdate, "should have update subcommand")
}

func TestNewUpdateCommand(t *testing.T) {
	cmd := newUpdateCommand()
	assert.NotNil(t, cmd)
	assert.Equal(t, "update", cmd.Use)

	// Check required flags
	flags := cmd.Flags()
	assert.NotNil(t, flags.Lookup("source-repo"))
	assert.NotNil(t, flags.Lookup("deploy-name"))
	assert.NotNil(t, flags.Lookup("environment"))
	assert.NotNil(t, flags.Lookup("sha"))
	assert.NotNil(t, flags.Lookup("version"))
	assert.NotNil(t, flags.Lookup("artifacts"))
}

func TestWriteManifest(t *testing.T) {
	// Create a temp directory
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	// Create initial manifest
	initial := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
  state:
    dev:
      sha: abc123
other_key: preserved
`
	err := os.WriteFile(manifestPath, []byte(initial), 0644)
	require.NoError(t, err)

	// Create a CICDFile to write
	cicdFile := &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "test", "prod"},
		},
		State: map[string]*config.EnvState{
			"dev": {
				SHA: "def456",
				External: map[string]*config.ExternalDeployState{
					"cdk": {
						Repo: "org/cdk-infra",
						SHA:  "cdk123",
					},
				},
			},
		},
	}

	// Write the manifest
	err = writeManifest(manifestPath, "ci", cicdFile)
	require.NoError(t, err)

	// Read back and verify
	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	content := string(data)
	// Should contain updated SHA
	assert.Contains(t, content, "def456")
	// Should contain external state
	assert.Contains(t, content, "external")
	assert.Contains(t, content, "cdk")
	// Should preserve other keys
	assert.Contains(t, content, "other_key")
	assert.Contains(t, content, "preserved")
}

func TestUpdateCommand_RequiredFlags(t *testing.T) {
	cmd := NewCommand()

	// Test without required flags should fail
	cmd.SetArgs([]string{"update"})
	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required flag")
}

func TestUpdateCommand_InvalidArtifactsJSON(t *testing.T) {
	// Create a temp directory with a valid manifest
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
    external:
      - repo: org/cdk-infra
        deploys:
          - name: cdk
            workflow: .github/workflows/deploy-cdk.yaml
  state:
    dev:
      sha: abc123
`
	err := os.WriteFile(manifestPath, []byte(manifest), 0644)
	require.NoError(t, err)

	// Set globals
	configPath = manifestPath
	manifestKey = "ci"

	// Run with invalid JSON
	err = runUpdate("org/cdk-infra", "cdk", "dev", "sha123", "", "invalid json")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing artifacts JSON")
}

func TestUpdateCommand_NotPrimaryRepo(t *testing.T) {
	// Create a temp directory with a manifest without external repos
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
  state:
    dev:
      sha: abc123
`
	err := os.WriteFile(manifestPath, []byte(manifest), 0644)
	require.NoError(t, err)

	// Set globals
	configPath = manifestPath
	manifestKey = "ci"

	// Run should fail because repo is not primary
	err = runUpdate("org/cdk-infra", "cdk", "dev", "sha123", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not configured as a primary")
}

func TestUpdateCommand_ExternalDeployNotFound(t *testing.T) {
	// Create a temp directory with a manifest with external repos
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
    external:
      - repo: org/cdk-infra
        deploys:
          - name: cdk
            workflow: .github/workflows/deploy-cdk.yaml
  state:
    dev:
      sha: abc123
`
	err := os.WriteFile(manifestPath, []byte(manifest), 0644)
	require.NoError(t, err)

	// Set globals
	configPath = manifestPath
	manifestKey = "ci"

	// Run with non-existent deploy name
	err = runUpdate("org/cdk-infra", "nonexistent", "dev", "sha123", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found in config")
}

func TestUpdateCommand_SourceRepoMismatch(t *testing.T) {
	// Create a temp directory with a manifest with external repos
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
    external:
      - repo: org/cdk-infra
        deploys:
          - name: cdk
            workflow: .github/workflows/deploy-cdk.yaml
  state:
    dev:
      sha: abc123
`
	err := os.WriteFile(manifestPath, []byte(manifest), 0644)
	require.NoError(t, err)

	// Set globals
	configPath = manifestPath
	manifestKey = "ci"

	// Run with wrong source repo
	err = runUpdate("org/wrong-repo", "cdk", "dev", "sha123", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "source repo mismatch")
}

func TestUpdateCommand_EnvironmentNotFound(t *testing.T) {
	// Create a temp directory with a manifest with external repos
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
    external:
      - repo: org/cdk-infra
        deploys:
          - name: cdk
            workflow: .github/workflows/deploy-cdk.yaml
  state:
    dev:
      sha: abc123
`
	err := os.WriteFile(manifestPath, []byte(manifest), 0644)
	require.NoError(t, err)

	// Set globals
	configPath = manifestPath
	manifestKey = "ci"

	// Run with non-existent environment
	err = runUpdate("org/cdk-infra", "cdk", "nonexistent", "sha123", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "environment 'nonexistent' not found")
}

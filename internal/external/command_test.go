package external

import (
	"os"
	"os/exec"
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

// TestCommitWithApplicationRetry_PushFailureIncludesOutput asserts that when
// all push attempts fail the returned error includes the captured git push
// output so operators can diagnose push-rejection failures from logs alone
// without re-running the workflow.
func TestCommitWithApplicationRetry_PushFailureIncludesOutput(t *testing.T) {
	// Use a local bare repo as the fetch remote (so fetch succeeds) but configure
	// a non-existent path as the push URL (so push fails with a recognisable
	// "does not appear to be a git repository" message every attempt).
	remoteDir := t.TempDir()
	workDir := t.TempDir()

	gitRemote := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", remoteDir}, args...)...).CombinedOutput()
		require.NoErrorf(t, err, "git -C remote %v failed: %s", args, out)
	}
	gitWork := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", workDir}, args...)...).CombinedOutput()
		require.NoErrorf(t, err, "git -C work %v failed: %s", args, out)
	}

	// Seed the remote and working tree. Pin the working-tree branch to "main"
	// explicitly: `git init` honours the caller's init.defaultBranch config
	// (CI runners default to "master"), and commitWithApplicationRetry fetches
	// the current branch by name, so the local branch must match the remote ref
	// pushed below for the test to be hermetic.
	gitRemote("init", "--bare")
	gitWork("init")
	gitWork("checkout", "-B", "main")
	gitWork("config", "user.email", "test@example.com")
	gitWork("config", "user.name", "Test")
	gitWork("remote", "add", "origin", remoteDir)

	manifestPath := filepath.Join(workDir, "manifest.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte("initial: true\n"), 0644))
	gitWork("add", "manifest.yaml")
	gitWork("commit", "-m", "init")
	gitWork("push", "-u", "origin", "HEAD:main")

	// Redirect push to a non-existent path so every push fails with a message
	// while fetch from the real remote continues to succeed.
	gitWork("remote", "set-url", "--push", "origin", "/nonexistent-cascade-test-remote")

	// Change to the work dir so git commands in commitWithApplicationRetry run there.
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	callCount := 0
	applyUpdate := func() error {
		callCount++
		return os.WriteFile(manifestPath, []byte("value: "+string(rune('a'+callCount))+"\n"), 0644)
	}

	// Two attempts so the retry loop exhausts maxAttempts and reaches the final
	// "git push failed after N attempts" return path.
	err = commitWithApplicationRetry(manifestPath, "chore: test [skip ci]", 2, applyUpdate)
	require.Error(t, err, "must fail when all push attempts are rejected")

	// The error must carry the git push output so operators can diagnose the
	// failure without re-running the workflow.
	assert.Contains(t, err.Error(), "git push failed after 2 attempts",
		"error must state how many attempts were made")
	// git prints a diagnostic when the push URL is unreachable; verify it is
	// captured and included in the error (not silently discarded).
	assert.Contains(t, err.Error(), "does not appear to be a git repository",
		"error must include the captured git push output so failures are diagnosable from logs")
}

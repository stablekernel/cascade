package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()

	assert.Equal(t, "generate-workflow", cmd.Use)
	assert.Contains(t, cmd.Short, "Generate orchestration workflow")

	// Verify flags exist
	configFlag := cmd.Flags().Lookup("config")
	assert.NotNil(t, configFlag)
	assert.Equal(t, "", configFlag.DefValue) // Empty by default, auto-detects manifest.yaml at runtime

	outputFlag := cmd.Flags().Lookup("output")
	assert.NotNil(t, outputFlag)
	assert.Equal(t, ".github/workflows/orchestrate.yaml", outputFlag.DefValue)

	promoteOutputFlag := cmd.Flags().Lookup("promote-output")
	assert.NotNil(t, promoteOutputFlag)
	assert.Equal(t, ".github/workflows/promote.yaml", promoteOutputFlag.DefValue)

	validateOnlyFlag := cmd.Flags().Lookup("validate-only")
	assert.NotNil(t, validateOnlyFlag)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	assert.NotNil(t, dryRunFlag)

	forceFlag := cmd.Flags().Lookup("force")
	assert.NotNil(t, forceFlag)

	commitFlag := cmd.Flags().Lookup("commit")
	assert.NotNil(t, commitFlag)

	pushFlag := cmd.Flags().Lookup("push")
	assert.NotNil(t, pushFlag)
	assert.Equal(t, "p", pushFlag.Shorthand)

	orchestrateOnlyFlag := cmd.Flags().Lookup("orchestrate-only")
	assert.NotNil(t, orchestrateOnlyFlag)

	promoteOnlyFlag := cmd.Flags().Lookup("promote-only")
	assert.NotNil(t, promoteOnlyFlag)
}

// Helper to create generateOptions with defaults
func defaultOpts(configPath, outputPath string) generateOptions {
	return generateOptions{
		configPath:        configPath,
		outputPath:        outputPath,
		promoteOutputPath: filepath.Join(filepath.Dir(outputPath), "promote.yaml"),
		orchestrateOnly:   true, // Default to orchestrate-only for existing tests
	}
}

// validManifestContent returns a valid manifest with ci: key at top level
const validManifestContent = `ci:
  config:
    project: test-project
    trunk_branch: main
    environments: [dev]
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
        triggers: ["src/**"]
`

// invalidManifestContent returns an invalid manifest (missing build workflow)
const invalidManifestContent = `ci:
  config:
    trunk_branch: main
    builds:
      - name: app
        triggers: ["src/**"]
`

func TestRunGenerateWorkflow_ValidateOnly(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid manifest with ci: key at top level
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	err := os.WriteFile(configPath, []byte(validManifestContent), 0644)
	require.NoError(t, err)

	// Create workflow file
	workflowDir := filepath.Join(tmpDir, ".github/workflows")
	err = os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	err = os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	// Run validate-only
	opts := defaultOpts(configPath, "")
	opts.validateOnly = true
	err = runGenerateWorkflow(opts)
	assert.NoError(t, err)
}

func TestRunGenerateWorkflow_DryRun(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid manifest with ci: key at top level
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	err := os.WriteFile(configPath, []byte(validManifestContent), 0644)
	require.NoError(t, err)

	// Create workflow file
	workflowDir := filepath.Join(tmpDir, ".github/workflows")
	err = os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	err = os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	// Run with dry-run (output goes to stdout)
	opts := defaultOpts(configPath, "")
	opts.dryRun = true
	err = runGenerateWorkflow(opts)
	assert.NoError(t, err)
}

func TestRunGenerateWorkflow_FileExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid manifest with ci: key at top level
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	err := os.WriteFile(configPath, []byte(validManifestContent), 0644)
	require.NoError(t, err)

	// Create workflow file
	workflowDir := filepath.Join(tmpDir, ".github/workflows")
	err = os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	err = os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	// Create existing output file
	outputPath := filepath.Join(tmpDir, "output.yaml")
	err = os.WriteFile(outputPath, []byte("existing"), 0644)
	require.NoError(t, err)

	// Run without force - should fail
	opts := defaultOpts(configPath, outputPath)
	err = runGenerateWorkflow(opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exists")

	// Run with force - should succeed
	opts.force = true
	err = runGenerateWorkflow(opts)
	assert.NoError(t, err)

	// Verify file was overwritten
	content, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "AUTO-GENERATED")
}

func TestRunGenerateWorkflow_InvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Create invalid config (missing build workflow)
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	err := os.WriteFile(configPath, []byte(invalidManifestContent), 0644)
	require.NoError(t, err)

	opts := defaultOpts(configPath, "")
	opts.validateOnly = true
	err = runGenerateWorkflow(opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")
}

func TestRunGenerateWorkflow_ConfigNotFound(t *testing.T) {
	opts := defaultOpts("/nonexistent/path/config.yaml", "")
	opts.validateOnly = true
	err := runGenerateWorkflow(opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config")
}

func TestRunGenerateWorkflow_WithCommit(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize a git repo in the temp dir
	initCmd := exec.Command("git", "init")
	initCmd.Dir = tmpDir
	require.NoError(t, initCmd.Run())

	// Configure git user for commits
	configCmd := exec.Command("git", "config", "user.email", "test@test.com")
	configCmd.Dir = tmpDir
	require.NoError(t, configCmd.Run())

	configCmd2 := exec.Command("git", "config", "user.name", "Test User")
	configCmd2.Dir = tmpDir
	require.NoError(t, configCmd2.Run())

	// Create a valid manifest with ci: key at top level
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	err := os.WriteFile(configPath, []byte(validManifestContent), 0644)
	require.NoError(t, err)

	// Create workflow file
	workflowDir := filepath.Join(tmpDir, ".github/workflows")
	err = os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	err = os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	// Output path for generated workflow
	outputPath := filepath.Join(workflowDir, "orchestrate.yaml")

	// Change to tmpDir for git operations
	oldWd, _ := os.Getwd()
	err = os.Chdir(tmpDir)
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWd) }()

	// Run with commit flag (no push)
	opts := defaultOpts(configPath, outputPath)
	opts.commit = true
	err = runGenerateWorkflow(opts)
	assert.NoError(t, err)

	// Verify file was created
	_, err = os.Stat(outputPath)
	assert.NoError(t, err)

	// Verify commit was created
	logCmd := exec.Command("git", "log", "--oneline", "-1")
	logCmd.Dir = tmpDir
	output, err := logCmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "regenerate orchestrate.yaml")
}

func TestRunGenerateWorkflow_CommitNoChanges(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize a git repo in the temp dir
	initCmd := exec.Command("git", "init")
	initCmd.Dir = tmpDir
	require.NoError(t, initCmd.Run())

	// Configure git user for commits
	configCmd := exec.Command("git", "config", "user.email", "test@test.com")
	configCmd.Dir = tmpDir
	require.NoError(t, configCmd.Run())

	configCmd2 := exec.Command("git", "config", "user.name", "Test User")
	configCmd2.Dir = tmpDir
	require.NoError(t, configCmd2.Run())

	// Create a valid manifest with ci: key at top level
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	err := os.WriteFile(configPath, []byte(validManifestContent), 0644)
	require.NoError(t, err)

	// Create workflow file
	workflowDir := filepath.Join(tmpDir, ".github/workflows")
	err = os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	err = os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	outputPath := filepath.Join(workflowDir, "orchestrate.yaml")

	// Change to tmpDir for git operations
	oldWd, _ := os.Getwd()
	err = os.Chdir(tmpDir)
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWd) }()

	// First run creates the file and commits it
	opts := defaultOpts(configPath, outputPath)
	opts.commit = true
	err = runGenerateWorkflow(opts)
	require.NoError(t, err)

	// Second run should detect no changes (same content)
	opts.force = true
	err = runGenerateWorkflow(opts)
	assert.NoError(t, err)

	// Verify only one commit exists (the initial one)
	logCmd := exec.Command("git", "log", "--oneline")
	logCmd.Dir = tmpDir
	output, err := logCmd.Output()
	require.NoError(t, err)
	// Should only have one line (one commit)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	assert.Len(t, lines, 1)
}

func TestRunGenerateWorkflow_WithPush(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize a git repo in the temp dir
	initCmd := exec.Command("git", "init")
	initCmd.Dir = tmpDir
	require.NoError(t, initCmd.Run())

	// Configure git user for commits
	configCmd := exec.Command("git", "config", "user.email", "test@test.com")
	configCmd.Dir = tmpDir
	require.NoError(t, configCmd.Run())

	configCmd2 := exec.Command("git", "config", "user.name", "Test User")
	configCmd2.Dir = tmpDir
	require.NoError(t, configCmd2.Run())

	// Create a valid manifest with ci: key at top level
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	err := os.WriteFile(configPath, []byte(validManifestContent), 0644)
	require.NoError(t, err)

	// Create workflow file
	workflowDir := filepath.Join(tmpDir, ".github/workflows")
	err = os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	err = os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	outputPath := filepath.Join(workflowDir, "orchestrate.yaml")

	// Change to tmpDir for git operations
	oldWd, _ := os.Getwd()
	err = os.Chdir(tmpDir)
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWd) }()

	// Run with push flag - should fail because there's no remote
	// This tests that push is attempted after commit
	opts := defaultOpts(configPath, outputPath)
	opts.commit = true
	opts.push = true
	err = runGenerateWorkflow(opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pushing to remote")

	// Verify file was created and committed before push failed
	_, err = os.Stat(outputPath)
	assert.NoError(t, err)

	logCmd := exec.Command("git", "log", "--oneline", "-1")
	logCmd.Dir = tmpDir
	output, err := logCmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "regenerate orchestrate.yaml")
}

package version

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionNewCommand_Structure(t *testing.T) {
	cmd := NewCommand()

	assert.Equal(t, "next-version", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)

	assert.NotNil(t, cmd.Flags().Lookup("config"))
	assert.NotNil(t, cmd.Flags().Lookup("environment"))
	assert.NotNil(t, cmd.Flags().Lookup("base-sha"))
	assert.NotNil(t, cmd.Flags().Lookup("head-sha"))
	assert.NotNil(t, cmd.Flags().Lookup("json"))
}

func TestVersionNewCommand_RunE_BadConfigPath(t *testing.T) {
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--environment", "dev",
		"--config", "/nonexistent/path/manifest.yaml",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading config")
}

func TestVersionNewCommand_RunE_MissingEnvironmentFlag(t *testing.T) {
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// environment is required; omitting it produces an error before RunE
	cmd.SetArgs([]string{"--config", "/some/path.yaml"})
	err := cmd.Execute()
	assert.Error(t, err)
}

// minimalConfig returns a valid manifest.yaml in a temp dir suitable for
// exercising the version command RunE without hitting infrastructure.
func minimalVersionConfig(t *testing.T, environments []string) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	// Build environments list
	envLines := ""
	for _, e := range environments {
		envLines += "      - " + e + "\n"
	}

	content := "ci:\n  config:\n    trunk_branch: main\n    environments:\n" + envLines
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))
	return configPath
}

func TestVersionNewCommand_RunE_EnvironmentNotFound(t *testing.T) {
	configPath := minimalVersionConfig(t, []string{"dev", "test"})

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--environment", "staging",
		"--config", configPath,
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "environment")
}

// versionStateRepo builds a hermetic git repo (a chore base commit, then a
// feat commit at HEAD), changes into it, and writes a manifest whose recorded
// state has dev at v1.2.0-rc.1 and test at v1.1.0 anchored to the base commit.
// Returns the manifest path.
func versionStateRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	runGitV(t, "init", "-b", "main")
	runGitV(t, "config", "user.email", "test@example.com")
	runGitV(t, "config", "user.name", "Test User")
	runGitV(t, "config", "commit.gpgsign", "false")

	writeCommitV(t, "README.md", "base", "chore: seed")
	baseSHA := gitOutV(t, "rev-parse", "HEAD")
	writeCommitV(t, "login.go", "package main", "feat: add login flow")

	configPath := filepath.Join(dir, "manifest.yaml")
	manifest := "ci:\n" +
		"  config:\n" +
		"    trunk_branch: main\n" +
		"    environments:\n" +
		"      - dev\n" +
		"      - test\n" +
		"  state:\n" +
		"    dev:\n" +
		"      version: v1.2.0-rc.1\n" +
		"    test:\n" +
		"      version: v1.1.0\n" +
		"      sha: " + baseSHA + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(manifest), 0o600))
	return configPath
}

func gitOutV(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).Output()
	require.NoError(t, err, "git %v", args)
	return strings.TrimSpace(string(out))
}

// executeVersionCapture runs next-version with args and returns what it wrote
// to os.Stdout (the command prints there directly, not to the cobra writer).
func executeVersionCapture(t *testing.T, args []string) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	cmd := NewCommand()
	var cmdOut bytes.Buffer
	cmd.SetOut(&cmdOut)
	cmd.SetErr(&cmdOut)
	cmd.SetArgs(args)
	runErr := cmd.Execute()

	require.NoError(t, w.Close())
	data, readErr := io.ReadAll(r)
	os.Stdout = orig
	require.NoError(t, readErr)
	return string(data), runErr
}

// TestVersionNewCommand_RunE_TextOutput_IncrementsRCOnSameBase pins the text
// path end to end: the feat commit in HEAD~1..HEAD bumps test's v1.1.0 to base
// v1.2.0, and dev's recorded v1.2.0-rc.1 on that same base yields rc.2.
func TestVersionNewCommand_RunE_TextOutput_IncrementsRCOnSameBase(t *testing.T) {
	configPath := versionStateRepo(t)

	out, err := executeVersionCapture(t, []string{
		"--environment", "dev",
		"--config", configPath,
		"--base-sha", "HEAD~1",
		"--head-sha", "HEAD",
	})
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0-rc.2\n", out)
}

// TestVersionNewCommand_RunE_JSONOutput_FullShape pins every field of the JSON
// payload. --base-sha is omitted so the range defaults to the next env's
// recorded SHA (the base commit), which must yield the same single feat commit.
func TestVersionNewCommand_RunE_JSONOutput_FullShape(t *testing.T) {
	configPath := versionStateRepo(t)

	out, err := executeVersionCapture(t, []string{
		"--environment", "dev",
		"--config", configPath,
		"--json",
	})
	require.NoError(t, err)

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, map[string]interface{}{
		"version":           "v1.2.0-rc.2",
		"base_version":      "v1.2.0",
		"current_dev":       "v1.2.0-rc.1",
		"next_env":          "v1.1.0",
		"bump_type":         "minor",
		"commit_count":      float64(1),
		"release_candidate": float64(2),
	}, got)
}

// TestVersionNewCommand_RunE_NoState_StartsAtFirstRC pins the fallback path:
// with no recorded state and no --base-sha the range starts at the initial
// commit, the fix commit bumps v0.0.0 to a patch, the v0.1.0 minimum floor
// applies, and the empty dev version starts the counter at rc.0.
func TestVersionNewCommand_RunE_NoState_StartsAtFirstRC(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	runGitV(t, "init", "-b", "main")
	runGitV(t, "config", "user.email", "test@example.com")
	runGitV(t, "config", "user.name", "Test User")
	runGitV(t, "config", "commit.gpgsign", "false")
	writeCommitV(t, "README.md", "base", "chore: seed")
	writeCommitV(t, "main.go", "package main", "fix: correct rounding")

	configPath := filepath.Join(dir, "manifest.yaml")
	manifest := "ci:\n" +
		"  config:\n" +
		"    trunk_branch: main\n" +
		"    environments:\n" +
		"      - dev\n" +
		"      - test\n"
	require.NoError(t, os.WriteFile(configPath, []byte(manifest), 0o600))

	out, err := executeVersionCapture(t, []string{
		"--environment", "dev",
		"--config", configPath,
	})
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0-rc.0\n", out)
}

func TestVersionNewCommand_RunE_NoConfigFlag(t *testing.T) {
	// When --config is omitted, FindConfigFile is called to locate a manifest.
	// From the package test directory there is no .github/manifest.yaml, so
	// config.Parse fails with "loading config".
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--environment", "dev"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading config")
}

func TestBumpTypeString(t *testing.T) {
	tests := []struct {
		bump BumpType
		want string
	}{
		{BumpMajor, "major"},
		{BumpMinor, "minor"},
		{BumpPatch, "patch"},
		{BumpNone, "none"},
		{BumpType(99), "none"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, bumpTypeString(tt.bump))
		})
	}
}

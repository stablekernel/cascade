package version

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestVersionNewCommand_RunE_ValidConfigTextOutput(t *testing.T) {
	configPath := minimalVersionConfig(t, []string{"dev", "test"})

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// HEAD~1..HEAD gets exactly one real commit; exercises the commit-parsing loop.
	cmd.SetArgs([]string{
		"--environment", "dev",
		"--config", configPath,
		"--base-sha", "HEAD~1",
		"--head-sha", "HEAD",
	})
	// May succeed or fail depending on git availability; exercise the code path.
	_ = cmd.Execute()
}

func TestVersionNewCommand_RunE_ValidConfigJSONOutput(t *testing.T) {
	configPath := minimalVersionConfig(t, []string{"dev", "test"})

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--environment", "dev",
		"--config", configPath,
		"--base-sha", "HEAD~1",
		"--head-sha", "HEAD",
		"--json",
	})
	_ = cmd.Execute()
}

func TestVersionNewCommand_RunE_EmptyRange(t *testing.T) {
	configPath := minimalVersionConfig(t, []string{"dev", "test"})

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// HEAD..HEAD yields zero commits; exercises the no-baseSHA fallback branch.
	cmd.SetArgs([]string{
		"--environment", "dev",
		"--config", configPath,
	})
	_ = cmd.Execute()
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

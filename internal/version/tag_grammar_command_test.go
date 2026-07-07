package version

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout redirects os.Stdout for the duration of f and returns what was
// written. The next-version command prints its result to os.Stdout directly, so
// asserting the emitted version requires capturing the real stream.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	f()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

// The next-version command honors a manifest tag_grammar: with a custom prefix
// and pre-release token, and an empty base..head range so the calculation is
// deterministic, it emits the next version in the custom shape.
func TestVersionNewCommand_HonorsTagGrammar(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")
	content := `ci:
  config:
    trunk_branch: main
    tag_grammar:
      prefix: ver
      prerelease_token: beta
    environments:
      - dev
      - test
  state:
    dev:
      version: v1.0.0-rc.3
    test:
      version: v0.9.0
      sha: HEAD
`
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--environment", "dev",
		"--config", configPath,
		"--base-sha", "HEAD",
		"--head-sha", "HEAD",
		"--json",
	})

	var runErr error
	stdout := captureStdout(t, func() { runErr = cmd.Execute() })
	require.NoError(t, runErr)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &result))
	got, _ := result["version"].(string)
	assert.Equal(t, "ver0.9.0-beta.0", got)
}

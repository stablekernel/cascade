package branchprotection

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/generate"
)

// minimalManifest is a hand-written manifest with validate, two builds, and a
// deploy so the integration path exercises reusable-caller handling end to end.
const minimalManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - staging
      - production
    validate:
      workflow: .github/workflows/validate.yaml
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
    deploys:
      - name: services
        workflow: .github/workflows/deploy.yaml
`

// writeManifest writes minimalManifest into a temp .github/manifest.yaml and
// returns its path.
func writeManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ghDir := filepath.Join(dir, ".github")
	require.NoError(t, os.MkdirAll(ghDir, 0o755))
	path := filepath.Join(ghDir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(minimalManifest), 0o644))
	return path
}

// TestCommand_EmitsSafePayloadToStdout runs the REAL branch-protection command
// against a temp manifest, captures stdout, and asserts the emitted JSON parses
// and satisfies the safety invariant end to end.
//
// This change emits no workflow and no schema field, so a Docker e2e/scenarios
// scenario (testcontainers + gitea + act over committed workflow fixtures) does
// not fit. This Go integration test follows the verify_clean_test.go precedent of
// driving the real command path instead.
func TestCommand_EmitsSafePayloadToStdout(t *testing.T) {
	path := writeManifest(t)

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", path})
	require.NoError(t, cmd.Execute())

	var p Payload
	require.NoError(t, json.Unmarshal(out.Bytes(), &p))

	// Safety invariant: required contexts are exactly Setup and Finalize.
	assert.ElementsMatch(t,
		[]string{generate.SetupJobName, generate.FinalizeJobName},
		p.Protection.RequiredStatusChecks.Contexts)

	// No bare reusable-caller DisplayName is required.
	for _, bare := range []string{"Validate (validate)", "Build (app)", "Deploy (services)"} {
		assert.NotContains(t, p.Protection.RequiredStatusChecks.Contexts, bare)
		assert.Contains(t, p.OperatorTodo.CompleteTheseContexts, bare+" / "+innerJobPlaceholder)
	}
}

// TestCommand_WritesToOutputFile confirms --output writes the payload to a file.
func TestCommand_WritesToOutputFile(t *testing.T) {
	path := writeManifest(t)
	outPath := filepath.Join(t.TempDir(), "branch-protection.json")

	cmd := NewCommand()
	cmd.SetArgs([]string{"--config", path, "--output", outPath})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	var p Payload
	require.NoError(t, json.Unmarshal(data, &p))
	assert.ElementsMatch(t,
		[]string{generate.SetupJobName, generate.FinalizeJobName},
		p.Protection.RequiredStatusChecks.Contexts)
}

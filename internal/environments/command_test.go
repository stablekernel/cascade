package environments

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalManifest is a hand-written manifest declaring two environments, one of
// them (production) carrying inline per-environment settings exercising the
// additive fields, so the integration path runs the real parse -> validate ->
// build -> emit chain end to end.
const minimalManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - staging
      - name: production
        gha_environment: prod
        required_reviewers: [octocat, team/ops]
        wait_timer: 15
        branch_policy: protected
        secrets: [MY_SECRET]
        variables: [REGION]
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

// TestCommand_EmitsPayloadToStdout runs the REAL environments command against a
// temp manifest, captures stdout, and asserts the emitted JSON parses and is in
// manifest order with the expected per-environment config.
//
// This change emits no workflow, so a Docker e2e/scenarios scenario (which
// drives committed workflow fixtures) is added separately for the parse + drift
// guarantee; this Go integration test drives the real command path, following
// the branch-protection command_test.go precedent.
func TestCommand_EmitsPayloadToStdout(t *testing.T) {
	path := writeManifest(t)

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", path})
	require.NoError(t, cmd.Execute())

	var p Payload
	require.NoError(t, json.Unmarshal(out.Bytes(), &p))

	require.Len(t, p.Environments, 2)
	// Manifest order: staging before production.
	assert.Equal(t, "staging", p.Environments[0].Name)
	assert.Equal(t, "production", p.Environments[1].Name)

	prod := p.Environments[1]
	assert.Equal(t, "prod", prod.GHAEnvironment)
	assert.Equal(t, 15, prod.Environment.WaitTimer)
	require.NotNil(t, prod.Environment.DeploymentBranchPolicy)
	assert.True(t, prod.Environment.DeploymentBranchPolicy.ProtectedBranches)
	assert.Equal(t, []string{"octocat", "team/ops"}, prod.OperatorTodo.RequiredReviewers)
	assert.Equal(t, []string{"MY_SECRET"}, prod.OperatorTodo.Secrets)
	assert.Equal(t, []string{"REGION"}, prod.OperatorTodo.Variables)
}

// TestCommand_WritesToOutputFile confirms --output writes the payload to a file.
func TestCommand_WritesToOutputFile(t *testing.T) {
	path := writeManifest(t)
	outPath := filepath.Join(t.TempDir(), "environments.json")

	cmd := NewCommand()
	cmd.SetArgs([]string{"--config", path, "--output", outPath})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	var p Payload
	require.NoError(t, json.Unmarshal(data, &p))
	require.Len(t, p.Environments, 2)
	assert.Equal(t, "staging", p.Environments[0].Name)
}

// TestCommand_StdoutAndFileMatch confirms stdout and --output produce identical
// bytes for the same manifest (the drift guarantee through the command path).
func TestCommand_StdoutAndFileMatch(t *testing.T) {
	path := writeManifest(t)
	outPath := filepath.Join(t.TempDir(), "environments.json")

	stdoutCmd := NewCommand()
	var stdout bytes.Buffer
	stdoutCmd.SetOut(&stdout)
	stdoutCmd.SetArgs([]string{"--config", path})
	require.NoError(t, stdoutCmd.Execute())

	fileCmd := NewCommand()
	fileCmd.SetArgs([]string{"--config", path, "--output", outPath})
	require.NoError(t, fileCmd.Execute())

	fileBytes, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, stdout.String(), string(fileBytes))
}

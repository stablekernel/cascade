package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
)

// callbackWorkflowDir writes a minimal reusable callback workflow into a temp
// repo layout so the generator's output discovery can read it.
func callbackWorkflowDir(t *testing.T, names ...string) string {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0o755))
	stub := `on:
  workflow_call:
    inputs:
      environment:
        required: false
        type: string
`
	for _, n := range names {
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows", n), []byte(stub), 0o644))
	}
	return tmpDir
}

// requireValidYAML asserts the generated document parses with yaml.v3, the
// same bar every emitted workflow must clear at GitHub's parser.
func requireValidYAML(t *testing.T, content string) map[string]interface{} {
	t.Helper()
	var doc map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(content), &doc), "generated workflow must be valid YAML:\n%s", content)
	return doc
}

// TestWriteExtraTriggers_WorkflowRunApostropheName_EscapedAndParseable pins
// the M2 fix: a workflow display name with an apostrophe ("Bob's CI") is a
// legitimate operator value; the emitter must single-quote-escape it so the
// generated document stays parseable and the value round-trips.
func TestWriteExtraTriggers_WorkflowRunApostropheName_EscapedAndParseable(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		ExtraTriggers: &config.ExtraTriggers{
			WorkflowRun: &config.WorkflowRunTrigger{
				Workflows: []string{"Bob's CI"},
				Types:     []string{"completed"},
			},
		},
	}
	require.Empty(t, config.Validate(cfg), "an apostrophe workflow name is legitimate and must validate clean")

	content, err := NewGenerator(cfg, callbackWorkflowDir(t)).Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "      - 'Bob''s CI'\n",
		"the workflow name must be emitted as an escaped single-quoted scalar")

	doc := requireValidYAML(t, content)
	on, ok := doc["on"].(map[string]interface{})
	if !ok {
		// yaml.v3 may parse the bare `on:` key as boolean true.
		on = doc["true"].(map[string]interface{})
	}
	wr := on["workflow_run"].(map[string]interface{})
	workflows := wr["workflows"].([]interface{})
	assert.Equal(t, "Bob's CI", workflows[0], "the workflow name must round-trip intact")
}

// TestOrchestratePathsFilter_ApostrophePattern_EscapedAndParseable pins the
// paths-filter sibling of the same class: a glob containing an apostrophe must
// be escaped, not spliced raw into the single-quoted sequence item.
func TestOrchestratePathsFilter_ApostrophePattern_EscapedAndParseable(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: "build.yaml", Triggers: []string{"src/it's/**"}},
		},
	}
	require.Empty(t, config.Validate(cfg))

	content, err := NewGenerator(cfg, callbackWorkflowDir(t, "build.yaml")).Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "      - 'src/it''s/**'\n")
	requireValidYAML(t, content)
}

// TestNativeDeployments_EnvironmentURLDollar_SingleQuotedLiteral pins the M6
// fix: a URL with a `$` in its query (OData-style) is legitimate; the emitted
// shell assignment must single-quote it so the shell never expands it, instead
// of silently eating `$top` inside double quotes.
func TestNativeDeployments_EnvironmentURLDollar_SingleQuotedLiteral(t *testing.T) {
	url := "https://app.example.com/?$top=1&$filter=x"
	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Environments: []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{EnvironmentURL: url}},
		},
		Deployments: &config.DeploymentsConfig{Enabled: boolPtr(true)},
	}
	require.Empty(t, config.Validate(cfg), "a $ in a URL query is legitimate and must validate clean")

	content, err := NewGenerator(cfg, callbackWorkflowDir(t)).Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "environment_url='"+url+"' ;;",
		"the URL must be emitted inside single quotes so $ stays literal")
	assert.NotContains(t, content, "environment_url=\""+url+"\"",
		"the URL must no longer sit inside a double-quoted assignment")
	requireValidYAML(t, content)

	// The promote workflow carries the same sink.
	promote, err := NewPromoteGenerator(cfg, "").Generate()
	require.NoError(t, err)
	if strings.Contains(promote, "environment_url=") {
		assert.Contains(t, promote, "environment_url='"+url+"' ;;")
	}
}

package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
)

// requireShellParseable writes script to a temp file and asserts bash -n
// accepts it. The generated matrix-building step must stay parseable no
// matter what characters an operator puts in an input value.
func requireShellParseable(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "step.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o600))
	out, err := exec.Command("bash", "-n", path).CombinedOutput()
	require.NoError(t, err, "bash -n rejected the emitted run script:\n%s\nscript:\n%s", out, script)
}

// findStep parses a generated workflow and returns the step with the given
// name from any job, proving along the way that the document is valid YAML.
func findStep(t *testing.T, content, stepName string) map[string]interface{} {
	t.Helper()
	var doc struct {
		Jobs map[string]struct {
			Steps []map[string]interface{} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(content), &doc), "generated workflow must be valid YAML")
	for _, job := range doc.Jobs {
		for _, step := range job.Steps {
			if step["name"] == stepName {
				return step
			}
		}
	}
	require.Failf(t, "step not found", "step %q not present in generated workflow", stepName)
	return nil
}

// TestBuildDeployMatrices_ApostropheInputValue_EnvRoutedAndShellParseable
// covers the promote matrix input sink: an ordinary human value containing an
// apostrophe (and any crafted value) must never sit inside a shell quote
// literal in the emitted run script. The JSON blobs are routed through the
// step env: map and referenced as quoted shell variables, so the script
// parses regardless of input content.
func TestBuildDeployMatrices_ApostropheInputValue_EnvRoutedAndShellParseable(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Inputs: map[string]interface{}{
					"message": "it's live",
				},
				EnvInputs: map[string]map[string]interface{}{
					"prod": {"message": "don't y'all stop"},
				},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	step := findStep(t, content, "Build Deploy Matrices")

	// The run script must be shell-parseable even with apostrophes in values.
	run, ok := step["run"].(string)
	require.True(t, ok, "Build Deploy Matrices step has no run script")
	requireShellParseable(t, run)

	// The JSON reaches the shell through env: indirection, never through a
	// quoted literal inside the script.
	env, ok := step["env"].(map[string]interface{})
	require.True(t, ok, "Build Deploy Matrices step has no env map")
	assert.Equal(t, `{"message":"it's live"}`, env["DEFAULT_INPUTS_APP"])
	assert.Equal(t, `{"prod":{"message":"don't y'all stop"}}`, env["ENV_INPUTS_APP"])
	assert.NotContains(t, run, "DEFAULT_INPUTS='")
	assert.NotContains(t, run, "ENV_INPUTS='")
	assert.Contains(t, run, `DEFAULT_INPUTS="$DEFAULT_INPUTS_APP"`)
	assert.Contains(t, run, `ENV_INPUTS="$ENV_INPUTS_APP"`)
}

// TestBuildDeployMatrices_PlainInputValues_ShellParseable pins the same
// guarantee for apostrophe-free values across multiple deploys: the script
// still parses and each deploy gets its own env-routed JSON pair.
func TestBuildDeployMatrices_PlainInputValues_ShellParseable(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Inputs: map[string]interface{}{
					"cluster": "dev-eks",
				},
				EnvInputs: map[string]map[string]interface{}{
					"prod": {"cluster": "prod-eks"},
				},
			},
			{
				Name:     "infra-core",
				Workflow: ".github/workflows/deploy-infra.yaml",
				Inputs: map[string]interface{}{
					"stack": "main",
				},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	step := findStep(t, content, "Build Deploy Matrices")
	run, ok := step["run"].(string)
	require.True(t, ok, "Build Deploy Matrices step has no run script")
	requireShellParseable(t, run)

	env, ok := step["env"].(map[string]interface{})
	require.True(t, ok, "Build Deploy Matrices step has no env map")
	assert.Equal(t, `{"cluster":"dev-eks"}`, env["DEFAULT_INPUTS_APP"])
	assert.Equal(t, `{"prod":{"cluster":"prod-eks"}}`, env["ENV_INPUTS_APP"])
	assert.Equal(t, `{"stack":"main"}`, env["DEFAULT_INPUTS_INFRA_CORE"])
	assert.Equal(t, `{}`, env["ENV_INPUTS_INFRA_CORE"])
}

// TestGenerator_DispatchInputDefaultWithApostrophe_ValidYAML covers the
// dispatch-input default sink: an apostrophe in a default value must produce
// a valid YAML scalar (single-quote style doubles embedded quotes), so the
// emitted orchestrate workflow parses and the value round-trips intact.
func TestGenerator_DispatchInputDefaultWithApostrophe_ValidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0o644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		DispatchInputs: map[string]config.DispatchInput{
			"greeting": {
				Type:        config.DispatchInputTypeString,
				Default:     "it's a default",
				Description: "operator note",
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	var doc struct {
		On struct {
			WorkflowDispatch struct {
				Inputs map[string]struct {
					Default interface{} `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_dispatch"`
		} `yaml:"on"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(result), &doc), "generated orchestrate workflow must be valid YAML")
	require.Contains(t, doc.On.WorkflowDispatch.Inputs, "greeting")
	assert.Equal(t, "it's a default", doc.On.WorkflowDispatch.Inputs["greeting"].Default)
}

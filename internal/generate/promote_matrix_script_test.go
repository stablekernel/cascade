package generate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
)

// extractStepRun parses a generated workflow and returns the run script of the
// step with the given id, failing the test if no such step exists.
func extractStepRun(t *testing.T, workflowYAML, stepID string) string {
	t.Helper()

	var wf map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(workflowYAML), &wf))

	jobs, ok := wf["jobs"].(map[string]interface{})
	require.True(t, ok, "generated workflow has no jobs map")

	for _, j := range jobs {
		job, ok := j.(map[string]interface{})
		if !ok {
			continue
		}
		steps, ok := job["steps"].([]interface{})
		if !ok {
			continue
		}
		for _, s := range steps {
			step, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			if step["id"] == stepID {
				run, ok := step["run"].(string)
				require.True(t, ok && run != "", "step %q has no run script", stepID)
				return run
			}
		}
	}
	t.Fatalf("step %q not found in generated workflow", stepID)
	return ""
}

// TestGeneratedMatrixScript_ResolvesDeployInputsAtRuntime executes the emitted
// "Build Deploy Matrices" bash step, the production path that resolves deploy
// inputs at promotion time. It pins the runtime semantics: defaults merged with
// env_inputs overrides per promotion (prod gets prod-cluster, others keep the
// default) and ${{ matrix.* }} placeholders substituted per promotion entry.
func TestGeneratedMatrixScript_ResolvesDeployInputsAtRuntime(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the emitted matrix script requires it")
	}

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "test", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Inputs: map[string]interface{}{
					"environment": "${{ matrix.environment }}",
					"sha":         "${{ matrix.sha }}",
					"cluster":     "dev-cluster",
				},
				EnvInputs: map[string]map[string]interface{}{
					"prod": {"cluster": "prod-cluster"},
				},
			},
			{
				Name:     "infra",
				Workflow: ".github/workflows/deploy-infra.yaml",
				Inputs: map[string]interface{}{
					"environment": "${{ matrix.environment }}",
					"sha":         "${{ matrix.sha }}",
					"image_tag":   "${{ matrix.version }}",
				},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	script := extractStepRun(t, content, "build-matrices")

	// runScript executes the emitted step exactly as GitHub Actions would
	// (bash -e), feeding the preflight promotion result through the same env
	// variable the generated env: block wires up, and returns the parsed
	// GITHUB_OUTPUT key/value pairs.
	runScript := func(t *testing.T, promotionResult string) map[string]string {
		t.Helper()
		outFile := filepath.Join(t.TempDir(), "github_output")
		require.NoError(t, os.WriteFile(outFile, nil, 0o600))

		cmd := exec.Command("bash", "-e", "-c", script)
		cmd.Env = append(os.Environ(),
			"PROMOTION_RESULT="+promotionResult,
			"GITHUB_OUTPUT="+outFile,
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "emitted matrix script failed:\n%s", out)

		data, err := os.ReadFile(outFile)
		require.NoError(t, err)
		outputs := make(map[string]string)
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				outputs[k] = v
			}
		}
		return outputs
	}

	decodeMatrix := func(t *testing.T, raw string) []map[string]interface{} {
		t.Helper()
		var matrix []map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(raw), &matrix), "matrix output is not valid JSON: %s", raw)
		return matrix
	}

	t.Run("merges env overrides and substitutes matrix variables per promotion", func(t *testing.T) {
		promotionResult := `{"promotions":[` +
			`{"environment":"dev","sha":"aaa111","version":"v1.0.0-rc.1"},` +
			`{"environment":"test","sha":"aaa111","version":"v1.0.0-rc.1"},` +
			`{"environment":"prod","sha":"bbb222","version":"v1.0.0-rc.2"}]}`

		outputs := runScript(t, promotionResult)

		assert.Equal(t, []map[string]interface{}{
			{"cluster": "dev-cluster", "environment": "dev", "sha": "aaa111"},
			{"cluster": "dev-cluster", "environment": "test", "sha": "aaa111"},
			{"cluster": "prod-cluster", "environment": "prod", "sha": "bbb222"},
		}, decodeMatrix(t, outputs["deploy_app_matrix"]))

		assert.Equal(t, []map[string]interface{}{
			{"environment": "dev", "image_tag": "v1.0.0-rc.1", "sha": "aaa111"},
			{"environment": "test", "image_tag": "v1.0.0-rc.1", "sha": "aaa111"},
			{"environment": "prod", "image_tag": "v1.0.0-rc.2", "sha": "bbb222"},
		}, decodeMatrix(t, outputs["deploy_infra_matrix"]))
	})

	t.Run("empty promotions produce empty matrices", func(t *testing.T) {
		outputs := runScript(t, `{"promotions":[]}`)
		assert.Empty(t, decodeMatrix(t, outputs["deploy_app_matrix"]))
		assert.Empty(t, decodeMatrix(t, outputs["deploy_infra_matrix"]))
	})
}

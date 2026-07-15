package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// jobResultInCallOutput matches a `jobs.<id>.result` reference. Inside
// on.workflow_call.outputs.<id>.value the jobs context exposes ONLY outputs,
// so a .result reference silently resolves to the empty string at runtime and
// any caller gating on the output (the generated orchestrate's
// `<job>_result` guards) is permanently dead. actionlint reports the same as
// `property "result" is not defined in object type {outputs: {}}`.
var jobResultInCallOutput = regexp.MustCompile(`jobs\.[A-Za-z0-9_-]+\.result`)

// TestWorkflowCallOutputs_ReferenceJobOutputsNotJobResult lints every one of
// cascade's own reusable workflows: a workflow_call output value must be wired
// to a real job output (jobs.<id>.outputs.<name>), never jobs.<id>.result.
func TestWorkflowCallOutputs_ReferenceJobOutputsNotJobResult(t *testing.T) {
	githubDir := repoGitHubDir(t)
	patterns := []string{
		filepath.Join(githubDir, "workflows", "*.yml"),
		filepath.Join(githubDir, "workflows", "*.yaml"),
	}

	var violations []string
	checked := 0
	for _, pattern := range patterns {
		files, err := filepath.Glob(pattern)
		require.NoError(t, err)
		for _, file := range files {
			content, err := os.ReadFile(file) //nolint:gosec // fixed glob under the repo's .github tree.
			require.NoError(t, err)

			var wf struct {
				On struct {
					WorkflowCall struct {
						Outputs map[string]struct {
							Value string `yaml:"value"`
						} `yaml:"outputs"`
					} `yaml:"workflow_call"`
				} `yaml:"on"`
			}
			require.NoError(t, yaml.Unmarshal(content, &wf), "parse %s", file)

			for name, out := range wf.On.WorkflowCall.Outputs {
				checked++
				if jobResultInCallOutput.MatchString(out.Value) {
					violations = append(violations, fmt.Sprintf(
						"%s: workflow_call output %q references %s; the jobs context in "+
							"a workflow_call output exposes only outputs, so this resolves "+
							"empty and downstream guards on it are dead",
						filepath.Base(file), name, jobResultInCallOutput.FindString(out.Value)))
				}
			}
		}
	}

	require.Positive(t, checked, "lint found no workflow_call outputs to check; "+
		"the reusable-workflow contract files moved")
	if len(violations) > 0 {
		t.Fatal("invalid workflow_call output wiring:\n  " + strings.Join(violations, "\n  "))
	}
}

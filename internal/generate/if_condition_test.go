package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forceDependsOnConfig builds a manifest where a build-linked deploy carries
// run_policy: force. The force policy contributes no dependency condition and
// the build-linked path skips setup detection, so the deploy job's condition
// list is empty. The generator must emit a bare `if: always()` for that job,
// never a dangling `always() &&` with no right operand (which GitHub's
// expression parser rejects).
func forceDependsOnConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: "build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "web", Workflow: "deploy.yaml", DependsOn: []string{"app"}, RunPolicy: config.RunPolicyForce},
		},
	}
}

func TestIfCondition_ForceWithDependsOnEmitsBareAlways(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	result, err := NewGenerator(forceDependsOnConfig(), tmpDir).Generate()
	require.NoError(t, err)

	job := jobBlock(t, result, "deploy-web")
	assert.Contains(t, job, "if: always()\n",
		"force policy with no remaining conditions must emit always() alone")
	assert.NotContains(t, job, "always() &&",
		"a force job with an empty condition list must not emit a dangling always() &&")
}

func TestIfCondition_AlwaysWithDependsOnCombinesConditions(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := forceDependsOnConfig()
	cfg.Deploys[0].RunPolicy = config.RunPolicyAlways

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	job := jobBlock(t, result, "deploy-web")
	assert.Contains(t, job, "always() &&\n",
		"always policy with conditions must keep the always() prefix")
	assert.Contains(t, job,
		"(needs.build-app.result == 'success' || needs.build-app.result == 'skipped')\n",
		"the dependency condition must follow the always() prefix as the right operand")
}

// TestIfCondition_ForceWithDependsOnActionlintClean proves the emitted
// workflow passes actionlint for the force + depends_on combination that
// previously produced an if: expression with a trailing &&.
func TestIfCondition_ForceWithDependsOnActionlintClean(t *testing.T) {
	actionlint := locateActionlint(t)
	dir, wfDir := stageActionlintProject(t)

	wf, err := NewGenerator(forceDependsOnConfig(), dir).Generate()
	require.NoError(t, err)

	path := filepath.Join(wfDir, "orchestrate.yaml")
	require.NoError(t, os.WriteFile(path, []byte(wf), 0o644))

	out, runErr := runActionlint(t, actionlint, path)
	if runErr != nil {
		t.Errorf("actionlint found errors in generated workflow:\n%s", out)
	}
	if strings.Contains(out, "unexpected end of input") {
		t.Errorf("dangling operator in if: expression:\n%s", out)
	}
}

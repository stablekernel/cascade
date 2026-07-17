package generate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// externalRollbackConfig is a primary repo with one local deploy and one
// external deploy in a satellite repo, which is the shape that produces an
// external rollback job.
func externalRollbackConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "master",
		Environments: config.EnvNames("dev", "test", "prod"),
		Deploys: []config.DeployConfig{
			{Name: "api", Workflow: ".github/workflows/deploy-api.yaml"},
		},
		External: []config.ExternalRepoConfig{{
			Repo: "example/cdk-infra",
			Ref:  "main",
			Deploys: []config.ExternalDeployConfig{
				{Name: "cdk", Workflow: "example/cdk-infra/.github/workflows/deploy.yaml"},
			},
		}},
	}
}

// generatedJob renders the promote workflow and returns one job's parsed body.
func generatedJob(t *testing.T, cfg *config.TrunkConfig, jobName string) map[string]interface{} {
	t.Helper()

	content, err := NewPromoteGenerator(cfg, "").Generate()
	require.NoError(t, err)

	var wf struct {
		Jobs map[string]map[string]interface{} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(content), &wf), "generated promote workflow must be valid YAML")

	job, ok := wf.Jobs[jobName]
	require.True(t, ok, "generated promote workflow has no %q job (jobs: %v)", jobName, jobKeys(wf.Jobs))
	return job
}

func jobKeys(jobs map[string]map[string]interface{}) []string {
	names := make([]string, 0, len(jobs))
	for n := range jobs {
		names = append(names, n)
	}
	return names
}

// TestPromoteGenerator_ExternalRollbackJob_IsWiredToRollbackSHA pins the wiring
// that carries a rollback to an external deploy: the satellite's deploy workflow
// is re-called at the SHA the target environment held before the failed promote.
//
// The existing coverage for this was assert.Contains against the whole rendered
// document, which cannot distinguish the external rollback job from any other
// job that happens to mention the same string: "rollback-cdk:" and
// "sha: ${{ needs.preflight.outputs.rollback_sha }}" both pass even if they
// appear in unrelated jobs, and both would keep passing if rollback-cdk were
// wired to the wrong SHA entirely. Parsing the document and asserting against
// the job's own body is what makes the claim falsifiable.
func TestPromoteGenerator_ExternalRollbackJob_IsWiredToRollbackSHA(t *testing.T) {
	job := generatedJob(t, externalRollbackConfig(), "rollback-cdk")

	assert.Equal(t, "Rollback cdk (external)", job["name"])

	// The rollback re-calls the satellite's own deploy workflow, pinned to the
	// external repo's declared ref.
	assert.Equal(t, "example/cdk-infra/.github/workflows/deploy.yaml@main", job["uses"],
		"external rollback must call the satellite's deploy workflow at the declared ref")

	with, ok := job["with"].(map[string]interface{})
	require.True(t, ok, "rollback-cdk must pass inputs to the satellite workflow")

	// This is the claim the deleted rollback-with-external scenario asserted in
	// its header and never checked: the external deploy receives the rollback SHA.
	assert.Equal(t, "${{ needs.preflight.outputs.rollback_sha }}", with["sha"],
		"external rollback must hand the satellite the rollback SHA, not the promoted one")
	assert.Equal(t, "${{ needs.preflight.outputs.target_env }}", with["environment"])
}

// TestPromoteGenerator_ExternalRollbackJob_GateIsComplete pins the four
// conditions that must all hold before an external rollback fires. Each is load
// bearing: dropping any one either fires a rollback that should not have (for
// example on a clean promote, or with an empty SHA that would deploy nothing) or
// rolls back a deploy that never succeeded.
func TestPromoteGenerator_ExternalRollbackJob_GateIsComplete(t *testing.T) {
	job := generatedJob(t, externalRollbackConfig(), "rollback-cdk")

	gate, ok := job["if"].(string)
	require.True(t, ok, "rollback-cdk must be gated by an if: condition")

	for _, cond := range []string{
		// Without always(), the gate never evaluates once a sibling deploy fails.
		"always()",
		// The rollback is opt-in per promote.
		"needs.preflight.outputs.rollback_on_failure == 'true'",
		// An empty rollback SHA means the env had no prior state to return to.
		"needs.preflight.outputs.rollback_sha != ''",
		// Only a deploy that actually succeeded needs undoing.
		"needs.deploy-cdk.result == 'success'",
		// The trigger: some deploy in the promote failed.
		"needs.deploy-api.result == 'failure'",
		"needs.deploy-cdk.result == 'failure'",
	} {
		assert.Contains(t, gate, cond, "rollback-cdk gate is missing a required condition")
	}

	needs, ok := job["needs"].([]interface{})
	require.True(t, ok, "rollback-cdk must declare needs")
	assert.Contains(t, needs, "preflight", "rollback-cdk reads preflight outputs, so it must need preflight")
	assert.Contains(t, needs, "deploy-cdk")
	assert.Contains(t, needs, "deploy-api")
}

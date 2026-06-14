package generate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
)

// rollbackTestConfig builds a multi-env config with a single deploy that has a
// reusable deploy workflow, for asserting on the generated rollback workflow.
func rollbackTestConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "services",
				Workflow: ".github/workflows/deploy.yaml",
			},
		},
	}
}

func TestRollbackGenerator_Enabled_TrueWithOneEnv(t *testing.T) {
	cfg := &config.TrunkConfig{Environments: []string{"prod"}}
	g := NewRollbackGenerator(cfg, "")
	assert.True(t, g.Enabled())
}

func TestRollbackGenerator_Enabled_FalseWithZeroEnv(t *testing.T) {
	cfg := &config.TrunkConfig{}
	g := NewRollbackGenerator(cfg, "")
	assert.False(t, g.Enabled())
}

func TestRollbackGenerator_DispatchInputs(t *testing.T) {
	g := NewRollbackGenerator(rollbackTestConfig(), "")
	content, err := g.Generate()
	assert.NoError(t, err)

	assert.Contains(t, content, "name: Rollback")
	assert.Contains(t, content, "workflow_dispatch:")
	assert.Contains(t, content, "      environment:")
	assert.Contains(t, content, "      target:")
	assert.Contains(t, content, "      deployable:")
	assert.Contains(t, content, "      dry_run:")
	assert.Contains(t, content, "permissions:")
	assert.Contains(t, content, "  contents: write")
}

func TestRollbackGenerator_PreflightResolves(t *testing.T) {
	g := NewRollbackGenerator(rollbackTestConfig(), "")
	content, err := g.Generate()
	assert.NoError(t, err)

	assert.Contains(t, content, "cascade rollback preflight")
	assert.Contains(t, content, "--gha-output")
	assert.Contains(t, content, "target_sha: ${{ steps.preflight.outputs.target_sha }}")
	assert.Contains(t, content, "target_env: ${{ steps.preflight.outputs.target_env }}")
	assert.Contains(t, content, "can_proceed: ${{ steps.preflight.outputs.can_proceed }}")
}

func TestRollbackGenerator_DeployJobsKeyedOnTargetSha(t *testing.T) {
	g := NewRollbackGenerator(rollbackTestConfig(), "")
	content, err := g.Generate()
	assert.NoError(t, err)

	assert.Contains(t, content, "  deploy-services:")
	assert.Contains(t, content, "needs: [preflight]")
	assert.Contains(t, content, "needs.preflight.outputs.target_sha")
}

func TestRollbackGenerator_FinalizeNeedsWiring(t *testing.T) {
	g := NewRollbackGenerator(rollbackTestConfig(), "")
	content, err := g.Generate()
	assert.NoError(t, err)

	assert.Contains(t, content, "  finalize:")
	assert.Contains(t, content, "cascade rollback finalize")
	assert.Contains(t, content, "--commit-push")

	// finalize must need both preflight and the deploy job.
	finalizeNeeds := finalizeNeedsLine(t, content)
	assert.Contains(t, finalizeNeeds, "preflight")
	assert.Contains(t, finalizeNeeds, "deploy-services")
}

func TestRollbackGenerator_FinalizeThreadsDeployResults(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "services", Workflow: ".github/workflows/deploy.yaml"},
			{Name: "web-api", Workflow: ".github/workflows/deploy-web-api.yaml"},
		},
	}
	g := NewRollbackGenerator(cfg, "")
	content, err := g.Generate()
	assert.NoError(t, err)

	// Each deploy job's result must be threaded into finalize as a
	// DEPLOY_RESULT_<UPPER_NAME> env var so the CLI can gate the state write.
	assert.Contains(t, content, "DEPLOY_RESULT_SERVICES: ${{ needs.deploy-services.result }}")
	assert.Contains(t, content, "DEPLOY_RESULT_WEB_API: ${{ needs.deploy-web-api.result }}")
}

func TestRollbackGenerator_FinalizeThreadsDeployableScope(t *testing.T) {
	g := NewRollbackGenerator(rollbackTestConfig(), "")
	content, err := g.Generate()
	assert.NoError(t, err)

	// The dispatch deployable input must reach finalize so the state write is
	// scoped the same way the deploy jobs were. Without this, a deployable-scoped
	// rollback would deploy one deployable but finalize the whole environment,
	// marking it diverged and mirroring onto siblings that never redeployed.
	assert.Contains(t, content, "DEPLOYABLE: ${{ github.event.inputs.deployable }}")
	assert.Contains(t, content, "--deployable \"$DEPLOYABLE\"")

	// The deployable flag must sit on the finalize invocation, not only preflight.
	finalizeIdx := strings.Index(content, "  finalize:")
	assert.Greater(t, finalizeIdx, -1)
	assert.Contains(t, content[finalizeIdx:], "--deployable \"$DEPLOYABLE\"")
}

func TestRollbackGenerator_PreflightBeforeDeployBeforeFinalize(t *testing.T) {
	g := NewRollbackGenerator(rollbackTestConfig(), "")
	content, err := g.Generate()
	assert.NoError(t, err)

	preflightIdx := strings.Index(content, "  preflight:")
	deployIdx := strings.Index(content, "  deploy-services:")
	finalizeIdx := strings.Index(content, "  finalize:")

	assert.Greater(t, preflightIdx, -1)
	assert.Greater(t, deployIdx, preflightIdx)
	assert.Greater(t, finalizeIdx, deployIdx)
}

// finalizeNeedsLine returns the needs: line of the finalize job.
func finalizeNeedsLine(t *testing.T, content string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if line == "  finalize:" {
			for j := i + 1; j < len(lines); j++ {
				trimmed := strings.TrimSpace(lines[j])
				if strings.HasPrefix(trimmed, "needs:") {
					return trimmed
				}
				// Stop if we leave the job before finding needs.
				if len(lines[j]) > 0 && lines[j][0] != ' ' {
					break
				}
			}
		}
	}
	t.Fatalf("finalize job needs: line not found")
	return ""
}

package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rollbackTestConfig builds a multi-env config with a single deploy that has a
// reusable deploy workflow, for asserting on the generated rollback workflow.
func rollbackTestConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "services",
				Workflow: ".github/workflows/deploy.yaml",
			},
		},
	}
}

func TestRollbackGenerator_Enabled_FalseWithOneEnv(t *testing.T) {
	// A single-environment project's only env is the first (trunk-tracking)
	// environment, which reverts via a merge to trunk, not a rollback. With no
	// promoted environment to roll back, the workflow is not emitted, mirroring
	// the hotfix generator.
	cfg := &config.TrunkConfig{Environments: config.EnvNames("prod")}
	g := NewRollbackGenerator(cfg, "")
	assert.False(t, g.Enabled())
}

func TestRollbackGenerator_Enabled_TrueWithTwoEnvs(t *testing.T) {
	cfg := &config.TrunkConfig{Environments: config.EnvNames("dev", "prod")}
	g := NewRollbackGenerator(cfg, "")
	assert.True(t, g.Enabled())
}

func TestRollbackGenerator_Enabled_FalseWithZeroEnv(t *testing.T) {
	cfg := &config.TrunkConfig{}
	g := NewRollbackGenerator(cfg, "")
	assert.False(t, g.Enabled())
}

func TestRollbackGenerator_EnvironmentChoices_ExcludeFirstEnv(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "staging", "prod"),
		Deploys: []config.DeployConfig{
			{Name: "services", Workflow: ".github/workflows/deploy.yaml"},
		},
	}
	content, err := NewRollbackGenerator(cfg, "").Generate()
	assert.NoError(t, err)

	// The first env tracks trunk and is refused by the runtime guard, so the
	// dropdown must not offer it. The promoted envs remain selectable.
	assert.NotContains(t, content, "          - dev\n",
		"first environment must not be a rollback choice")
	assert.Contains(t, content, "          - staging\n")
	assert.Contains(t, content, "          - prod\n")
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
	// target_source must be wired as a job output so downstream steps and the
	// e2e harness can observe which resolution path the preflight chose.
	assert.Contains(t, content, "target_source: ${{ steps.preflight.outputs.target_source }}")
	// The "Report Resolved Source" step echoes the source to the job log so the
	// e2e harness can assert it via log inspection.
	assert.Contains(t, content, "Report Resolved Source")
	assert.Contains(t, content, "rollback resolved from ${{ steps.preflight.outputs.target_source }}")
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
		Environments: config.EnvNames("dev", "prod"),
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

// rollbackDispatchConfig returns rollbackTestConfig with the opt-in
// repository_dispatch trigger enabled, carrying a neutral event type.
func rollbackDispatchConfig() *config.TrunkConfig {
	cfg := rollbackTestConfig()
	cfg.Rollback = &config.RollbackConfig{
		RepositoryDispatch: &config.RepositoryDispatchTrigger{
			Types: []string{"rollback-requested"},
		},
	}
	return cfg
}

// TestRollbackGenerator_OffStateByteIdentical proves that absent the opt-in
// (Rollback nil), the generated workflow is byte-identical to the baseline and
// carries no repository_dispatch trigger or client_payload coalescing.
func TestRollbackGenerator_OffStateByteIdentical(t *testing.T) {
	baseline, err := NewRollbackGenerator(rollbackTestConfig(), "").Generate()
	assert.NoError(t, err)

	// A Rollback block with a nil RepositoryDispatch is still "off".
	cfg := rollbackTestConfig()
	cfg.Rollback = &config.RollbackConfig{}
	withEmpty, err := NewRollbackGenerator(cfg, "").Generate()
	assert.NoError(t, err)

	assert.Equal(t, baseline, withEmpty, "an empty rollback block must not change the output")
	assert.NotContains(t, baseline, "repository_dispatch")
	assert.NotContains(t, baseline, "client_payload")
}

// TestRollbackGenerator_DispatchTriggerEmitted proves the opt-in adds a
// repository_dispatch trigger (with the configured event types) under on: while
// keeping workflow_dispatch intact.
func TestRollbackGenerator_DispatchTriggerEmitted(t *testing.T) {
	content, err := NewRollbackGenerator(rollbackDispatchConfig(), "").Generate()
	assert.NoError(t, err)

	assert.Contains(t, content, "workflow_dispatch:")
	assert.Contains(t, content, "  repository_dispatch:\n")
	assert.Contains(t, content, "    types:\n")
	assert.Contains(t, content, "      - rollback-requested\n")

	// repository_dispatch must sit under on:, before the jobs block.
	onIdx := strings.Index(content, "\non:\n")
	dispatchIdx := strings.Index(content, "  repository_dispatch:")
	jobsIdx := strings.Index(content, "\njobs:\n")
	assert.Greater(t, dispatchIdx, onIdx)
	assert.Greater(t, jobsIdx, dispatchIdx)
}

// TestRollbackGenerator_PreflightCoalescesReads proves the preflight env block
// coalesces github.event.inputs.* with github.event.client_payload.* when the
// dispatch trigger is enabled, so both trigger paths resolve the same target.
func TestRollbackGenerator_PreflightCoalescesReads(t *testing.T) {
	content, err := NewRollbackGenerator(rollbackDispatchConfig(), "").Generate()
	assert.NoError(t, err)

	assert.Contains(t, content, "ENVIRONMENT: ${{ github.event.inputs.environment || github.event.client_payload.environment }}")
	assert.Contains(t, content, "TARGET: ${{ github.event.inputs.target || github.event.client_payload.target }}")
	assert.Contains(t, content, "DEPLOYABLE: ${{ github.event.inputs.deployable || github.event.client_payload.deployable }}")
}

// TestRollbackGenerator_GuardsCoalesceReads proves the deploy-guard, finalize
// gate, and finalize DEPLOYABLE read all coalesce client_payload when the
// dispatch trigger is enabled, so an external signal drives the dry_run and
// deployable scoping the manual path does.
func TestRollbackGenerator_GuardsCoalesceReads(t *testing.T) {
	content, err := NewRollbackGenerator(rollbackDispatchConfig(), "").Generate()
	assert.NoError(t, err)

	// dry_run guard and deployable filter on the deploy job.
	assert.Contains(t, content, "github.event.inputs.dry_run || github.event.client_payload.dry_run")
	assert.Contains(t, content, "github.event.inputs.deployable || github.event.client_payload.deployable")
	// finalize DEPLOYABLE env read.
	assert.Contains(t, content, "DEPLOYABLE: ${{ github.event.inputs.deployable || github.event.client_payload.deployable }}")
}

// TestRollbackGenerator_DispatchActionlint runs actionlint over the rollback
// workflow generated with the repository_dispatch trigger enabled, proving the
// emitted trigger and the coalesced client_payload expressions are valid. Skipped
// when actionlint is not installed so the suite stays hermetic.
func TestRollbackGenerator_DispatchActionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	content, err := NewRollbackGenerator(rollbackDispatchConfig(), t.TempDir()).Generate()
	require.NoError(t, err)

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	path := filepath.Join(wfDir, "cascade-rollback.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")
	writeDeployReusableStub(t, dir, content)

	cmd := exec.Command(bin, "-shellcheck=", path)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues:\n%s", string(out))
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

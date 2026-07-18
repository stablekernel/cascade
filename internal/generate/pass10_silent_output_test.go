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

// pass10DeployWorkflow is a deploy callback that declares the standard
// environment and sha inputs plus a custom region input, so a manifest deploy
// with inputs takes the matrix path and the framework may thread environment/sha.
const pass10DeployWorkflow = `name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        type: string
      sha:
        type: string
      region:
        type: string
`

func pass10Fixture(t *testing.T, workflow string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github/workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github/workflows/deploy.yaml"), []byte(workflow), 0o644))
	return dir
}

// pass10JobBlock returns the lines of the named job, from its "  <job>:" header up to
// the next top-level job header (a line starting with two spaces then a word
// then a colon at the same indent), so an assertion can scope to one job.
func pass10JobBlock(t *testing.T, content, jobID string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	header := "  " + jobID + ":"
	for i, l := range lines {
		if l == header {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "job %q not found", jobID)
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if len(l) > 2 && l[0] == ' ' && l[1] == ' ' && l[2] != ' ' && strings.HasSuffix(strings.TrimSpace(l), ":") && !strings.HasPrefix(l, "   ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// TestGB2_PromoteMatrixDeploy_ThreadsEnvironmentAndSha proves the promote matrix
// deploy path passes the per-promotion environment and sha to its callback the
// way orchestrate does. Before the fix the matrix with: block carried only
// declared manifest inputs, so a promote deployed to an empty environment while
// the job name referenced ${{ matrix.environment }}, a key the matrix builder
// never set.
func TestGB2_PromoteMatrixDeploy_ThreadsEnvironmentAndSha(t *testing.T) {
	dir := pass10Fixture(t, pass10DeployWorkflow)
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{{
			Name: "web", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"},
			Inputs: map[string]interface{}{"region": "us-east-1"},
		}},
	}
	out, err := NewPromoteGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	block := pass10JobBlock(t, out, "deploy-web")
	assert.Contains(t, block, "environment: ${{ matrix.environment }}",
		"matrix deploy must thread the per-promotion environment to the callback")
	assert.Contains(t, block, "sha: ${{ matrix.sha }}",
		"matrix deploy must thread sha when the callback declares it")

	// The matrix builder must inject environment/sha onto each entry so the job
	// name and the with: inputs resolve to real values.
	assert.Contains(t, out, "'. + {environment: $env, sha: $sha}'",
		"matrix builder must carry environment and sha on every matrix entry")
}

// TestGB2_PromoteMatrixDeploy_OmitsShaWhenUndeclared keeps sha gated on
// declaration, matching orchestrate: a callback that does not declare sha must
// not receive it (an undeclared reusable-workflow input is a hard error).
func TestGB2_PromoteMatrixDeploy_OmitsShaWhenUndeclared(t *testing.T) {
	wf := "name: Deploy\non:\n  workflow_call:\n    inputs:\n      environment:\n        type: string\n      region:\n        type: string\n"
	dir := pass10Fixture(t, wf)
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{{
			Name: "web", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"},
			Inputs: map[string]interface{}{"region": "us-east-1"},
		}},
	}
	out, err := NewPromoteGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	block := pass10JobBlock(t, out, "deploy-web")
	assert.Contains(t, block, "environment: ${{ matrix.environment }}")
	assert.NotContains(t, block, "sha: ${{ matrix.sha }}",
		"sha must not be threaded to a callback that does not declare it")
}

// TestGM4_RollbackDispatch_DryRunTreatsBooleanAndString proves the rollback
// deploy guard and finalize gate treat a JSON boolean true (client_payload) and
// the string 'true' (workflow_dispatch) alike. Before the fix a bare "!= 'true'"
// read a boolean true as not-a-dry-run, so a dry-run rollback ran real deploys.
func TestGM4_RollbackDispatch_DryRunTreatsBooleanAndString(t *testing.T) {
	dir := pass10Fixture(t, pass10DeployWorkflow)
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{{
			Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"},
		}},
		Rollback: &config.RollbackConfig{RepositoryDispatch: &config.RepositoryDispatchTrigger{Types: []string{"rollback-requested"}}},
	}
	out, err := NewRollbackGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	coalesced := "github.event.inputs.dry_run || github.event.client_payload.dry_run"
	// Both the boolean and string forms of dry_run must be excluded.
	assert.Contains(t, out, "("+coalesced+") != true",
		"guard must treat a JSON boolean true as a dry run")
	assert.Contains(t, out, "("+coalesced+") != 'true'",
		"guard must treat the string 'true' as a dry run")
	// The old, single-form guard must be gone.
	assert.NotContains(t, out, "("+coalesced+") != 'true' && (github.event.inputs.deployable",
		"the boolean-blind single-comparison guard must be replaced")
}

// TestGM4_Rollback_NoDispatch_ByteIdenticalDryRun keeps the non-dispatch output
// unchanged: without repository_dispatch the dry_run signal is always the
// workflow_dispatch string, so the guard stays the bare string comparison.
func TestGM4_Rollback_NoDispatch_ByteIdenticalDryRun(t *testing.T) {
	dir := pass10Fixture(t, pass10DeployWorkflow)
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{{
			Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"},
		}},
	}
	out, err := NewRollbackGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	assert.Contains(t, out, "github.event.inputs.dry_run != 'true'")
	assert.NotContains(t, out, "!= true &&", "non-dispatch guard must not gain the boolean comparison")
}

// TestGM5_DependentDeploy_JudgesEffectiveResult proves a dependent deploy runs
// when the base deploy's ladder succeeded via a retry shim, not only when the
// immutable base result is success. It also proves the previously duplicated
// clause is gone.
func TestGM5_DependentDeploy_JudgesEffectiveResult(t *testing.T) {
	dir := pass10Fixture(t, pass10DeployWorkflow)
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Deploys: []config.DeployConfig{
			{Name: "web", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}, Retries: 2},
			{Name: "api", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}, DependsOn: []string{"web"}},
		},
	}
	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	block := pass10JobBlock(t, out, "deploy-api")
	assert.Contains(t, block, "needs.deploy-web-retry-1.result == 'success'",
		"dependent deploy must consult the base deploy's retry shims")
	assert.Contains(t, block, "needs.deploy-web-retry-2.result == 'success'")
	// The duplicated immutable-result clause must be gone.
	assert.NotContains(t, block, "needs.deploy-web.result == 'success' &&\n      needs.deploy-web.result == 'success'",
		"the duplicated dependency clause must be removed")
}

// TestGM5_DependentDeploy_NoRetries_SingleClause proves the N=0 path: no retries
// means the effective helper collapses to the bare result read, and the dedup
// leaves exactly one clause (not the previously duplicated pair).
func TestGM5_DependentDeploy_NoRetries_SingleClause(t *testing.T) {
	dir := pass10Fixture(t, pass10DeployWorkflow)
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Deploys: []config.DeployConfig{
			{Name: "web", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
			{Name: "api", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}, DependsOn: []string{"web"}},
		},
	}
	out, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	block := pass10JobBlock(t, out, "deploy-api")
	assert.Equal(t, 1, strings.Count(block, "needs.deploy-web.result == 'success'"),
		"a no-retry dependency must gate on exactly one, non-duplicated clause")
	assert.NotContains(t, block, "retry", "no retry shims exist for a zero-retry dependency")
}

// TestGM6_PromoteNativeDeployment_GuardsDryRunAndCountsSkips proves a dry-run
// promote does not create a real GitHub Deployment, and that the terminal status
// counts a legitimately skipped deploy as success and includes the prod deploy.
func TestGM6_PromoteNativeDeployment_GuardsDryRunAndCountsSkips(t *testing.T) {
	dir := pass10Fixture(t, pass10DeployWorkflow)
	tru := true
	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Environments: []config.EnvironmentEntry{
			{Name: "staging", EnvironmentConfig: config.EnvironmentConfig{EnvironmentURL: "https://staging.example.com"}},
			{Name: "production", EnvironmentConfig: config.EnvironmentConfig{EnvironmentURL: "https://app.example.com"}},
		},
		Deployments: &config.DeploymentsConfig{Enabled: &tru},
		Deploys: []config.DeployConfig{{
			Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"},
		}},
	}
	out, err := NewPromoteGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	// A dry run must not create a real Deployment.
	assert.Contains(t, out, "if: ${{ github.server_url == 'https://github.com' && github.event.inputs.dry_run != 'true' }}",
		"the Create deployment step must be gated on a non-dry-run promote")
	assert.Contains(t, out, "github.event.inputs.dry_run != 'true' && always()",
		"the status step must also be gated on a non-dry-run promote")

	// A skipped deploy is not a failure, and the prod deploy is not omitted.
	assert.Contains(t, out, "needs.deploy-app.result == 'success' || needs.deploy-app.result == 'skipped'",
		"a legitimately skipped deploy must not be counted as a Deployment failure")
	assert.Contains(t, out, "needs.deploy-app-prod.result == 'success' || needs.deploy-app-prod.result == 'skipped'",
		"the prod deploy result must be included in the Deployment status")
}

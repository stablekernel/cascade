package generate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPromote_ProdDeployJob_TargetsRoleReleaseEnv proves the generated prod
// deploy job targets the RELEASE environment, which is role-aware: with
// role: release on an environment that is NOT last in the list, the job must
// deploy to that env, not the positional last one. This guards the agreement
// between the generated job and the runtime prod-deployment gate
// (ProdDeployment.Environment = ReleaseEnvironment): if the job baked the
// positional last env while the gate fired on the role env's SHA, cascade would
// deploy the release SHA to the wrong native environment.
func TestPromote_ProdDeployJob_TargetsRoleReleaseEnv(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		// prod carries role: release but is NOT the last entry (monitor is).
		Environments: []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "staging", Role: config.EnvRolePrerelease},
			{Name: "prod", Role: config.EnvRoleRelease},
			{Name: "monitor"},
		},
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// The prod deploy job is keyed on the role-release env (prod), and its
	// environment input targets prod.
	roleBlock := jobBlock(t, result, "deploy-svc-prod")
	require.NotEmpty(t, roleBlock, "expected the prod deploy job to target the role:release env (deploy-svc-prod)")
	assert.Contains(t, roleBlock, "      environment: prod",
		"prod deploy must target the role:release environment via the with: input")

	// It must NOT be keyed on the positional last env (monitor); that was the
	// pre-fix behavior when finalEnv was derived positionally.
	assert.NotContains(t, result, "  deploy-svc-monitor:",
		"prod deploy job must not target the positional last env (monitor) when role:release is elsewhere")
}

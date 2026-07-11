package environments

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// fullConfig returns a manifest config exercising every additive
// environment_config field, with environments declared out of alphabetical
// order so the manifest-order guarantee is observable.
func fullConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		Environments: []string{"prod", "dev", "test"},
		EnvironmentConfig: map[string]config.EnvironmentConfig{
			"prod": {
				GHAEnvironment:    "production",
				RequiredReviewers: []string{"octocat", "team/ops"},
				WaitTimer:         intPtr(10),
				BranchPolicy:      config.EnvBranchPolicyProtected,
				Secrets:           []string{"MY_SECRET", "DB_PASSWORD"},
				Variables:         []string{"REGION"},
			},
			"dev": {
				BranchPolicy:   config.EnvBranchPolicyCustom,
				BranchPatterns: []string{"main", "release/*"},
				TagPatterns:    []string{"v*"},
			},
			// "test" intentionally has no environment_config entry.
		},
	}
}

// TestBuild_OrdersByManifestAndDefaults asserts the payload follows the
// manifest's environments order and fills defaults for environments without an
// environment_config entry.
func TestBuild_OrdersByManifestAndDefaults(t *testing.T) {
	t.Parallel()

	p := Build(fullConfig())

	require.Len(t, p.Environments, 3)
	assert.Equal(t, []string{"prod", "dev", "test"},
		[]string{p.Environments[0].Name, p.Environments[1].Name, p.Environments[2].Name},
		"environments must follow manifest order")

	// prod: gha_environment override + protected policy + wait timer.
	prod := p.Environments[0]
	assert.Equal(t, "production", prod.GHAEnvironment)
	assert.Equal(t, 10, prod.Environment.WaitTimer)
	require.NotNil(t, prod.Environment.DeploymentBranchPolicy)
	assert.True(t, prod.Environment.DeploymentBranchPolicy.ProtectedBranches)
	assert.False(t, prod.Environment.DeploymentBranchPolicy.CustomBranchPolicies)
	assert.Equal(t, []string{"octocat", "team/ops"}, prod.OperatorTodo.RequiredReviewers)
	assert.Equal(t, []string{"MY_SECRET", "DB_PASSWORD"}, prod.OperatorTodo.Secrets)
	assert.Equal(t, []string{"REGION"}, prod.OperatorTodo.Variables)

	// dev: custom policy with patterns, gha_environment defaults to name.
	dev := p.Environments[1]
	assert.Equal(t, "dev", dev.GHAEnvironment)
	require.NotNil(t, dev.Environment.DeploymentBranchPolicy)
	assert.False(t, dev.Environment.DeploymentBranchPolicy.ProtectedBranches)
	assert.True(t, dev.Environment.DeploymentBranchPolicy.CustomBranchPolicies)
	assert.Equal(t, []string{"main", "release/*"}, dev.OperatorTodo.BranchPatterns)
	assert.Equal(t, []string{"v*"}, dev.OperatorTodo.TagPatterns)

	// test: no environment_config entry -> defaults.
	tst := p.Environments[2]
	assert.Equal(t, "test", tst.GHAEnvironment)
	assert.Equal(t, 0, tst.Environment.WaitTimer)
	assert.Nil(t, tst.Environment.DeploymentBranchPolicy, "absent policy marshals to null (all branches)")
	assert.Nil(t, tst.OperatorTodo.RequiredReviewers)
	assert.Nil(t, tst.OperatorTodo.Secrets)
	assert.Nil(t, tst.OperatorTodo.Variables)
}

// TestBuild_NoEnvironmentConfigBlock confirms a manifest with environments but
// no environment_config map still emits one default entry per environment.
func TestBuild_NoEnvironmentConfigBlock(t *testing.T) {
	t.Parallel()

	cfg := &config.TrunkConfig{Environments: []string{"staging", "prod"}}
	p := Build(cfg)

	require.Len(t, p.Environments, 2)
	assert.Equal(t, "staging", p.Environments[0].Name)
	assert.Equal(t, "staging", p.Environments[0].GHAEnvironment)
	assert.Nil(t, p.Environments[0].Environment.DeploymentBranchPolicy)
	assert.Equal(t, "prod", p.Environments[1].Name)
}

// TestBuild_AllBranchPolicyIsNull confirms the "all" policy marshals to a null
// deployment_branch_policy, GitHub's all-branches value.
func TestBuild_AllBranchPolicyIsNull(t *testing.T) {
	t.Parallel()

	cfg := &config.TrunkConfig{
		Environments: []string{"prod"},
		EnvironmentConfig: map[string]config.EnvironmentConfig{
			"prod": {BranchPolicy: config.EnvBranchPolicyAll},
		},
	}
	p := Build(cfg)
	assert.Nil(t, p.Environments[0].Environment.DeploymentBranchPolicy)
}

// TestMarshal_Deterministic is the acceptance-criterion drift test: the same
// manifest always produces byte-identical output.
func TestMarshal_Deterministic(t *testing.T) {
	t.Parallel()

	first, err := Marshal(Build(fullConfig()))
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		again, err := Marshal(Build(fullConfig()))
		require.NoError(t, err)
		assert.Equal(t, string(first), string(again), "output must be byte-identical across runs")
	}
}

// TestMarshal_ValidJSONAndTrailingNewline confirms the output is valid JSON,
// round-trippable, and ends in exactly one trailing newline.
func TestMarshal_ValidJSONAndTrailingNewline(t *testing.T) {
	t.Parallel()

	out, err := Marshal(Build(fullConfig()))
	require.NoError(t, err)

	require.Greater(t, len(out), 1)
	assert.Equal(t, byte('\n'), out[len(out)-1], "must end with a newline")
	assert.NotEqual(t, byte('\n'), out[len(out)-2], "must end with exactly one newline")

	var round Payload
	require.NoError(t, json.Unmarshal(out, &round), "output must be valid, round-trippable JSON")
	require.Len(t, round.Environments, 3)
	assert.Equal(t, "prod", round.Environments[0].Name)
}

// TestMarshal_SecretsAndVariablesAreNamesOnly is a guardrail: the emitted JSON
// carries secret and variable NAMES and never a values map or inline value.
func TestMarshal_SecretsAndVariablesAreNamesOnly(t *testing.T) {
	t.Parallel()

	out, err := Marshal(Build(fullConfig()))
	require.NoError(t, err)

	var generic map[string]any
	require.NoError(t, json.Unmarshal(out, &generic))

	envs, ok := generic["environments"].([]any)
	require.True(t, ok)
	prod, ok := envs[0].(map[string]any)
	require.True(t, ok)
	todo, ok := prod["operator_todo"].(map[string]any)
	require.True(t, ok)

	// secrets and variables are arrays of strings (names), not objects (values).
	secrets, ok := todo["secrets"].([]any)
	require.True(t, ok, "secrets must be a list of names")
	for _, s := range secrets {
		_, isString := s.(string)
		assert.True(t, isString, "each secret entry must be a bare name string")
	}
	_, hasValue := todo["secret_values"]
	assert.False(t, hasValue, "no value-carrying field may be emitted")
}

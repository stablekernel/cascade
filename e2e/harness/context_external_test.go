package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionContext_ExternalState(t *testing.T) {
	ctx := NewExecutionContext()

	// Initially no external state
	state := ctx.GetExternalDeployState("dev", "cdk")
	assert.Nil(t, state)

	// Record external state
	ctx.RecordExternalDeployState("dev", "cdk", "sha-123", "v1.0.0")

	// Retrieve external state
	state = ctx.GetExternalDeployState("dev", "cdk")
	require.NotNil(t, state)
	assert.Equal(t, "sha-123", state.SHA)
	assert.Equal(t, "v1.0.0", state.Version)
}

func TestExecutionContext_ExternalState_MultipleEnvs(t *testing.T) {
	ctx := NewExecutionContext()

	// Record state for multiple environments
	ctx.RecordExternalDeployState("dev", "cdk", "dev-sha", "v1.0.0-rc.0")
	ctx.RecordExternalDeployState("test", "cdk", "test-sha", "v1.0.0-rc.1")
	ctx.RecordExternalDeployState("prod", "cdk", "prod-sha", "v1.0.0")

	// Verify each environment has separate state
	devState := ctx.GetExternalDeployState("dev", "cdk")
	require.NotNil(t, devState)
	assert.Equal(t, "dev-sha", devState.SHA)

	testState := ctx.GetExternalDeployState("test", "cdk")
	require.NotNil(t, testState)
	assert.Equal(t, "test-sha", testState.SHA)

	prodState := ctx.GetExternalDeployState("prod", "cdk")
	require.NotNil(t, prodState)
	assert.Equal(t, "prod-sha", prodState.SHA)
}

func TestExecutionContext_ExternalState_MultipleDeploys(t *testing.T) {
	ctx := NewExecutionContext()

	// Record state for multiple deploys in same environment
	ctx.RecordExternalDeployState("dev", "cdk", "cdk-sha", "v1.0.0")
	ctx.RecordExternalDeployState("dev", "k8s", "k8s-sha", "v2.0.0")

	cdkState := ctx.GetExternalDeployState("dev", "cdk")
	require.NotNil(t, cdkState)
	assert.Equal(t, "cdk-sha", cdkState.SHA)

	k8sState := ctx.GetExternalDeployState("dev", "k8s")
	require.NotNil(t, k8sState)
	assert.Equal(t, "k8s-sha", k8sState.SHA)
}

func TestExecutionContext_GetAllExternalState(t *testing.T) {
	ctx := NewExecutionContext()

	// Record multiple deploys
	ctx.RecordExternalDeployState("dev", "cdk", "cdk-sha", "v1.0.0")
	ctx.RecordExternalDeployState("dev", "k8s", "k8s-sha", "v2.0.0")
	ctx.RecordExternalDeployState("dev", "lambda", "lambda-sha", "v3.0.0")

	allState := ctx.GetAllExternalState("dev")
	require.NotNil(t, allState)
	assert.Len(t, allState, 3)

	assert.Equal(t, "cdk-sha", allState["cdk"].SHA)
	assert.Equal(t, "k8s-sha", allState["k8s"].SHA)
	assert.Equal(t, "lambda-sha", allState["lambda"].SHA)
}

func TestExecutionContext_Clone_PreservesExternalState(t *testing.T) {
	ctx := NewExecutionContext()

	ctx.RecordExternalDeployState("dev", "cdk", "sha-123", "v1.0.0")
	ctx.RecordExternalDeployState("test", "cdk", "sha-456", "v1.0.0-rc.0")

	// Clone the context
	clone := ctx.Clone()

	// Verify cloned state
	devState := clone.GetExternalDeployState("dev", "cdk")
	require.NotNil(t, devState)
	assert.Equal(t, "sha-123", devState.SHA)

	testState := clone.GetExternalDeployState("test", "cdk")
	require.NotNil(t, testState)
	assert.Equal(t, "sha-456", testState.SHA)

	// Modify original - should not affect clone
	ctx.RecordExternalDeployState("dev", "cdk", "new-sha", "v2.0.0")
	assert.Equal(t, "sha-123", clone.GetExternalDeployState("dev", "cdk").SHA)
}

func TestExecutionContext_ExternalState_Update(t *testing.T) {
	ctx := NewExecutionContext()

	// Initial state
	ctx.RecordExternalDeployState("dev", "cdk", "sha-1", "v1.0.0")

	// Update state
	ctx.RecordExternalDeployState("dev", "cdk", "sha-2", "v1.1.0")

	state := ctx.GetExternalDeployState("dev", "cdk")
	require.NotNil(t, state)
	assert.Equal(t, "sha-2", state.SHA)
	assert.Equal(t, "v1.1.0", state.Version)
}

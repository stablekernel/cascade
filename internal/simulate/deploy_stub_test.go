package simulate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeployStub_RecordedEffects_DefaultSuccess(t *testing.T) {
	t.Parallel()

	stub := newDeployStub([]string{"app"}, []string{"services"}, nil)

	effects := stub.recordedEffects()
	require.Len(t, effects, 2)

	assert.Equal(t, Effect{
		Disposition: DispositionRun,
		Action:      "build",
		Target:      "app",
		Detail:      "simulated success (not executed)",
	}, effects[0])
	assert.Equal(t, Effect{
		Disposition: DispositionRun,
		Action:      "deploy",
		Target:      "services",
		Detail:      "simulated success (not executed)",
	}, effects[1])

	assert.Empty(t, stub.gate(), "all-success deploys do not gate finalize")
}

func TestDeployStub_RecordedEffects_NoCallbacks(t *testing.T) {
	t.Parallel()

	stub := newDeployStub(nil, nil, nil)

	assert.Nil(t, stub.recordedEffects())
	assert.False(t, stub.hasCallbacks())
	assert.Empty(t, stub.gate(), "with no deploys there is nothing to gate on")
}

func TestDeployStub_InjectedFailure_Gates(t *testing.T) {
	t.Parallel()

	stub := newDeployStub([]string{"app"}, []string{"services"}, map[string]DeployOutcome{
		"services": OutcomeFailure,
	})

	effects := stub.recordedEffects()
	require.Len(t, effects, 2)
	assert.Equal(t, DispositionRun, effects[1].Disposition)
	assert.Equal(t, "deploy", effects[1].Action)
	assert.Contains(t, effects[1].Detail, "simulated failure")

	reason := stub.gate()
	require.NotEmpty(t, reason, "a failed deploy must gate finalize")
	assert.Contains(t, reason, "services")
}

func TestDeployStub_AllSkipped_Gates(t *testing.T) {
	t.Parallel()

	stub := newDeployStub(nil, []string{"services"}, map[string]DeployOutcome{
		"services": OutcomeSkipped,
	})

	effects := stub.recordedEffects()
	require.Len(t, effects, 1)
	assert.Equal(t, DispositionSkip, effects[0].Disposition)

	reason := stub.gate()
	require.NotEmpty(t, reason, "a step whose only deploy was skipped deploys nothing")
	assert.Contains(t, reason, "no simulated deploy succeeded")
}

func TestDeployStub_OneSucceedsOneSkipped_Proceeds(t *testing.T) {
	t.Parallel()

	stub := newDeployStub(nil, []string{"infra", "services"}, map[string]DeployOutcome{
		"infra": OutcomeSkipped,
	})

	assert.Empty(t, stub.gate(), "one succeeding deploy is enough to advance finalize")
}

// TestDeployStub_ScopedGate mirrors the real rollback gate's --deployable
// scoping (gateOnDeployResults): with a deployable set, only that deploy's
// outcome is in scope, so an out-of-scope failure neither gates nor counts.
func TestDeployStub_ScopedGate(t *testing.T) {
	t.Parallel()

	stub := newDeployStub(nil, []string{"svc", "web"}, map[string]DeployOutcome{
		"web": OutcomeFailure,
	})

	assert.Empty(t, stub.gateScoped("svc"),
		"an out-of-scope failure must not gate a scoped rollback")

	reason := stub.gateScoped("web")
	require.NotEmpty(t, reason, "the scoped deploy failed, so the write must gate")
	assert.Contains(t, reason, "web")
}

// TestDeployStub_ScopedGate_SkippedInScope mirrors the all-skipped rule under
// scoping: the in-scope deploy was skipped, so nothing was deployed and the
// write gates even though an out-of-scope deploy succeeded.
func TestDeployStub_ScopedGate_SkippedInScope(t *testing.T) {
	t.Parallel()

	stub := newDeployStub(nil, []string{"svc", "web"}, map[string]DeployOutcome{
		"svc": OutcomeSkipped,
	})

	reason := stub.gateScoped("svc")
	require.NotEmpty(t, reason)
	assert.Contains(t, reason, "no simulated deploy succeeded")
}

func TestDeployStub_DoesNotAliasInputs(t *testing.T) {
	t.Parallel()

	builds := []string{"app"}
	deploys := []string{"services"}
	stub := newDeployStub(builds, deploys, nil)

	builds[0] = "mutated"
	deploys[0] = "mutated"

	effects := stub.recordedEffects()
	require.Len(t, effects, 2)
	assert.Equal(t, "app", effects[0].Target)
	assert.Equal(t, "services", effects[1].Target)
}

func TestParseDeployResults(t *testing.T) {
	t.Parallel()

	t.Run("valid pairs", func(t *testing.T) {
		t.Parallel()
		got, err := ParseDeployResults([]string{"services=failure", "infra=skipped", "app=SUCCESS"})
		require.NoError(t, err)
		assert.Equal(t, map[string]DeployOutcome{
			"services": OutcomeFailure,
			"infra":    OutcomeSkipped,
			"app":      OutcomeSuccess,
		}, got)
	})

	t.Run("nil for empty input", func(t *testing.T) {
		t.Parallel()
		got, err := ParseDeployResults(nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("rejects malformed pair", func(t *testing.T) {
		t.Parallel()
		_, err := ParseDeployResults([]string{"services"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "name=success")
	})

	t.Run("rejects blank name", func(t *testing.T) {
		t.Parallel()
		_, err := ParseDeployResults([]string{"=failure"})
		require.Error(t, err)
	})

	t.Run("rejects unknown outcome", func(t *testing.T) {
		t.Parallel()
		_, err := ParseDeployResults([]string{"services=maybe"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown outcome")
	})
}

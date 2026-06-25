package simulate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/promote"
)

// seedManifestWithCallbacks writes a manifest with dev populated and uat empty
// that also declares one build and one deploy callback, so a dev->uat promotion
// drives the deploy-stub model.
func seedManifestWithCallbacks(t *testing.T) string {
	t.Helper()

	return writeManifest(t, &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "uat", "prod"},
			Builds: []config.BuildConfig{
				{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
			},
			Deploys: []config.DeployConfig{
				{Name: "services", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"deploy/**"}, DependsOn: []string{"app"}},
			},
		},
		State: map[string]*config.EnvState{
			"dev": {SHA: "a1b2c3d4e5f6", Version: "v1.2.0-rc.1"},
			"uat": {},
		},
	})
}

func TestEngine_Simulate_DeployStub_DefaultSuccess(t *testing.T) {
	t.Parallel()

	path := seedManifestWithCallbacks(t)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	assert.NotEmpty(t, result.Note, "output must carry the orchestration-not-deploys note")
	assert.Contains(t, result.Note, "simulated")
	assert.Contains(t, result.Note, "not executed")

	build, ok := findEffect(result.Effects, "build")
	require.True(t, ok, "the configured build appears as a stubbed effect")
	assert.Equal(t, "app", build.Target)
	assert.Equal(t, DispositionRun, build.Disposition)
	assert.Contains(t, build.Detail, "simulated success")

	deploy, ok := findRecordedDeploy(result.Effects, "services")
	require.True(t, ok, "the configured deploy appears as a stubbed effect")
	assert.Equal(t, DispositionRun, deploy.Disposition)
	assert.Contains(t, deploy.Detail, "simulated success")

	// Finalize is reached: the env write-state effect runs.
	ws, ok := findEffect(result.Effects, "write state")
	require.True(t, ok)
	assert.Equal(t, DispositionRun, ws.Disposition)
}

func TestEngine_Simulate_DeployStub_InjectedFailureGatesFinalize(t *testing.T) {
	t.Parallel()

	path := seedManifestWithCallbacks(t)

	engine, err := NewEngine(path,
		WithActor("tester"),
		WithDeployResults(map[string]DeployOutcome{"services": OutcomeFailure}),
	)
	require.NoError(t, err)

	result, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	deploy, ok := findRecordedDeploy(result.Effects, "services")
	require.True(t, ok)
	assert.Contains(t, deploy.Detail, "simulated failure")

	ws, ok := findEffect(result.Effects, "write state")
	require.True(t, ok, "the gated finalize is still surfaced as an effect")
	assert.Equal(t, DispositionGate, ws.Disposition, "a failed deploy gates finalize")
	assert.Contains(t, ws.Detail, "services")
}

func TestEngine_Simulate_DeployStub_NoCallbacksUnchanged(t *testing.T) {
	t.Parallel()

	// The seed manifest declares no builds or deploys, so the stub records
	// nothing and the generic deploy marker stands on its own.
	path := seedManifest(t)

	engine, err := NewEngine(path, WithActor("tester"))
	require.NoError(t, err)

	result, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)

	_, ok := findEffect(result.Effects, "build")
	assert.False(t, ok, "no build callback configured, so none is recorded")

	ws, ok := findEffect(result.Effects, "write state")
	require.True(t, ok)
	assert.Equal(t, DispositionRun, ws.Disposition, "no deploy gate without configured deploys")
}

// findRecordedDeploy finds the stubbed deploy callback effect with the given
// target name, distinguishing it from the env-level deploy marker.
func findRecordedDeploy(effects []Effect, target string) (Effect, bool) {
	for _, e := range effects {
		if e.Action == "deploy" && e.Target == target {
			return e, true
		}
	}
	return Effect{}, false
}

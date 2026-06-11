package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stablekernel/cascade/e2e/harness"
	"github.com/stretchr/testify/require"
)

// TestMultiRepoScenarios runs all multi-repo scenarios from the scenarios/multi-repo directory
func TestMultiRepoScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-repo e2e tests in short mode")
	}

	// Find the scenarios directory
	scenariosDir := filepath.Join("scenarios", "multi-repo")
	if _, err := os.Stat(scenariosDir); os.IsNotExist(err) {
		t.Skip("scenarios/multi-repo directory not found")
	}

	scenarios, err := harness.DiscoverMultiRepoScenarios(scenariosDir)
	require.NoError(t, err, "failed to discover scenarios")

	if len(scenarios) == 0 {
		t.Skip("no multi-repo scenarios found")
	}

	for _, scenario := range scenarios {
		scenario := scenario // capture range variable
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			runMultiRepoScenario(t, scenario)
		})
	}
}

func runMultiRepoScenario(t *testing.T, scenario *harness.MultiRepoScenario) {
	// Real per-repo workflow generation (clone + build + generate + push +
	// converge for each repo) plus the external-update run under act is heavy;
	// give scenarios a generous ceiling under the outer go test -timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	h := harness.NewMultiRepoHarness(t)
	err := h.SetupInfra(ctx)
	require.NoError(t, err, "failed to setup infrastructure")
	defer h.Cleanup(ctx)

	runner := harness.NewMultiRepoRunner(h, scenario)
	err = runner.Run(ctx)
	require.NoError(t, err, "scenario failed: %s", scenario.Description)
}

// TestMultiRepoScenario_SatelliteNotifiesPrimary tests the specific scenario
// in isolation for easier debugging
func TestMultiRepoScenario_SatelliteNotifiesPrimary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	scenarioPath := filepath.Join("scenarios", "multi-repo", "satellite-notifies-primary.yaml")
	if _, err := os.Stat(scenarioPath); os.IsNotExist(err) {
		t.Skip("scenario file not found")
	}

	data, err := os.ReadFile(scenarioPath)
	require.NoError(t, err)

	scenario, err := harness.ParseMultiRepoScenario(data)
	require.NoError(t, err)

	runMultiRepoScenario(t, scenario)
}

// TestMultiRepoScenario_ExternalStatePromotion tests external state tracking
func TestMultiRepoScenario_ExternalStatePromotion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	scenarioPath := filepath.Join("scenarios", "multi-repo", "external-state-promotion.yaml")
	if _, err := os.Stat(scenarioPath); os.IsNotExist(err) {
		t.Skip("scenario file not found")
	}

	data, err := os.ReadFile(scenarioPath)
	require.NoError(t, err)

	scenario, err := harness.ParseMultiRepoScenario(data)
	require.NoError(t, err)

	runMultiRepoScenario(t, scenario)
}

// TestMultiRepoScenario_MixedDeploys tests mixed local and external deploys
func TestMultiRepoScenario_MixedDeploys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	scenarioPath := filepath.Join("scenarios", "multi-repo", "mixed-deploys.yaml")
	if _, err := os.Stat(scenarioPath); os.IsNotExist(err) {
		t.Skip("scenario file not found")
	}

	data, err := os.ReadFile(scenarioPath)
	require.NoError(t, err)

	scenario, err := harness.ParseMultiRepoScenario(data)
	require.NoError(t, err)

	runMultiRepoScenario(t, scenario)
}

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

	if pattern := scenarioFilter(); pattern != "" {
		t.Skipf("%s=%q targets multi-step scenarios; skipping multi-repo suite", envScenarioFilter, pattern)
	}

	// Find the scenarios directory
	scenariosDir := filepath.Join("scenarios", "multi-repo")
	if _, err := os.Stat(scenariosDir); os.IsNotExist(err) {
		t.Skip("scenarios/multi-repo directory not found")
	}

	scenarios, err := harness.DiscoverMultiRepoScenarios(scenariosDir)
	require.NoError(t, err, "failed to discover scenarios")

	// Select only this shard's slice so the heavy per-repo setup runs once
	// across the matrix rather than once per leg. The default (unset) shard
	// returns the full set, leaving local runs unchanged. Description embeds
	// the unique source path, so the round-robin distribution is stable.
	shard, err := harness.ShardFromEnv()
	require.NoError(t, err)
	scenarios = harness.SelectShard(scenarios, multiRepoShardKey, shard)

	if len(scenarios) == 0 {
		t.Skip("no multi-repo scenarios for this shard")
	}

	for _, scenario := range scenarios {
		scenario := scenario // capture range variable
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			runMultiRepoScenario(t, scenario)
		})
	}
}

// multiRepoShardKey returns the stable, collision-free key used to order
// multi-repo scenarios before they are distributed across CI shards. Discovery
// sets Description to the unique source path.
func multiRepoShardKey(s *harness.MultiRepoScenario) string {
	if s.Description != "" {
		return s.Description
	}
	return s.Name
}

// requireShardOwns skips a standalone, singly-named heavyweight test unless the
// active shard owns it. Each test name hashes to exactly one shard, so across
// the matrix it runs once; the unset default owns everything for local runs.
func requireShardOwns(t *testing.T) {
	t.Helper()
	if pattern := scenarioFilter(); pattern != "" {
		t.Skipf("%s=%q targets multi-step scenarios; skipping standalone test", envScenarioFilter, pattern)
	}
	shard, err := harness.ShardFromEnv()
	require.NoError(t, err)
	if !shard.Owns(t.Name()) {
		t.Skipf("assigned to a different shard (this leg is %d of %d)", shard.Index, shard.Total)
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
	requireShardOwns(t)

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
	requireShardOwns(t)

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
	requireShardOwns(t)

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

// Package e2e contains cascade's end-to-end scenario tests. Each
// scenario generates workflows from a manifest, runs them with act
// against a local gitea instance, and asserts the resulting repository
// and manifest state. Package harness provides the infrastructure.
package e2e

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stablekernel/cascade/e2e/harness"
	"github.com/stretchr/testify/require"
)

// envScenarioFilter, when set to a regular expression, narrows the run to
// scenarios whose name matches it. It is a single-scenario debugging switch: it
// bypasses shard selection so one matrix leg can run exactly the target
// scenarios regardless of which shard would normally own them.
const envScenarioFilter = "E2E_SCENARIO"

// scenarioFilter returns the trimmed scenario-name pattern, or "" when unset.
func scenarioFilter() string { return strings.TrimSpace(os.Getenv(envScenarioFilter)) }

func TestMultiStepScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E tests")
	}

	// Discover multi-step scenarios from the scenarios directory
	scenarios, err := harness.DiscoverMultiStepScenarios("scenarios")
	require.NoError(t, err)

	if pattern := scenarioFilter(); pattern != "" {
		// Debug switch: filter the full set by name and bypass sharding so a
		// single leg runs only the matching scenarios.
		re, err := regexp.Compile(pattern)
		require.NoError(t, err, "invalid %s pattern", envScenarioFilter)
		scenarios = filterScenariosByName(scenarios, re)
		t.Logf("scenario filter %q selected %d scenario(s)", pattern, len(scenarios))
	} else {
		// Select this CI shard's slice. Outside a sharded matrix the default
		// (E2E_SHARD_TOTAL unset = 1) returns every scenario, so a local run is
		// unchanged. The scenario Description embeds the unique source path,
		// giving a stable round-robin distribution across shards.
		shard, err := harness.ShardFromEnv()
		require.NoError(t, err)
		scenarios = harness.SelectShard(scenarios, scenarioShardKey, shard)
	}

	if len(scenarios) == 0 {
		t.Log("No multi-step scenarios selected for this leg")
		return
	}

	for _, s := range scenarios {
		scenario := s // capture range variable
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			// RunMultiStepScenario runs the whole scenario with a bounded
			// scenario-level retry on transient act/docker execution failures.
			// Each attempt gets a fresh harness (network, gitea repo, act
			// containers), so a retry is a clean slate. Real assertion or
			// job-level failures fail deterministically without a retry.
			err := harness.RunMultiStepScenario(ctx, t, scenario)
			require.NoError(t, err, "scenario failed")
		})
	}
}

// filterScenariosByName keeps only the scenarios whose name matches re.
func filterScenariosByName(scenarios []*harness.MultiStepScenario, re *regexp.Regexp) []*harness.MultiStepScenario {
	out := make([]*harness.MultiStepScenario, 0, len(scenarios))
	for _, s := range scenarios {
		if re.MatchString(s.Name) {
			out = append(out, s)
		}
	}
	return out
}

// scenarioShardKey returns the stable key used to order scenarios before they
// are distributed across CI shards. Discovery sets Description to the unique
// source path (optionally suffixed with the scenario description), so it is a
// reliable, collision-free sort key even when two scenarios share a name.
func scenarioShardKey(s *harness.MultiStepScenario) string {
	if s.Description != "" {
		return s.Description
	}
	return s.Name
}

// DefaultParallelism returns recommended parallel test count
func DefaultParallelism() int {
	cpus := runtime.NumCPU()
	if cpus <= 2 {
		return 1
	}
	return cpus - 2
}

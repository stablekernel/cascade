package e2e

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stablekernel/cascade/e2e/harness"
	"github.com/stretchr/testify/require"
)

func TestMultiStepScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E tests")
	}

	// Discover multi-step scenarios from the scenarios directory
	scenarios, err := harness.DiscoverMultiStepScenarios("scenarios")
	require.NoError(t, err)

	if len(scenarios) == 0 {
		t.Log("No multi-step scenarios found")
		return
	}

	for _, s := range scenarios {
		scenario := s // capture range variable
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			h := harness.New(t)
			defer h.Cleanup()

			err := h.SetupInfra(ctx)
			require.NoError(t, err, "failed to setup infrastructure")

			// Stage initial repo with config
			err = h.StageRepoFromConfig(ctx, scenario.Config)
			require.NoError(t, err, "failed to stage repo")

			// Create runner and execute
			runner := harness.NewRunner(t, h)
			err = runner.Run(ctx, scenario)
			require.NoError(t, err, "scenario failed")
		})
	}
}

// DefaultParallelism returns recommended parallel test count
func DefaultParallelism() int {
	cpus := runtime.NumCPU()
	if cpus <= 2 {
		return 1
	}
	return cpus - 2
}

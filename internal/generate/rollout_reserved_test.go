package generate

import (
	"bytes"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// TestRolloutReservedFieldsAreByteIdentical asserts that populating the
// reserved canary sub-block fields (percent, bake_time, promote_callback,
// rollback_callback) produces byte-identical output to a config that leaves
// them empty. These fields are reserved shape but not yet wired to generation.
func TestRolloutReservedFieldsAreByteIdentical(t *testing.T) {
	t.Parallel()

	// Case A: reserved fields populated.
	rolloutWithReserved := &config.RolloutConfig{
		Type: "canary",
		Canary: &config.CanaryConfig{
			Steps:            []int{10, 50, 100},
			Analysis:         ".github/workflows/canary-analysis.yaml",
			Percent:          10,
			BakeTime:         "30m",
			PromoteCallback:  ".github/workflows/promote.yaml",
			RollbackCallback: ".github/workflows/rollback.yaml",
		},
	}

	// Case B: same type, same Steps/Analysis, reserved fields absent.
	rolloutWithoutReserved := &config.RolloutConfig{
		Type: "canary",
		Canary: &config.CanaryConfig{
			Steps:    []int{10, 50, 100},
			Analysis: ".github/workflows/canary-analysis.yaml",
		},
	}

	cfgA := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Rollout:  rolloutWithReserved,
			},
		},
	}

	cfgB := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Rollout:  rolloutWithoutReserved,
			},
		},
	}

	gA := NewPromoteGenerator(cfgA, t.TempDir())
	outA, err := gA.Generate()
	require.NoError(t, err)

	gB := NewPromoteGenerator(cfgB, t.TempDir())
	outB, err := gB.Generate()
	require.NoError(t, err)

	// Guard against a vacuous comparison of two empty strings: the generator
	// must have produced a substantial workflow for the equality to be meaningful.
	require.NotEmpty(t, outA, "generated output must be non-empty")
	require.Greater(t, len(outA), 1024, "generated workflow should be substantial")

	require.True(t, bytes.Equal([]byte(outA), []byte(outB)),
		"reserved canary fields must not affect generated output:\nwith fields:\n%s\nwithout fields:\n%s", outA, outB)
}

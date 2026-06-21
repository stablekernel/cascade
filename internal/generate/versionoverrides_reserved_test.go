package generate

import (
	"bytes"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// TestVersionOverridesReservedFieldIsByteIdentical asserts that populating the
// reserved release.version_overrides.dir pointer produces byte-identical output
// to a config that leaves the block absent. The pointer is reserved shape but is
// not wired to generation.
func TestVersionOverridesReservedFieldIsByteIdentical(t *testing.T) {
	t.Parallel()

	// Case A: reserved version_overrides pointer populated.
	cfgA := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
			},
		},
		Release: &config.ReleaseConfig{
			VersionOverrides: &config.VersionOverridesConfig{
				Dir: ".cascade/version-overrides",
			},
		},
	}

	// Case B: same base fields, reserved version_overrides block absent.
	cfgB := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
			},
		},
		Release: &config.ReleaseConfig{},
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
		"reserved version_overrides field must not affect generated output:\nwith field:\n%s\nwithout field:\n%s", outA, outB)
}

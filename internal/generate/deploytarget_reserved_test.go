package generate

import (
	"bytes"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// TestDeployTargetReservedFieldsAreByteIdentical asserts that populating the
// reserved GitOps deploy_target fields (branch, track_sha) produces
// byte-identical output to a config that leaves them empty. These fields are
// reserved shape but not yet wired to generation.
func TestDeployTargetReservedFieldsAreByteIdentical(t *testing.T) {
	t.Parallel()

	// Case A: reserved GitOps fields populated.
	targetWithReserved := &config.DeployTarget{
		Mode:     config.DeployTargetModeGitOps,
		Repo:     "org/gitops-config",
		Path:     "envs/prod/values.yaml",
		Field:    "image.tag",
		Value:    "v1.2.3",
		Branch:   "release/prod",
		TrackSHA: true,
	}

	// Case B: same base fields, reserved GitOps fields absent.
	targetWithoutReserved := &config.DeployTarget{
		Mode:  config.DeployTargetModeGitOps,
		Repo:  "org/gitops-config",
		Path:  "envs/prod/values.yaml",
		Field: "image.tag",
		Value: "v1.2.3",
	}

	cfgA := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:         "app",
				Workflow:     ".github/workflows/deploy-app.yaml",
				DeployTarget: targetWithReserved,
			},
		},
	}

	cfgB := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:         "app",
				Workflow:     ".github/workflows/deploy-app.yaml",
				DeployTarget: targetWithoutReserved,
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
		"reserved deploy_target fields must not affect generated output:\nwith fields:\n%s\nwithout fields:\n%s", outA, outB)
}

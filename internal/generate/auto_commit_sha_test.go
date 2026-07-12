package generate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// TestAnyAutoCommits_Build reports true when a build declares auto_commits.
func TestAnyAutoCommits_Build(t *testing.T) {
	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", AutoCommits: true},
		},
	}
	g := NewGenerator(cfg, "")
	require.True(t, g.anyAutoCommits())
}

// TestAnyAutoCommits_Deploy reports true when a deploy declares auto_commits.
func TestAnyAutoCommits_Deploy(t *testing.T) {
	cfg := &config.TrunkConfig{
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", AutoCommits: true},
		},
	}
	g := NewGenerator(cfg, "")
	require.True(t, g.anyAutoCommits())
}

// TestAnyAutoCommits_None reports false when no callback declares auto_commits.
func TestAnyAutoCommits_None(t *testing.T) {
	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml"},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}
	g := NewGenerator(cfg, "")
	require.False(t, g.anyAutoCommits())
}

// TestManifestUpdateStep_AutoCommitsReResolvesHEAD checks that the generated
// manifest update step reassigns HEAD_SHA from git rev-parse HEAD when any
// callback has auto_commits: true.
func TestManifestUpdateStep_AutoCommitsReResolvesHEAD(t *testing.T) {
	cfg := &config.TrunkConfig{
		Environments: config.EnvNames("dev", "test"),
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", AutoCommits: true},
		},
	}
	g := NewGenerator(cfg, "")
	// Ensure outputs/inputs maps are initialised (discoverOutputsAndInputs
	// requires a real workflow file; skip it and rely on empty maps, which is
	// fine for this shell-generation test).
	var sb strings.Builder
	g.writeManifestUpdateStep(&sb, []string{"deploy-app"})
	output := sb.String()

	require.Contains(t, output, `HEAD_SHA="$(git rev-parse HEAD)"`,
		"generated script must re-resolve HEAD_SHA when auto_commits is set")
}

// TestManifestUpdateStep_NoAutoCommits_NoReResolve checks that the generated
// manifest update step does NOT include a git rev-parse line when no callback
// declares auto_commits: true, preserving existing behavior.
func TestManifestUpdateStep_NoAutoCommits_NoReResolve(t *testing.T) {
	cfg := &config.TrunkConfig{
		Environments: config.EnvNames("dev", "test"),
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}
	g := NewGenerator(cfg, "")
	var sb strings.Builder
	g.writeManifestUpdateStep(&sb, []string{"deploy-app"})
	output := sb.String()

	require.NotContains(t, output, `git rev-parse HEAD`,
		"generated script must NOT re-resolve HEAD_SHA when no auto_commits callback exists")
}

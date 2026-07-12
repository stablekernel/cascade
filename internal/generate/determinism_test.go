package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// writeDeterminismWorkflows lays down the reusable-workflow stubs referenced by
// the determinism manifest and returns the base directory the generators read.
func writeDeterminismWorkflows(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github/workflows"), 0o755))

	imageBuild := `
name: Image Build
on:
  workflow_call:
    inputs:
      os:
        type: string
      arch:
        type: string
    outputs:
      image:
        value: test
`
	bundleBuild := `
name: Bundle Build
on:
  workflow_call:
    inputs:
      image:
        type: string
    outputs:
      bundle:
        value: test
`
	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        type: string
      bundle:
        type: string
`
	files := map[string]string{
		".github/workflows/image-build.yaml":  imageBuild,
		".github/workflows/bundle-build.yaml": bundleBuild,
		".github/workflows/deploy.yaml":       deployWorkflow,
	}
	for path, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644))
	}
	return dir
}

// determinismConfig returns a representative multi-environment manifest with a
// matrix image build, a dependent bundle build, and a deploy that depends on the
// bundle. This produces multiple jobs, multi-entry needs: lists, and multi-entry
// if: skip-gate conditions, exercising every order-sensitive emission path.
func determinismConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "staging", "prod", "canary"),
		Builds: []config.BuildConfig{
			{
				Name:     "image",
				Workflow: ".github/workflows/image-build.yaml",
				Triggers: []string{"src/**"},
				Matrix: &config.MatrixConfig{
					Dimensions: map[string][]string{
						"os":   {"linux", "darwin", "windows"},
						"arch": {"amd64", "arm64"},
					},
				},
				// A second callback contributing to the top-level permissions
				// union, so the union spans multiple nodes (map iteration order
				// across nodes must not leak into output).
				Permissions: map[string]string{"attestations": "write"},
			},
			{
				Name:      "bundle",
				Workflow:  ".github/workflows/bundle-build.yaml",
				Triggers:  []string{"bundle/**"},
				DependsOn: []string{"image"},
			},
			// docs is an independent build root (no depends_on). Multiple
			// roots are what surface toposort seed-order non-determinism:
			// a single linear chain would be stable regardless of seed.
			{
				Name:     "docs",
				Workflow: ".github/workflows/bundle-build.yaml",
				Triggers: []string{"docs/**"},
			},
		},
		Deploys: []config.DeployConfig{
			{
				Name:      "app",
				Workflow:  ".github/workflows/deploy.yaml",
				Triggers:  []string{"src/**"},
				DependsOn: []string{"bundle"},
				// Per-callback permissions exercise the top-level permissions
				// union, whose appended scopes are map-sourced and must emit in a
				// stable sorted order run to run.
				Permissions: map[string]string{
					"id-token": "write",
					"packages": "read",
				},
			},
			// sidecar is an independent deploy root, again adding parallel
			// nodes to the graph so seed order can diverge run to run.
			{
				Name:     "sidecar",
				Workflow: ".github/workflows/deploy.yaml",
				Triggers: []string{"sidecar/**"},
			},
		},
		// External repos make this a primary repo, exercising external-update.yaml
		// and the external-deploy branches of promote.yaml. Multiple repos and
		// deploys give the external emission paths multiple entries to order.
		External: []config.ExternalRepoConfig{
			{
				Repo: "org/infra",
				Deploys: []config.ExternalDeployConfig{
					{Name: "cdk", Workflow: "org/infra/.github/workflows/deploy.yaml@main", Triggers: []string{"infra/**"}},
					{Name: "dns", Workflow: "org/infra/.github/workflows/deploy.yaml@main", Triggers: []string{"dns/**"}},
				},
			},
			{
				Repo: "org/data",
				Deploys: []config.ExternalDeployConfig{
					{Name: "etl", Workflow: "org/data/.github/workflows/deploy.yaml@main", Triggers: []string{"etl/**"}},
				},
			},
		},
	}
}

// generateAll produces every workflow file the determinism contract covers for
// the given config, keyed by a stable file label. It generates in a single
// process so repeated calls within one test exercise Go's per-range map-order
// randomization.
func generateAll(t *testing.T, cfg *config.TrunkConfig, dir string) map[string]string {
	t.Helper()
	out := make(map[string]string)

	orchestrate, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	out["orchestrate.yaml"] = orchestrate

	promote, err := NewPromoteGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	out["promote.yaml"] = promote

	hotfix, err := NewHotfixGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	out["cascade-hotfix.yaml"] = hotfix

	rollback, err := NewRollbackGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	out["cascade-rollback.yaml"] = rollback

	external, err := NewExternalUpdateGenerator(cfg, dir).Generate()
	require.NoError(t, err)
	out["external-update.yaml"] = external

	return out
}

// TestGeneration_Deterministic_MultiEnv regenerates the same multi-environment
// manifest many times in one process and asserts every generated file is
// byte-identical across all runs. Go randomizes map range order per range
// statement per process, so any emission that derives order from a Go map
// iteration would diverge across these iterations.
func TestGeneration_Deterministic_MultiEnv(t *testing.T) {
	dir := writeDeterminismWorkflows(t)
	cfg := determinismConfig()

	const runs = 20
	baseline := generateAll(t, cfg, dir)
	require.NotEmpty(t, baseline)

	for i := 1; i < runs; i++ {
		got := generateAll(t, cfg, dir)
		require.Equal(t, len(baseline), len(got), "run %d produced a different set of files", i)
		for name, want := range baseline {
			require.Equal(t, want, got[name],
				"run %d produced non-deterministic output for %s", i, name)
		}
	}
}

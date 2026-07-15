package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCallbackWorkflows writes minimal reusable workflows into
// dir/.github/workflows so output/input discovery has files to parse.
func writeCallbackWorkflows(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github/workflows"), 0755))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".github/workflows", name), []byte(content), 0644))
	}
}

// TestGenerator_FinalizeOutputKeysUseJobID proves the finalize job's outputs:
// keys derive from the collision-safe job ID, not the bare callback name. A
// build and a deploy may legally share a name; keying on the name emits the
// same YAML mapping key twice, which is a duplicate-key parse hazard.
func TestGenerator_FinalizeOutputKeysUseJobID(t *testing.T) {
	tmpDir := t.TempDir()
	writeCallbackWorkflows(t, tmpDir, map[string]string{
		"build.yaml": `
on:
  workflow_call:
    outputs:
      digest:
        value: ${{ jobs.build.outputs.digest }}
`,
		"deploy.yaml": `
on:
  workflow_call:
    outputs:
      digest:
        value: ${{ jobs.deploy.outputs.digest }}
`,
	})

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"deploy/**"}},
		},
	}

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "build_app_digest: ${{ needs.build-app.outputs.digest }}",
		"build output key must carry the job identity")
	assert.Contains(t, result, "deploy_app_digest: ${{ needs.deploy-app.outputs.digest }}",
		"deploy output key must carry the job identity")

	if n := strings.Count(result, "      app_digest:"); n > 0 {
		t.Errorf("name-derived output key app_digest emitted %d times; a shared build/deploy name duplicates it", n)
	}
}

// TestGenerator_UnresolvedStateRefFailsGeneration proves an operator input
// referencing missing manifest state fails generation loudly, per the expr.go
// contract, instead of emitting a dead ${{ state.* }} ref that GitHub Actions
// rejects (state is not a runtime context).
func TestGenerator_UnresolvedStateRefFailsGeneration(t *testing.T) {
	tmpDir := t.TempDir()
	writeCallbackWorkflows(t, tmpDir, map[string]string{
		"build.yaml": `
on:
  workflow_call:
    inputs:
      prior_sha:
        type: string
`,
	})

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				Inputs: map[string]interface{}{
					"prior_sha": "${{ state.prod.sha }}",
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	// No state threaded: the reference cannot resolve.
	_, err := gen.Generate()
	require.Error(t, err, "generation must fail on an unresolved state reference")
	assert.Contains(t, err.Error(), "state.prod.sha")
}

// TestGenerator_ExternalReleaseDotlessTagErrors proves the exported Generate
// returns an error, never panics, when release_build.tag lacks the
// callback.output form and CLI-side validation was skipped.
func TestGenerator_ExternalReleaseDotlessTagErrors(t *testing.T) {
	tmpDir := t.TempDir()
	writeCallbackWorkflows(t, tmpDir, map[string]string{
		"build.yaml": "on:\n  workflow_call:\n",
	})

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{Name: "goreleaser", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		ReleaseBuild: &config.ReleaseBuildConfig{Tag: "goreleaser"},
	}

	var result string
	var err error
	require.NotPanics(t, func() {
		result, err = NewGenerator(cfg, tmpDir).Generate()
	})
	require.Error(t, err, "a dotless release_build.tag must fail generation, got:\n%s", result)
	assert.Contains(t, err.Error(), "release_build.tag")
}

// TestReleaseGenerator_BranchResolutionHandlesTagRef proves the emitted
// finalize step resolves the state push branch with an explicit refs/heads/
// case: release.yaml is dispatched with --ref <tag>, where the old
// prefix-strip left BRANCH=refs/tags/... and the trunk fallback never fired,
// so finalize pushed against a bogus branch until the retries exhausted.
func TestReleaseGenerator_BranchResolutionHandlesTagRef(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assert.Contains(t, content, `case "$GITHUB_REF" in`,
		"branch resolution must switch on the ref type")
	assert.Contains(t, content, `refs/heads/*) BRANCH="${GITHUB_REF#refs/heads/}"`,
		"a branch ref must strip to the branch name")
	assert.Contains(t, content, `*) BRANCH="main"`,
		"any non-branch ref (tag dispatch) must fall back to trunk")
	assert.NotContains(t, content, `BRANCH="${GITHUB_REF##refs/heads/}"`,
		"the prefix-strip form leaves refs/tags/* values intact and defeats the trunk fallback")
}

// TestDriftCheckGenerator_AbsoluteManifestPathRebased proves the drift-check
// lane routes the manifest path through the repo-relative rebase every sibling
// generator uses, so an absolute manifest_file on the generating machine is
// not baked into the emitted workflow.
func TestDriftCheckGenerator_AbsoluteManifestPathRebased(t *testing.T) {
	baseDir := t.TempDir()
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		ManifestFile: filepath.Join(baseDir, ".github/manifest.yaml"),
		DriftCheck:   &config.DriftCheckConfig{Enabled: true, Comment: true},
	}

	gen := NewDriftCheckGenerator(cfg, baseDir)

	check, err := gen.Generate()
	require.NoError(t, err)
	comment, err := gen.GenerateComment()
	require.NoError(t, err)

	for name, content := range map[string]string{"check": check, "comment": comment} {
		assert.NotContains(t, content, baseDir,
			"%s workflow must not bake the generating machine's absolute path", name)
	}
	assert.Contains(t, check, "cascade verify --config .github/manifest.yaml")
	assert.Contains(t, check, "cascade generate-workflow --config .github/manifest.yaml")
	assert.Contains(t, comment, "cascade generate-workflow --config .github/manifest.yaml")
}

// TestPromoteGenerator_UnresolvedEnvStateRefStaysVisible proves an env input
// whose state reference cannot resolve survives into the generated matrix
// payload as the raw expression, failing loudly at workflow parse, instead of
// being silently deleted so the deploy runs without its configured input.
func TestPromoteGenerator_UnresolvedEnvStateRefStaysVisible(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy.yaml",
				Inputs: map[string]interface{}{
					"base_sha": "${{ state.dev.builds.missing.sha }}",
				},
				EnvInputs: map[string]map[string]interface{}{
					"prod": {"prior_version": "${{ state.prod.deploys.missing.version }}"},
				},
			},
		},
	}

	content, err := NewPromoteGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "state.dev.builds.missing.sha",
		"an unresolved default-level state ref must stay visible, not vanish")
	assert.Contains(t, content, "state.prod.deploys.missing.version",
		"an unresolved env-level state ref must stay visible, not vanish")
}

// TestReconcileGenerator_EmitsManifestFlags proves the emitted cascade
// reconcile invocations carry --config and --manifest-key, so a repo with a
// non-default manifest path or key reconciles the manifest it was generated
// from rather than the default.
func TestReconcileGenerator_EmitsManifestFlags(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		ManifestFile: ".github/custom-manifest.yaml",
		ManifestKey:  "cascade",
		Reconcile:    &config.ReconcileConfig{Enabled: true},
	}

	gen := NewReconcileGenerator(cfg, "")

	check, err := gen.Generate()
	require.NoError(t, err)
	companion, err := gen.GenerateCompanion()
	require.NoError(t, err)

	assert.Contains(t, companion, `--config ".github/custom-manifest.yaml"`,
		"companion reconcile must name the manifest it reconciles")
	assert.Contains(t, companion, `--manifest-key "cascade"`,
		"companion reconcile must name the manifest key")
	assert.Contains(t, check, `--config ".github/custom-manifest.yaml"`,
		"detector must name the manifest for consistency with the companion")
	assert.Contains(t, check, `--manifest-key "cascade"`,
		"detector must name the manifest key")
}

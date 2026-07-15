package generate

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// manifestPathBuildStub is the minimal reusable workflow the orchestrate
// generator's output/input discovery pass needs on disk.
const manifestPathBuildStub = `
on:
  workflow_call:
    outputs:
      digest:
        value: ${{ jobs.build.outputs.digest }}
`

// manifestPathConfig builds a config that enables every lane whose emitted
// output embeds the manifest path, with the path spelled as given.
func manifestPathConfig(manifestFile string) *config.TrunkConfig {
	enabled := true
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		ManifestFile: manifestFile,
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		External: []config.ExternalRepoConfig{
			{
				Repo: "example/cdk-infra",
				Ref:  "main",
				Deploys: []config.ExternalDeployConfig{
					{Name: "cdk", Workflow: "example/cdk-infra/.github/workflows/deploy.yaml"},
				},
			},
		},
		Reconcile: &config.ReconcileConfig{Enabled: true},
		PRPreview: &config.PRPreviewConfig{Enabled: &enabled, Comment: &enabled},
	}
}

// renderManifestPathLanes renders every emitted document that embeds the
// manifest path via a previously raw sink, keyed by lane name.
func renderManifestPathLanes(t *testing.T, cfg *config.TrunkConfig, baseDir string) map[string]string {
	t.Helper()
	out := make(map[string]string)

	orchestrate, err := NewGenerator(cfg, baseDir).Generate()
	require.NoError(t, err)
	out["orchestrate"] = orchestrate

	external, err := NewExternalUpdateGenerator(cfg, baseDir).Generate()
	require.NoError(t, err)
	out["external"] = external

	reconcileGen := NewReconcileGenerator(cfg, baseDir)
	check, err := reconcileGen.Generate()
	require.NoError(t, err)
	out["reconcile-check"] = check
	companion, err := reconcileGen.GenerateCompanion()
	require.NoError(t, err)
	out["reconcile-companion"] = companion

	preview, err := NewPRPreviewGenerator(cfg, baseDir).Generate()
	require.NoError(t, err)
	out["pr-preview"] = preview

	return out
}

// TestGenerators_AbsoluteManifestPathRelativizedInOutput proves every lane
// that embeds the manifest path in emitted output rebases an absolute
// manifest_file to the repo-relative path, matching the sibling generators.
// An auto-detected manifest resolves through the working directory to an
// absolute path (config.FindConfigFile), so a raw embed bakes the generating
// machine's local layout into a committed workflow: the output is then
// non-reproducible across machines and the drift gate false-positives on a
// byte-clean tree.
func TestGenerators_AbsoluteManifestPathRelativizedInOutput(t *testing.T) {
	baseDir := t.TempDir()
	writeCallbackWorkflows(t, baseDir, map[string]string{"build.yaml": manifestPathBuildStub})

	cfg := manifestPathConfig(filepath.Join(baseDir, ".github/manifest.yaml"))

	for lane, content := range renderManifestPathLanes(t, cfg, baseDir) {
		assert.NotContains(t, content, baseDir,
			"%s output must not embed the generating machine's absolute path", lane)
		assert.Contains(t, content, "cascade generate-workflow --config .github/manifest.yaml",
			"%s header must name the repo-relative manifest path", lane)
	}
}

// TestGenerators_ManifestPathSpellingIsByteIdentical proves the emitted bytes
// do not depend on how the manifest path was spelled at invocation: a relative
// --config and the equivalent absolute path (what auto-detect produces) yield
// identical output, so regenerating on any machine reproduces the committed
// files and verify stays drift-free.
func TestGenerators_ManifestPathSpellingIsByteIdentical(t *testing.T) {
	baseDir := t.TempDir()
	writeCallbackWorkflows(t, baseDir, map[string]string{"build.yaml": manifestPathBuildStub})

	relative := renderManifestPathLanes(t, manifestPathConfig(".github/manifest.yaml"), baseDir)
	absolute := renderManifestPathLanes(t, manifestPathConfig(filepath.Join(baseDir, ".github/manifest.yaml")), baseDir)

	require.Equal(t, len(relative), len(absolute))
	for lane, relContent := range relative {
		assert.Equal(t, relContent, absolute[lane],
			"%s output must be byte-identical for relative and absolute manifest paths", lane)
	}
}

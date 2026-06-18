package generate

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// chdir changes the working directory to dir for the duration of the test,
// restoring the original on cleanup. The generate path and Plan both resolve
// relative workflow paths against the working directory, so a test comparing
// their emitted sets must run with the repo root as the working directory.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(orig))
	})
}

// writePlanManifest writes the determinism manifest plus its reusable-workflow
// stubs into a temp repo and returns the repo root. Plan and the existing
// generate path are both rooted at this directory so their emitted file sets can
// be compared byte for byte.
func writePlanManifest(t *testing.T) string {
	t.Helper()
	dir := writeDeterminismWorkflows(t)

	cfg := determinismConfig()
	manifest := map[string]any{
		config.DefaultManifestKey: config.CICDFile{Config: cfg},
	}
	body, err := yaml.Marshal(manifest)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "manifest.yaml"), body, 0o644))
	return dir
}

// walkGeneratedFiles walks every workflow and action file the generate path
// wrote under dir, returning a path->content map keyed by the path relative to
// dir so it can be compared to Plan's emitted set.
func walkGeneratedFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	roots := []string{
		filepath.Join(dir, ".github", "workflows"),
		filepath.Join(dir, ".github", "actions"),
	}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(dir, path)
			require.NoError(t, rerr)
			// Skip the reusable-workflow stubs the test laid down by hand.
			switch rel {
			case filepath.Join(".github", "workflows", "image-build.yaml"),
				filepath.Join(".github", "workflows", "bundle-build.yaml"),
				filepath.Join(".github", "workflows", "deploy.yaml"):
				return nil
			}
			content, rerr := os.ReadFile(path)
			require.NoError(t, rerr)
			out[rel] = string(content)
			return nil
		})
		require.NoError(t, err)
	}
	return out
}

// TestPlan_MatchesGeneratedBytes proves Plan returns a {Path, Content} set that
// is byte-identical to what the existing generate path writes to disk for a
// representative multi-environment manifest. This is the keystone guard for the
// verify refactor: if Plan ever diverges from generate, verify would falsely
// report drift.
func TestPlan_MatchesGeneratedBytes(t *testing.T) {
	dir := writePlanManifest(t)
	chdir(t, dir)
	// Resolve symlinks so dir matches the path the generator derives from
	// os.Getwd (on macOS /var is a symlink to /private/var); otherwise the
	// absolute composite-action path would not be relative to dir.
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = resolved

	// Run the existing generate path into the repo, using the relative paths the
	// CLI uses by default so the hardcoded workflow paths resolve identically.
	opts := generateOptions{
		configPath:        ".github/manifest.yaml",
		manifestKey:       config.DefaultManifestKey,
		actionFolder:      "manage-release",
		outputPath:        ".github/workflows/orchestrate.yaml",
		promoteOutputPath: ".github/workflows/promote.yaml",
		force:             true,
	}
	require.NoError(t, runGenerateWorkflow(opts))

	written := walkGeneratedFiles(t, dir)

	planned, err := Plan(PlanOptions{
		ConfigPath:        ".github/manifest.yaml",
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        ".github/workflows/orchestrate.yaml",
		PromoteOutputPath: ".github/workflows/promote.yaml",
	})
	require.NoError(t, err)
	require.NotEmpty(t, planned)

	// Plan must be sorted by Path.
	paths := make([]string, len(planned))
	for i, p := range planned {
		paths[i] = p.Path
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	require.Equal(t, sorted, paths, "Plan output must be sorted by Path")

	// Compare Plan's emitted set to the bytes generate wrote. Plan paths are a
	// mix of relative (workflows) and absolute (composite action under baseDir);
	// normalize both against dir for comparison.
	plannedByRel := make(map[string]string, len(planned))
	for _, p := range planned {
		abs := p.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, abs)
		}
		rel, rerr := filepath.Rel(dir, abs)
		require.NoError(t, rerr)
		plannedByRel[rel] = p.Content
	}

	require.Equal(t, len(written), len(plannedByRel),
		"Plan emitted a different number of files than generate wrote")
	for rel, want := range written {
		got, ok := plannedByRel[rel]
		require.Truef(t, ok, "Plan did not emit %s that generate wrote", rel)
		require.Equalf(t, want, got, "Plan content for %s differs from generated bytes", rel)
	}
}

// TestPlan_Deterministic asserts Plan returns an identical set across repeated
// calls in one process, guarding the #174 determinism contract through the
// plan-set boundary that verify consumes.
func TestPlan_Deterministic(t *testing.T) {
	dir := writePlanManifest(t)
	chdir(t, dir)

	planOpts := PlanOptions{
		ConfigPath:        ".github/manifest.yaml",
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        ".github/workflows/orchestrate.yaml",
		PromoteOutputPath: ".github/workflows/promote.yaml",
	}

	baseline, err := Plan(planOpts)
	require.NoError(t, err)
	require.NotEmpty(t, baseline)

	const runs = 10
	for i := 1; i < runs; i++ {
		got, gerr := Plan(planOpts)
		require.NoError(t, gerr)
		require.Equalf(t, baseline, got, "Plan run %d diverged from baseline", i)
	}
}

package generate

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// updateGolden rewrites the byte-identical baseline goldens when set. Run:
//
//	go test ./internal/generate/ -run TestByteIdenticalBaseline -update
//
// A maintainer regenerates the goldens only for a deliberate, reviewed change to
// the single-component generated output. An accidental perturbation reds the gate
// instead of silently updating.
var updateGolden = flag.Bool("update", false, "update golden files")

// byteIdenticalGoldenDir holds the frozen single-component output snapshot the
// baseline gate compares against.
var byteIdenticalGoldenDir = filepath.Join("testdata", "byte_identical_baseline")

// TestByteIdenticalBaseline_SingleComponent is the authoritative byte-identical
// baseline gate for the multi-component campaign. The load-bearing invariant it
// protects: a manifest with NO components: block produces output byte-for-byte
// identical to the pre-component behavior, so no future change to the component
// machinery can silently perturb an existing single-component repo.
//
// This is the consolidated capstone over three per-surface increments, each of
// which locks its own layer with a relative oracle:
//
//   - config resolution: TestNoComponents_SingleComponentPathUntouched (a manifest
//     with no components: reports no components and resolves the untouched path).
//   - state serialization: TestWriteManifestState_ByteIdenticalAcrossCases and
//     TestWriteManifestState_PublishDropsPrereleaseByteIdentical (the scoped
//     serializer equals the whole-node-replace oracle for every single-component
//     case, including the publish delete).
//   - workflow generation: TestOrchestrateTargets_SingleComponent_ByteIdentical
//     (the fan-out seam returns the pre-component generator for a no-components
//     manifest) and TestPlan_MatchesGeneratedBytes (Plan equals generate on disk).
//
// Those siblings are all RELATIVE oracles: they prove the component path reduces
// to the pre-component function, but they move together with any deliberate
// template edit, so none freezes the actual bytes. This gate closes that gap for
// the full workflow-output surface. It generates the complete output for a
// realistic multi-environment single-component manifest that exercises the builds,
// deploys, external, promote, release-publish, hotfix, and rollback surfaces
// (hotfix and rollback auto-enable at two or more environments), then asserts:
//
//   1. every emitted file is byte-for-byte equal to a committed golden fixture,
//      so any single-component byte change reds the gate;
//   2. the emitted set carries no components-keyed artifact: no
//      orchestrate-<name>.yaml fan-out file, exactly one repo-wide orchestrate.yaml,
//      no component-namespaced workflow name, and no components: key in any body;
//   3. the golden set matches the emitted set exactly, so adding or dropping an
//      output file also reds the gate.
func TestByteIdenticalBaseline_SingleComponent(t *testing.T) {
	// Anchor the golden directory to the package directory before chdir moves the
	// working directory into the temp repo, so goldens read from and write to the
	// committed testdata tree rather than the temporary generate root.
	pkgDir, err := os.Getwd()
	require.NoError(t, err)
	goldenDir := filepath.Join(pkgDir, byteIdenticalGoldenDir)

	dir := writePlanManifest(t)
	chdir(t, dir)
	// Resolve symlinks so the absolute composite-action path Plan derives from
	// os.Getwd is relative to dir (on macOS /var is a symlink to /private/var).
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	dir = resolved

	planned, err := Plan(PlanOptions{
		ConfigPath:        ".github/manifest.yaml",
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        ".github/workflows/orchestrate.yaml",
		PromoteOutputPath: ".github/workflows/promote.yaml",
	})
	require.NoError(t, err)
	require.NotEmpty(t, planned)

	// Normalize every planned path to a key relative to the repo root. Workflow
	// paths are already relative; the composite action path is absolute under
	// baseDir (== dir), so rebase it onto dir.
	byRel := make(map[string]string, len(planned))
	for _, p := range planned {
		abs := p.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(dir, abs)
		}
		rel, rerr := filepath.Rel(dir, abs)
		require.NoError(t, rerr)
		byRel[rel] = p.Content
	}

	// (2) No components-keyed artifact anywhere in the emitted set.
	orchestrateCount := 0
	for rel, content := range byRel {
		base := filepath.Base(rel)
		if strings.HasPrefix(base, "orchestrate-") {
			t.Errorf("single-component output emitted a component fan-out file %q; the no-components path must not fan out", rel)
		}
		if rel == filepath.Join(".github", "workflows", "orchestrate.yaml") {
			orchestrateCount++
		}
		require.NotContainsf(t, content, "Orchestrate CI/CD (", "%s carries a component-namespaced workflow name", rel)
		for _, line := range strings.Split(content, "\n") {
			require.Falsef(t, strings.HasPrefix(line, "components:"), "%s carries a top-level components: key", rel)
		}
	}
	require.Equalf(t, 1, orchestrateCount, "expected exactly one repo-wide orchestrate.yaml, got %d", orchestrateCount)

	// (1) + (3) Byte-for-byte equality against the committed golden set, and the
	// golden set matches the emitted set exactly.
	if *updateGolden {
		regenBaselineGoldens(t, goldenDir, byRel)
	}

	wantFiles := listBaselineGoldens(t, goldenDir)
	gotKeys := make([]string, 0, len(byRel))
	for rel := range byRel {
		gotKeys = append(gotKeys, goldenName(rel))
	}
	sort.Strings(gotKeys)
	require.Equalf(t, wantFiles, gotKeys,
		"emitted file set differs from the committed golden set; run with -update after a deliberate change")

	for rel, got := range byRel {
		goldenPath := filepath.Join(goldenDir, goldenName(rel))
		want, rerr := os.ReadFile(goldenPath)
		require.NoErrorf(t, rerr, "missing golden for %s; run with -update", rel)
		require.Equalf(t, string(want), got,
			"single-component output for %s drifted from the byte-identical baseline; run with -update only for a deliberate change", rel)
	}
}

// goldenName flattens a repo-relative output path into a single golden filename so
// the frozen baseline lives in one flat directory.
func goldenName(rel string) string {
	return strings.ReplaceAll(rel, string(os.PathSeparator), "__") + ".golden"
}

// regenBaselineGoldens rewrites the golden directory to exactly the emitted set,
// pruning any stale golden so a removed output file cannot linger.
func regenBaselineGoldens(t *testing.T, goldenDir string, byRel map[string]string) {
	t.Helper()
	require.NoError(t, os.RemoveAll(goldenDir))
	require.NoError(t, os.MkdirAll(goldenDir, 0o755))
	for rel, content := range byRel {
		require.NoError(t, os.WriteFile(filepath.Join(goldenDir, goldenName(rel)), []byte(content), 0o644))
	}
}

// listBaselineGoldens returns the sorted golden filenames committed under the
// baseline directory.
func listBaselineGoldens(t *testing.T, goldenDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(goldenDir)
	require.NoError(t, err, "golden directory missing; run with -update")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

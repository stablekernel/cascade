package plan

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// newRepo lays down a representative multi-environment manifest plus the
// reusable-workflow stubs it references, then materializes the full generated
// set on disk so a clean repo plans with no pending changes. It returns the repo
// root. All paths are absolute, so the tests never change the process working
// directory and stay parallel-safe.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755))

	stubs := map[string]string{
		".github/workflows/image-build.yaml": "" +
			"name: Image Build\non:\n  workflow_call:\n    inputs:\n      os:\n        type: string\n",
		".github/workflows/deploy.yaml": "" +
			"name: Deploy\non:\n  workflow_call:\n    inputs:\n      environment:\n        type: string\n",
	}
	for path, body := range stubs {
		require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644))
	}

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Builds: []config.BuildConfig{
			{Name: "image", Workflow: ".github/workflows/image-build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}, DependsOn: []string{"image"}},
		},
	}
	manifest := map[string]any{config.DefaultManifestKey: config.CICDFile{Config: cfg}}
	body, err := yaml.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "manifest.yaml"), body, 0o644))

	// Materialize the full generated set so a clean repo has zero pending change.
	// The plan options carry absolute paths rooted at dir, so the planned files
	// land in the temp repo without changing the working directory.
	planned, err := generate.Plan(planOpts(dir))
	require.NoError(t, err)
	for _, p := range planned {
		path := p.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(p.Content), 0o644))
	}
	return dir
}

// planOpts builds the generate plan options for a repo rooted at dir. Every path
// is absolute so Plan resolves the manifest, base directory, and emitted files
// without consulting the process working directory.
func planOpts(dir string) generate.PlanOptions {
	return generate.PlanOptions{
		ConfigPath:        filepath.Join(dir, ".github", "manifest.yaml"),
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        filepath.Join(dir, ".github", "workflows", "orchestrate.yaml"),
		PromoteOutputPath: filepath.Join(dir, ".github", "workflows", "promote.yaml"),
	}
}

// opts builds the plan options for a repo rooted at dir, mirroring planOpts so
// the plan run reads the same absolute paths the plan emitted.
func opts(dir string) Options {
	return Options{
		ConfigPath:        filepath.Join(dir, ".github", "manifest.yaml"),
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        filepath.Join(dir, ".github", "workflows", "orchestrate.yaml"),
		PromoteOutputPath: filepath.Join(dir, ".github", "workflows", "promote.yaml"),
	}
}

func TestRun_CleanRepo_NoPendingChanges(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.NoError(t, err)
	require.Contains(t, out.String(), "no pending changes")
	require.Empty(t, strings.TrimSpace(errOut.String()), "plan writes nothing to stderr on success")
}

func TestRun_MutatedFile_ReturnsNil_ShowsUnifiedDiff(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	target := filepath.Join(dir, ".github", "workflows", "orchestrate.yaml")
	original, err := os.ReadFile(target)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target, append(original, '\n', '#', ' ', 'x', '\n'), 0o644))

	var out, errOut bytes.Buffer
	err = Run(opts(dir), &out, &errOut)
	require.NoError(t, err, "a pending change is informational, not an error")

	report := out.String()
	require.Contains(t, report, "a/")
	require.Contains(t, report, "b/")
	require.Contains(t, report, "orchestrate.yaml")
	require.Contains(t, report, "would change")
}

func TestRun_MissingFile_RendersWholeFileAdd(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	target := filepath.Join(dir, ".github", "workflows", "promote.yaml")
	require.NoError(t, os.Remove(target))

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.NoError(t, err)

	report := out.String()
	require.Contains(t, report, "promote.yaml")
	require.Contains(t, report, "would change")
	require.Contains(t, report, "+++ b/", "missing file must render with a unified to-file header")

	// A whole-file add renders the generated body as additions. Read the planned
	// promote content and assert each of its non-empty lines appears as a "+"
	// addition in the diff, proving the committed (empty) side produced no real
	// context or removal lines for the file body.
	planned, perr := generate.Plan(planOpts(dir))
	require.NoError(t, perr)
	var promoteBody string
	for _, p := range planned {
		if strings.HasSuffix(p.Path, "promote.yaml") {
			promoteBody = p.Content
			break
		}
	}
	require.NotEmpty(t, promoteBody, "fixture must plan a promote.yaml")
	for _, line := range strings.Split(promoteBody, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		require.Contains(t, report, "+"+line, "every promote line must appear as an addition")
	}
}

func TestRun_Deterministic_SameStdoutTwice(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	target := filepath.Join(dir, ".github", "workflows", "orchestrate.yaml")
	original, err := os.ReadFile(target)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target, append(original, '\n', '#', ' ', 'x', '\n'), 0o644))

	var first, second bytes.Buffer
	var e1, e2 bytes.Buffer
	require.NoError(t, Run(opts(dir), &first, &e1))
	require.NoError(t, Run(opts(dir), &second, &e2))
	require.Equal(t, first.String(), second.String(), "plan stdout must be byte-identical across runs")
}

// snapshotTree records every regular file under root and its bytes, so a test
// can assert plan changed nothing on disk.
func snapshotTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	snap := map[string][]byte{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		snap[path] = b
		return nil
	})
	require.NoError(t, err)
	return snap
}

func TestRun_ReadOnly_NothingChangesOnDisk(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	// Drive a real diff so plan has work to do, then prove it wrote nothing.
	target := filepath.Join(dir, ".github", "workflows", "orchestrate.yaml")
	original, err := os.ReadFile(target)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target, append(original, '\n', '#', ' ', 'x', '\n'), 0o644))

	before := snapshotTree(t, dir)

	var out, errOut bytes.Buffer
	require.NoError(t, Run(opts(dir), &out, &errOut))

	after := snapshotTree(t, dir)
	require.Equal(t, before, after, "plan must not write, create, or delete any file")
}

func TestRun_ManifestAbsent_OperationalError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // No manifest: the plan cannot run.
	o := opts(dir)
	o.ConfigPath = filepath.Join(dir, "does-not-exist.yaml")

	var out, errOut bytes.Buffer
	err := Run(o, &out, &errOut)
	require.Error(t, err, "a missing manifest is an operational failure")
}

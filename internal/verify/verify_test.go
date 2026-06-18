package verify

import (
	"bytes"
	"errors"
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
// set on disk so a clean repo verifies with no drift. It returns the repo root.
// All paths are absolute, so the tests never change the process working
// directory and stay parallel-safe.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755))

	stubs := map[string]string{
		".github/workflows/image-build.yaml": "" +
			"name: Image Build\non:\n  workflow_call:\n    inputs:\n      os:\n        type: string\n",
		".github/workflows/bundle-build.yaml": "" +
			"name: Bundle Build\non:\n  workflow_call:\n    inputs:\n      image:\n        type: string\n",
		".github/workflows/deploy.yaml": "" +
			"name: Deploy\non:\n  workflow_call:\n    inputs:\n      environment:\n        type: string\n",
	}
	for path, body := range stubs {
		require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644))
	}

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "staging", "prod"},
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

	// Materialize the full generated set so a clean repo has zero drift. The
	// plan options carry absolute paths rooted at dir, so the planned files land
	// in the temp repo without changing the working directory.
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

// planOpts builds the generate plan options for a repo rooted at dir. Every
// path is absolute so Plan resolves the manifest, base directory, and emitted
// files without consulting the process working directory.
func planOpts(dir string) generate.PlanOptions {
	return generate.PlanOptions{
		ConfigPath:        filepath.Join(dir, ".github", "manifest.yaml"),
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        filepath.Join(dir, ".github", "workflows", "orchestrate.yaml"),
		PromoteOutputPath: filepath.Join(dir, ".github", "workflows", "promote.yaml"),
	}
}

// opts builds the verify options for a repo rooted at dir, mirroring planOpts so
// the verify run reads the same absolute paths the plan emitted.
func opts(dir string) Options {
	return Options{
		ConfigPath:        filepath.Join(dir, ".github", "manifest.yaml"),
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        filepath.Join(dir, ".github", "workflows", "orchestrate.yaml"),
		PromoteOutputPath: filepath.Join(dir, ".github", "workflows", "promote.yaml"),
	}
}

func TestRun_CleanRepo_NoDrift(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.NoError(t, err)
	require.Contains(t, out.String(), "no drift")
}

func TestRun_ChangedFile_ReportsOnlyThatFile(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	target := filepath.Join(dir, ".github", "workflows", "orchestrate.yaml")
	original, err := os.ReadFile(target)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target, append(original, '\n', '#', ' ', 'x'), 0o644))

	var out, errOut bytes.Buffer
	err = Run(opts(dir), &out, &errOut)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDrift), "changed file must be drift, got %v", err)

	report := errOut.String()
	require.Contains(t, report, "orchestrate.yaml")
	require.NotContains(t, report, "promote.yaml")
}

func TestRun_MissingFile_ReportsMissing(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	target := filepath.Join(dir, ".github", "workflows", "promote.yaml")
	require.NoError(t, os.Remove(target))

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDrift), "missing file must be drift, got %v", err)

	report := errOut.String()
	require.Contains(t, report, "promote.yaml")
	require.Contains(t, report, "missing")
}

func TestRun_UnrelatedFile_Ignored(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	// A workflow that is NOT in the plan must be ignored entirely.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".github", "workflows", "ci.yaml"),
		[]byte("name: CI\non: push\n"), 0o644))

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.NoError(t, err)
	require.NotContains(t, errOut.String(), "ci.yaml")
}

func TestRun_ManifestAbsent_OperationalNotDrift(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrDrift), "absent manifest must be operational, not drift")
}

func TestRun_Quiet_SuppressesReportBody(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	target := filepath.Join(dir, ".github", "workflows", "orchestrate.yaml")
	original, err := os.ReadFile(target)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target, append(original, '\n', '#', ' ', 'x'), 0o644))

	o := opts(dir)
	o.Quiet = true
	var out, errOut bytes.Buffer
	err = Run(o, &out, &errOut)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDrift))

	// No per-file report body under --quiet.
	require.NotContains(t, errOut.String(), "orchestrate.yaml")
	require.Empty(t, strings.TrimSpace(out.String()))
}

func TestRun_Deterministic_CleanTwice(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)

	for i := range 2 {
		var out, errOut bytes.Buffer
		err := Run(opts(dir), &out, &errOut)
		require.NoErrorf(t, err, "run %d expected clean", i)
	}
}

// exitCoder mirrors the interface cmd/cascade/main.go uses to map a returned
// error to a process exit code without importing this package.
type exitCoder interface{ ExitCode() int }

func TestRun_CleanRepo_ReturnsNil_ExitZero(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.NoError(t, err, "clean repo must return nil so the command exits 0")
}

func TestRun_Drift_ExitCodeOne(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	target := filepath.Join(dir, ".github", "workflows", "orchestrate.yaml")
	original, err := os.ReadFile(target)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target, append(original, '\n', '#', ' ', 'x'), 0o644))

	var out, errOut bytes.Buffer
	err = Run(opts(dir), &out, &errOut)
	require.True(t, errors.Is(err, ErrDrift), "drift must wrap ErrDrift, got %v", err)

	var ec exitCoder
	require.ErrorAs(t, err, &ec, "drift error must carry an exit code")
	require.Equal(t, 1, ec.ExitCode(), "drift maps to exit code 1")
}

func TestRun_OperationalFailure_ExitCodeTwo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // No manifest: the plan cannot run.

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrDrift), "operational failure is not drift")

	var ec exitCoder
	require.ErrorAs(t, err, &ec, "operational error must carry an exit code")
	require.Equal(t, 2, ec.ExitCode(), "operational failure maps to exit code 2")
}

func TestErrDrift_ExitCodeOne(t *testing.T) {
	t.Parallel()
	var ec exitCoder
	require.ErrorAs(t, error(ErrDrift), &ec)
	require.Equal(t, 1, ec.ExitCode())
}

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
		Environments: config.EnvNames("dev", "staging", "prod"),
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
	return planOptsWithCLIInstall(dir, "")
}

// planOptsWithCLIInstall is planOpts with an explicit --cli-install mode, for
// tests that need to plan (or re-plan) a repo generated in binary mode.
func planOptsWithCLIInstall(dir, cliInstall string) generate.PlanOptions {
	return generate.PlanOptions{
		ConfigPath:        filepath.Join(dir, ".github", "manifest.yaml"),
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        filepath.Join(dir, ".github", "workflows", "orchestrate.yaml"),
		PromoteOutputPath: filepath.Join(dir, ".github", "workflows", "promote.yaml"),
		CLIInstall:        cliInstall,
	}
}

// opts builds the verify options for a repo rooted at dir, mirroring planOpts so
// the verify run reads the same absolute paths the plan emitted.
func opts(dir string) Options {
	return optsWithCLIInstall(dir, "")
}

// optsWithCLIInstall is opts with an explicit --cli-install mode, mirroring
// planOptsWithCLIInstall so a verify Run reads back what a matching Plan wrote.
func optsWithCLIInstall(dir, cliInstall string) Options {
	return Options{
		ConfigPath:        filepath.Join(dir, ".github", "manifest.yaml"),
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        filepath.Join(dir, ".github", "workflows", "orchestrate.yaml"),
		PromoteOutputPath: filepath.Join(dir, ".github", "workflows", "promote.yaml"),
		CLIInstall:        cliInstall,
	}
}

// newRepoWithCLIInstall mirrors newRepo but plans and materializes the repo
// using the given --cli-install mode, so tests can build a binary-mode
// generated fixture the same way a real adopter's repo would look.
func newRepoWithCLIInstall(t *testing.T, cliInstall string) string {
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
		Environments: config.EnvNames("dev", "staging", "prod"),
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

	planned, err := generate.Plan(planOptsWithCLIInstall(dir, cliInstall))
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

func TestRun_OrphanedGeneratedFile_ReportsDrift(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	// A cascade-owned file (carrying the generated marker) that the manifest no
	// longer plans is an orphan and must drift.
	orphan := filepath.Join(dir, ".github", "workflows", "stale.yaml")
	require.NoError(t, os.WriteFile(orphan,
		[]byte(generate.GeneratedFileMarker+"\nname: Stale\non: push\n"), 0o644))

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDrift), "orphaned generated file must be drift, got %v", err)

	report := errOut.String()
	require.Contains(t, report, "stale.yaml")
	require.Contains(t, report, "orphaned")

	var ec exitCoder
	require.ErrorAs(t, err, &ec)
	require.Equal(t, 1, ec.ExitCode(), "orphan drift maps to exit code 1")
}

func TestRun_Orphan_AllowOrphans_NoDrift(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	orphan := filepath.Join(dir, ".github", "workflows", "stale.yaml")
	require.NoError(t, os.WriteFile(orphan,
		[]byte(generate.GeneratedFileMarker+"\nname: Stale\non: push\n"), 0o644))

	o := opts(dir)
	o.AllowOrphans = true
	var out, errOut bytes.Buffer
	err := Run(o, &out, &errOut)
	require.NoError(t, err, "--allow-orphans must suppress orphan drift")
	require.NotContains(t, errOut.String(), "stale.yaml")
}

func TestRun_HandWrittenFile_NeverOrphan(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	// A file WITHOUT the generated marker is hand-written and must never be
	// flagged as an orphan, even when the manifest does not plan it.
	handwritten := filepath.Join(dir, ".github", "workflows", "ci.yaml")
	require.NoError(t, os.WriteFile(handwritten,
		[]byte("name: CI\non: push\n"), 0o644))

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.NoError(t, err, "hand-written file must not be drift")
	require.NotContains(t, errOut.String(), "ci.yaml")
}

func TestRun_OwnRepoMarkedFile_NeverOrphan(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	// A file carrying the own-repo self-heal marker is cascade-generated but
	// intentionally outside the manifest plan, so it must never be flagged as an
	// orphan even though it sits in the scanned workflow directory.
	ownRepo := filepath.Join(dir, ".github", "workflows", "pin-reconcile.yaml")
	require.NoError(t, os.WriteFile(ownRepo,
		[]byte(generate.OwnRepoGeneratedFileMarker+"\nname: Pin Reconcile\non: push\n"), 0o644))

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.NoError(t, err, "own-repo marked file must not be reported as drift")
	require.NotContains(t, errOut.String(), "pin-reconcile.yaml")
}

func TestRun_PlainMarkedFile_StillOrphan_RegressionGuard(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	// Regression guard: adding the own-repo skip must not weaken real orphan
	// detection. A file carrying the plain GeneratedFileMarker that the manifest
	// no longer plans is still an orphan and must drift.
	plain := filepath.Join(dir, ".github", "workflows", "stale.yaml")
	require.NoError(t, os.WriteFile(plain,
		[]byte(generate.GeneratedFileMarker+"\nname: Stale\non: push\n"), 0o644))

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.True(t, errors.Is(err, ErrDrift), "plain-marked unplanned file must still be an orphan")
	require.Contains(t, errOut.String(), "stale.yaml")
	require.Contains(t, errOut.String(), "orphaned")
}

func TestRun_MultipleOrphans_SortedDeterministic(t *testing.T) {
	t.Parallel()
	dir := newRepo(t)
	for _, name := range []string{"zeta.yaml", "alpha.yaml", "mid.yaml"} {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, ".github", "workflows", name),
			[]byte(generate.GeneratedFileMarker+"\nname: X\non: push\n"), 0o644))
	}

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut)
	require.True(t, errors.Is(err, ErrDrift))

	report := errOut.String()
	ai := strings.Index(report, "alpha.yaml")
	mi := strings.Index(report, "mid.yaml")
	zi := strings.Index(report, "zeta.yaml")
	require.Greater(t, ai, -1)
	require.Greater(t, mi, ai, "orphans must be reported in sorted order")
	require.Greater(t, zi, mi, "orphans must be reported in sorted order")
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

// TestRun_CLIInstallBinary_NoDriftWhenModeMatches proves verify can correctly
// check a repo generated with --cli-install=binary: without CLIInstall wired
// through to the underlying Plan, every generator silently defaults to action
// mode internally, so a binary-mode repo would report spurious drift on every
// file whose Setup CLI step differs by mode even though nothing is out of
// sync.
func TestRun_CLIInstallBinary_NoDriftWhenModeMatches(t *testing.T) {
	t.Parallel()
	dir := newRepoWithCLIInstall(t, "binary")

	var out, errOut bytes.Buffer
	err := Run(optsWithCLIInstall(dir, "binary"), &out, &errOut)
	require.NoError(t, err, "a clean binary-mode repo must verify with no drift when CLIInstall matches; report:\n%s", errOut.String())
	require.Contains(t, out.String(), "no drift")
}

// TestRun_CLIInstallBinary_MismatchedModeIsRealDrift is the control for the
// test above: a binary-mode repo checked WITHOUT CLIInstall set (defaulting to
// action) must still report drift, proving the two modes produce genuinely
// different bytes and the prior test isn't passing by coincidence.
func TestRun_CLIInstallBinary_MismatchedModeIsRealDrift(t *testing.T) {
	t.Parallel()
	dir := newRepoWithCLIInstall(t, "binary")

	var out, errOut bytes.Buffer
	err := Run(opts(dir), &out, &errOut) // opts(dir) leaves CLIInstall unset (action)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDrift), "action-mode verify against a binary-mode repo must report drift, got %v", err)
}

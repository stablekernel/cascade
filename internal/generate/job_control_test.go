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

// jobBlock returns the YAML block for a single job (from "  <jobID>:" up to the
// next top-level job key or EOF) so assertions can be scoped to one job rather
// than the whole workflow.
func jobBlock(t *testing.T, content, jobID string) string {
	t.Helper()
	idx := strings.Index(content, "  "+jobID+":\n")
	require.GreaterOrEqual(t, idx, 0, "job %q not found", jobID)
	rest := content[idx:]
	lines := strings.SplitAfter(rest, "\n")
	var b strings.Builder
	b.WriteString(lines[0])
	for i := 1; i < len(lines); i++ {
		l := lines[i]
		// A new top-level job starts with exactly two spaces of indent followed
		// by a non-space (e.g. "  finalize:").
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.TrimSpace(l) != "" {
			break
		}
		b.WriteString(l)
	}
	return b.String()
}

func writeStubWorkflow(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, ".github/workflows", name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0755))
	require.NoError(t, os.WriteFile(p, []byte("on:\n  workflow_call:\n"), 0644))
}

// --- #37: cascade-owned job timeouts ---------------------------------------

// TestGenerator_OwnedJobsGetDefaultTimeout asserts setup and finalize (always
// cascade-owned) carry the default timeout-minutes when config.job_timeout_minutes
// is unset, so cascade's own jobs do not inherit GHA's 360-minute default.
func TestGenerator_OwnedJobsGetDefaultTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	want := "timeout-minutes: 30"
	assert.Contains(t, jobBlock(t, result, "setup"), want, "setup must carry the owned-job default timeout")
	assert.Contains(t, jobBlock(t, result, "finalize"), want, "finalize must carry the owned-job default timeout")
}

// TestGenerator_OwnedJobTimeoutConfigurable asserts config.job_timeout_minutes
// overrides the default on cascade-owned jobs.
func TestGenerator_OwnedJobTimeoutConfigurable(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:       "main",
		Environments:      []string{"dev"},
		JobTimeoutMinutes: 12,
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assert.Contains(t, jobBlock(t, result, "setup"), "timeout-minutes: 12")
	assert.Contains(t, jobBlock(t, result, "finalize"), "timeout-minutes: 12")
}

// TestGenerator_TimeoutNotOnReusableCallback asserts a reusable-workflow
// callback (jobs.<id>.uses) does NOT receive the owned-job timeout. The called
// workflow owns its own timeout.
func TestGenerator_TimeoutNotOnReusableCallback(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			// Reusable-workflow callback (uses:): no timeout-minutes.
			{Name: "reusable", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	reusable := jobBlock(t, result, "build-reusable")
	assert.Contains(t, reusable, "uses:", "sanity: reusable callback is a uses: caller")
	assert.NotContains(t, reusable, "timeout-minutes:", "reusable-workflow caller must not get a cascade timeout")
}

// --- #18: optional_depends_on ----------------------------------------------

// TestGenerator_OptionalDependsOnAddsNeedsWithoutGating asserts optional_depends_on
// adds the dep to needs: (ordering) but does NOT add a skip-gate to the if:
// condition, while a hard depends_on still gates.
func TestGenerator_OptionalDependsOnAddsNeedsWithoutGating(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "migrations", Workflow: ".github/workflows/build.yaml", Triggers: []string{"db/**"}},
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{
				Name:              "deploy-app",
				Workflow:          ".github/workflows/deploy.yaml",
				Triggers:          []string{"src/**"},
				DependsOn:         []string{"build:app"},
				OptionalDependsOn: []string{"build:migrations"},
			},
		},
	}

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	deploy := jobBlock(t, result, "deploy-deploy-app")

	// needs: must include BOTH the hard dep and the optional dep (ordering).
	assert.Regexp(t, `needs:.*build-app`, deploy, "hard dep in needs:")
	assert.Regexp(t, `needs:.*build-migrations`, deploy, "optional dep in needs: for ordering")

	// if: must skip-gate on the HARD dep but NOT on the optional dep. A skipped
	// optional dep must not skip this job.
	assert.Contains(t, deploy, "needs.build-app.result == 'success'",
		"hard depends_on still contributes a skip-gate")
	assert.NotContains(t, deploy, "needs.build-migrations.result == 'success'",
		"optional_depends_on must not contribute a skip-gate condition")
	assert.NotContains(t, deploy, "needs.build-migrations.result == 'skipped'",
		"optional_depends_on must not contribute any skip-gate condition")
}

// TestGenerator_OptionalDependsOnHonorsImplicitRunOnSkip documents the GHA
// semantics that make optional_depends_on sequence-without-gate: the explicit
// if: (which does not call always()) means a failed optional dep still skips the
// job, while a skipped optional dep (absent from the if:) does not. We assert the
// emitted shape that yields this: optional dep present in needs:, absent from if:.
func TestGenerator_OptionalDependsOnHonorsImplicitRunOnSkip(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "build", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{
				Name:              "deploy",
				Workflow:          ".github/workflows/deploy.yaml",
				Triggers:          []string{"src/**"},
				OptionalDependsOn: []string{"build:build"},
			},
		},
	}

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	deploy := jobBlock(t, result, "deploy-deploy")
	// The job's own change-detection gate is still present (it is not always()).
	assert.Contains(t, deploy, "needs.setup.outputs.run_deploy_deploy == 'true'",
		"job keeps its own run-policy/change-detection gate")
	// The optional dep sequences ordering only.
	assert.Regexp(t, `needs:.*build-build`, deploy, "optional dep in needs:")
	assert.NotContains(t, deploy, "needs.build-build.result",
		"optional dep must not appear in the skip-gate")
}

// TestGenerator_BothFieldsUnsetNonBreaking asserts that with neither field set,
// the only timeout-minutes additions are on cascade-owned jobs and no optional
// dependency wiring leaks in.
func TestGenerator_BothFieldsUnsetNonBreaking(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	// Callback (uses:) carries no timeout; setup/finalize do (owned default).
	assert.NotContains(t, jobBlock(t, result, "build-app"), "timeout-minutes:")
	assert.Contains(t, jobBlock(t, result, "setup"), "timeout-minutes: 30")
}

// TestGenerator_ExplicitTimeoutNotOnReusableCallback asserts that an explicit
// per-callback timeout_minutes is NOT emitted as a job-level timeout-minutes on
// a reusable-workflow (uses:) callback. GitHub forbids timeout-minutes on a job
// that calls a reusable workflow (allowed caller keys: name, uses, with,
// secrets, needs, if, permissions, strategy, concurrency); the timeout must live
// inside the called workflow.
func TestGenerator_ExplicitTimeoutNotOnReusableCallback(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			// Reusable-workflow callback with an explicit timeout: must NOT emit it.
			{Name: "reusable", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}, TimeoutMinutes: 15},
		},
		Deploys: []config.DeployConfig{
			// Reusable-workflow deploy callback with an explicit timeout: no emit.
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}, TimeoutMinutes: 15},
		},
	}

	result, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	reusableBuild := jobBlock(t, result, "build-reusable")
	assert.Contains(t, reusableBuild, "uses:", "sanity: reusable callback is a uses: caller")
	assert.NotContains(t, reusableBuild, "timeout-minutes:",
		"explicit timeout_minutes must not be emitted on a reusable-workflow caller job")

	reusableDeploy := jobBlock(t, result, "deploy-svc")
	assert.Contains(t, reusableDeploy, "uses:", "sanity: reusable deploy is a uses: caller")
	assert.NotContains(t, reusableDeploy, "timeout-minutes:",
		"explicit timeout_minutes must not be emitted on a reusable-workflow deploy caller job")
}

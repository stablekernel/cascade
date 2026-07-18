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

// writeCancellationWorkflows lays down the reusable-workflow files used by the
// cancellation tests and returns the temp directory the generator reads from.
func writeCancellationWorkflows(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github/workflows"), 0o755))

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      tag:
        value: test
`
	notifyWorkflow := `
name: Notify
on:
  workflow_call:
    inputs:
      app_tag:
        type: string
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github/workflows/notify.yaml"), []byte(notifyWorkflow), 0o644))
	return dir
}

// TestFinalizeRunsOnCancelledPredecessor asserts the finalize job is gated on
// always() && setup success, so it still runs (and records state) when a
// predecessor callback was cancelled mid-flight under cancel-in-progress, not
// only when one failed. A cancelled run that progressed partway must not leave
// the manifest stuck at a stale value.
func TestFinalizeRunsOnCancelledPredecessor(t *testing.T) {
	dir := writeCancellationWorkflows(t)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
			},
		},
	}

	result, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	// Finalize fires regardless of callback outcome (failure OR cancelled), as
	// long as setup itself produced state to record.
	assert.Contains(t, result, "  finalize:\n")
	assert.Contains(t, result, "if: always() && needs.setup.result == 'success'")
}

// TestFailureCheckMatchesCancelled asserts the abort-callback failure guard in
// finalize treats a cancelled predecessor the same as a failed one. Under
// cancel-in-progress a superseded run reports 'cancelled', and tolerating it
// silently would leave state un-recorded.
func TestFailureCheckMatchesCancelled(t *testing.T) {
	dir := writeCancellationWorkflows(t)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{
				Name:      "app",
				Workflow:  ".github/workflows/build.yaml",
				Triggers:  []string{"src/**"},
				OnFailure: config.OnFailureAbort,
			},
		},
	}

	result, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "Check for Failures")
	// The guard matches both failure and cancelled outcomes.
	assert.Contains(t, result, `contains(fromJSON('["failure", "cancelled"]'), needs.build-app.result)`)
	// The bare equality form must no longer appear in the failure guard.
	assert.NotContains(t, result, "needs.build-app.result == 'failure'\n        run:")
}

// TestOnFailureContinueNeverEmitsContinueOnError asserts the generator never
// emits continue-on-error, even when a callback is constructed directly with
// on_failure: continue (bypassing config validation, which rejects that value).
// GitHub Actions forbids continue-on-error on a reusable-workflow-call job and
// every callback is such a job, so emitting it would produce a workflow real
// GitHub rejects at parse. The invariant is absolute: valid YAML always.
func TestOnFailureContinueNeverEmitsContinueOnError(t *testing.T) {
	dir := writeCancellationWorkflows(t)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{
				Name:      "app",
				Workflow:  ".github/workflows/build.yaml",
				Triggers:  []string{"src/**"},
				OnFailure: config.OnFailureAbort,
			},
			{
				Name:      "notifications",
				Workflow:  ".github/workflows/notify.yaml",
				Triggers:  []string{"src/**"},
				DependsOn: []string{"app"},
				OnFailure: config.OnFailureContinue,
			},
		},
	}

	result, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	// No job carries continue-on-error, because it is invalid on a
	// reusable-workflow-call job.
	assertContinueOnErrorScopedToJob(t, result, "build-notifications", false)
	assertContinueOnErrorScopedToJob(t, result, "build-app", false)
	assert.NotContains(t, result, "continue-on-error: true")
}

// TestDefaultCallbackOmitsContinueOnError asserts the default case (no
// on_failure, which means abort) does not emit continue-on-error, keeping the
// change non-breaking for existing manifests.
func TestDefaultCallbackOmitsContinueOnError(t *testing.T) {
	dir := writeCancellationWorkflows(t)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				// OnFailure unset -> defaults to abort.
			},
		},
	}

	result, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	assertContinueOnErrorScopedToJob(t, result, "build-app", false)
	assert.NotContains(t, result, "continue-on-error: true")
}

// assertContinueOnErrorScopedToJob walks the generated YAML and asserts whether
// the named top-level job block contains a continue-on-error: true line. This
// confirms the directive lands on the right jobs.<id> rather than leaking into
// a neighbouring job.
func assertContinueOnErrorScopedToJob(t *testing.T, yaml, jobID string, want bool) {
	t.Helper()
	lines := strings.Split(yaml, "\n")
	inJob := false
	found := false
	jobHeader := "  " + jobID + ":"
	for _, line := range lines {
		// A two-space-indented "name:" or "<id>:" begins a new top-level job.
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(strings.TrimRight(line, " "), ":") {
			inJob = line == jobHeader
		}
		if inJob && strings.TrimSpace(line) == "continue-on-error: true" {
			found = true
		}
	}
	assert.Equalf(t, want, found, "continue-on-error presence for job %q", jobID)
}

package generate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerator_OrchestrateHasConcurrencyBlock asserts the generated
// orchestrate workflow declares a top-level concurrency: block. Without it,
// two rapid pushes to trunk fire concurrent runs that race on state writes
// (#92). Default group is per-ref so different branches don't block each
// other; cancel-in-progress drops the older run because a newer push
// supersedes it anyway.
func TestGenerator_OrchestrateHasConcurrencyBlock(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Must declare top-level concurrency.
	assert.Contains(t, result, "\nconcurrency:\n", "orchestrate workflow must declare top-level concurrency:")
	// Group must include github.ref so different branches don't block each
	// other.
	assert.Contains(t, result, "github.ref", "concurrency group must scope by github.ref")
	// Default behavior is to cancel an older in-progress run when a newer
	// push lands; the older run's work is obsolete.
	assert.Contains(t, result, "cancel-in-progress: true", "default concurrency cancels in-progress")
}

// bareTokenPattern matches a `token: NAME` (or `GH_TOKEN: NAME`) line whose
// value is a bare, unresolved identifier rather than a `${{ ... }}` expression.
// Such a value reaches a step as a literal string and fails authentication.
var bareTokenPattern = regexp.MustCompile(`(?m)^\s*(?:GH_)?[Tt][Oo][Kk][Ee][Nn]:\s+([A-Za-z_][A-Za-z0-9_]*)\s*$`)

// TestGenerator_ReleaseTokenBareSecretNameIsWrapped reproduces the
// cascade-example-primary manifest shape: a `release_token` set to a bare
// secret name. The generator previously emitted that name verbatim into the
// Setup CLI step (`token: CASCADE_STATE_TOKEN`), which `gh release download`
// then treated as a literal token and rejected with 401. The token input must
// be a resolvable `${{ secrets.* }}` expression.
func TestGenerator_ReleaseTokenBareSecretNameIsWrapped(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "staging", "prod"},
		ReleaseToken: "CASCADE_STATE_TOKEN", // bare secret name, as in the primary example
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// The Setup CLI token must be the wrapped, resolvable expression.
	assert.Contains(t, result, "token: ${{ secrets.CASCADE_STATE_TOKEN }}",
		"Setup CLI token must be a resolvable secrets expression")
	// And must never appear as the bare name.
	assert.NotContains(t, result, "token: CASCADE_STATE_TOKEN\n",
		"Setup CLI token must not be a bare secret name")

	// Regression guard: no emitted token: (or GH_TOKEN:) line may be a bare
	// identifier. Every token must be a ${{ ... }} expression.
	if m := bareTokenPattern.FindStringSubmatch(result); m != nil {
		t.Errorf("generated workflow emits a bare, unresolved token value %q; tokens must be ${{ ... }} expressions", m[1])
	}
}

// TestGenerator_DefaultReleaseTokenIsGitHubToken confirms the single-env shape
// (no release_token configured) still emits the GITHUB_TOKEN expression, which
// can read public releases.
func TestGenerator_DefaultReleaseTokenIsGitHubToken(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "token: ${{ secrets.GITHUB_TOKEN }}",
		"default Setup CLI token must be GITHUB_TOKEN")
	if m := bareTokenPattern.FindStringSubmatch(result); m != nil {
		t.Errorf("generated workflow emits a bare token value %q", m[1])
	}
}

// TestGenerator_OrchestrateConcurrencyOverride asserts manifest config
// can override both group and cancel-in-progress.
func TestGenerator_OrchestrateConcurrencyOverride(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Concurrency: &config.ConcurrencyConfig{
			Group:            "custom-orchestrate",
			CancelInProgress: false,
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "group: custom-orchestrate", "custom group must propagate")
	assert.Contains(t, result, "cancel-in-progress: false", "cancel_in_progress: false must propagate")
}

func TestGenerator_Generate(t *testing.T) {
	// Create temp dir with test workflow files
	tmpDir := t.TempDir()

	// Create build workflow with outputs
	buildWorkflow := `
name: Build App
on:
  workflow_call:
    inputs:
      sha:
        type: string
    outputs:
      image_tag:
        value: ${{ jobs.build.outputs.image_tag }}
`
	err := os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	// Create deploy workflow with inputs
	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      app_image_tag:
        type: string
      environment:
        type: string
`
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte(deployWorkflow), 0644)
	require.NoError(t, err)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
			},
		},
		Deploys: []config.DeployConfig{
			{
				Name:      "services",
				Workflow:  ".github/workflows/deploy.yaml",
				Triggers:  []string{"deploy/**"},
				DependsOn: []string{"app"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Verify header comment
	assert.Contains(t, result, "AUTO-GENERATED by cascade")
	assert.Contains(t, result, "DO NOT EDIT MANUALLY")

	// Verify workflow structure
	assert.Contains(t, result, "name: Orchestrate CI/CD")
	assert.Contains(t, result, "workflow_dispatch:")

	// Verify setup job
	assert.Contains(t, result, "setup:")
	assert.Contains(t, result, "run_build_app:")

	// Verify build job
	assert.Contains(t, result, "build-app:")
	assert.Contains(t, result, "needs: [setup]")
	assert.Contains(t, result, ".github/workflows/build.yaml")

	// Verify deploy job
	assert.Contains(t, result, "deploy-services:")
	assert.Contains(t, result, "needs: [setup, build-app]")
	assert.Contains(t, result, "app_image_tag:")

	// Verify finalize job
	assert.Contains(t, result, "finalize:")
}

// TestGenerator_CallbackTimeoutMinutes asserts that a per-callback
// timeout_minutes (#97) is NOT emitted as a job-level timeout-minutes on a
// reusable-workflow (uses:) callback. GitHub forbids timeout-minutes on a job
// that calls a reusable workflow, so the timeout must live inside the called
// workflow. Callbacks are reusable-workflow only, so the field is never emitted
// on the caller job for validate, build, or deploy.
func TestGenerator_CallbackTimeoutMinutes(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "validate.yaml")
	writeStubWorkflow(t, tmpDir, "build.yaml")
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Validate: &config.ValidateConfig{
			Workflow:       ".github/workflows/validate.yaml",
			TimeoutMinutes: 5,
		},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}, TimeoutMinutes: 30},
		},
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}, TimeoutMinutes: 15},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	requireJobLacksTimeout := func(t *testing.T, content, jobID string) {
		t.Helper()
		block := jobBlock(t, content, jobID)
		require.NotEmpty(t, block, "job %q not found", jobID)
		assert.Contains(t, block, "uses:", "job %q must be a reusable-workflow caller", jobID)
		assert.NotContains(t, block, "timeout-minutes:",
			"timeout-minutes must not be emitted on reusable-workflow callback %q", jobID)
	}

	requireJobLacksTimeout(t, result, "validate")
	requireJobLacksTimeout(t, result, "build-app")
	requireJobLacksTimeout(t, result, "deploy-svc")
}

// TestGenerator_CallbackTimeoutOmittedWhenZero asserts no timeout-minutes is
// emitted on a reusable-workflow callback (jobs.<id>.uses) when its
// timeout_minutes is unset; those callers own their own timeout. Cascade-owned
// jobs (setup/finalize) still receive the owned-job default (#37), so the check
// is scoped to the callback job block, not the whole workflow.
func TestGenerator_CallbackTimeoutOmittedWhenZero(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "build-app")
	assert.NotContains(t, block, "timeout-minutes:", "no timeout-minutes on reusable-workflow callback when unset")
}

func TestGenerator_GenerateWithRetries(t *testing.T) {
	tmpDir := t.TempDir()

	// Create workflow file
	buildWorkflow := `
name: Build App
on:
  workflow_call:
    outputs:
      image_tag:
        value: ${{ jobs.build.outputs.image_tag }}
`
	err := os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				Retries:  2,
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Verify retry jobs are generated
	assert.Contains(t, result, "build-app-retry-1:")
	assert.Contains(t, result, "build-app-retry-2:")
	assert.Contains(t, result, "Retry 1")
	assert.Contains(t, result, "Retry 2")
	assert.Contains(t, result, "needs.build-app.result == 'failure'")
	assert.Contains(t, result, "needs.build-app-retry-1.result == 'failure'")
}

func TestGenerator_GenerateWithRunPolicies(t *testing.T) {
	tmpDir := t.TempDir()

	// Create workflow files
	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      tag:
        value: test
`
	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      app_tag:
        type: string
`
	err := os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte(deployWorkflow), 0644)
	require.NoError(t, err)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
			},
		},
		Deploys: []config.DeployConfig{
			{
				Name:      "services",
				Workflow:  ".github/workflows/deploy.yaml",
				Triggers:  []string{"deploy/**"},
				DependsOn: []string{"app"},
				RunPolicy: config.RunPolicyAlways,
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Verify always() is added for always policy
	assert.Contains(t, result, "always()")
	// Verify skipped condition is included
	assert.Contains(t, result, "skipped")
}

func TestGenerator_GenerateWithOnFailureContinue(t *testing.T) {
	tmpDir := t.TempDir()

	// Create workflow files
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
	err := os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/notify.yaml"), []byte(notifyWorkflow), 0644)
	require.NoError(t, err)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
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
				OnFailure: config.OnFailureContinue, // This should NOT fail the workflow
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Verify failure check only includes abort callbacks. The condition now
	// matches both failure and cancelled outcomes.
	assert.Contains(t, result, "Check for Failures")
	assert.Contains(t, result, `contains(fromJSON('["failure", "cancelled"]'), needs.build-app.result)`)
	// Should NOT include notifications in failure check (it still appears in the
	// summary table and finalize needs:, so scope to the guard's condition form).
	assert.NotContains(t, result, `needs.build-notifications.result)`)
}

func TestGenerator_GenerateWithAllContinue(t *testing.T) {
	tmpDir := t.TempDir()

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      tag:
        value: test
`
	err := os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:      "app",
				Workflow:  ".github/workflows/build.yaml",
				Triggers:  []string{"src/**"},
				OnFailure: config.OnFailureContinue,
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// When all callbacks have on_failure: continue, there should be no failure check step
	assert.NotContains(t, result, "Check for Failures")
}

func TestGenerator_GenerateManifestUpdate(t *testing.T) {
	tmpDir := t.TempDir()

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    outputs:
      result:
        value: success
`
	err := os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte(deployWorkflow), 0644)
	require.NoError(t, err)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
			},
		},
		Deploys: []config.DeployConfig{
			{
				Name:      "app-deploy",
				Workflow:  ".github/workflows/deploy.yaml",
				DependsOn: []string{"app"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Verify state update step exists with environment-level updates
	assert.Contains(t, result, "Update Manifest")
	assert.Contains(t, result, "HEAD_SHA:")
	assert.Contains(t, result, "ENVIRONMENT:")
	assert.Contains(t, result, "MANIFEST_FILE=")

	// Verify per-deploy tracking
	assert.Contains(t, result, "APP_DEPLOY_RESULT:")
	assert.Contains(t, result, ".$MANIFEST_KEY.state.$ENVIRONMENT.sha")
	assert.Contains(t, result, ".$MANIFEST_KEY.state.$ENVIRONMENT.committed_at")
}

// TestGenerator_ManifestUpdate_PushRetriesOnConflict verifies the generated
// finalize step retries with rebase when concurrent runs race to push state.
//
// Why: when two orchestrate runs land back-to-back, the second `git push` is
// rejected as non-fast-forward. The fix is a fetch+reset+re-apply+push loop so
// the slower run rebuilds its state commit on the latest tip before retrying.
func TestGenerator_ManifestUpdate_PushRetriesOnConflict(t *testing.T) {
	tmpDir := t.TempDir()

	buildWorkflow := `
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	deployWorkflow := `
on:
  workflow_call:
`
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte(deployWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app-deploy", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Extract the Update Manifest step body for focused assertions.
	updateIdx := strings.Index(result, "- name: Update Manifest")
	require.GreaterOrEqual(t, updateIdx, 0, "Update Manifest step missing")
	step := result[updateIdx:]
	if next := strings.Index(step[1:], "- name:"); next > 0 {
		step = step[:next+1]
	}

	// Must rebase before push (else stale checkout races the remote tip).
	assert.Contains(t, step, "git fetch", "Update Manifest must fetch before push")
	// Must retry on rejection (single push is the bug we're fixing).
	assert.Regexp(t, `(?i)attempt|retry|for `, step, "Update Manifest must retry on push rejection")
	// Re-apply yq edits inside the loop, otherwise rebase conflicts surface.
	assert.Contains(t, step, "git reset --hard", "retry loop must reset to latest tip before re-applying state")
	// Final attempt should still fail loudly so CI catches a real problem.
	assert.Regexp(t, `(?s)exit\s+1`, step, "loop must exit non-zero after exhausting retries")
}

// TestGenerator_ManifestUpdate_NoEnvPushRetriesOnConflict mirrors the above for
// no-environment (library/CLI) projects which write to state.prerelease.
func TestGenerator_ManifestUpdate_NoEnvPushRetriesOnConflict(t *testing.T) {
	tmpDir := t.TempDir()

	buildWorkflow := `
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "master",
		Builds: []config.BuildConfig{
			{Name: "cli", Workflow: ".github/workflows/build.yaml", Triggers: []string{"cmd/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	updateIdx := strings.Index(result, "- name: Update Manifest")
	require.GreaterOrEqual(t, updateIdx, 0, "Update Manifest step missing")
	step := result[updateIdx:]
	if next := strings.Index(step[1:], "- name:"); next > 0 {
		step = step[:next+1]
	}

	assert.Contains(t, step, "git fetch")
	assert.Contains(t, step, "git reset --hard")
	assert.Regexp(t, `(?i)attempt|retry|for `, step)
	assert.Contains(t, step, "state.prerelease.sha")
}

func TestGenerator_GenerateSummaryWithOutputs(t *testing.T) {
	tmpDir := t.TempDir()

	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
      digest:
        value: sha256:abc
`
	err := os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Verify summary includes outputs section
	assert.Contains(t, result, "### Outputs")
	assert.Contains(t, result, "| Output | Value |")
	assert.Contains(t, result, "app_image_tag")
	assert.Contains(t, result, "app_digest")
}

func TestGenerator_ValidateMissingInputs(t *testing.T) {
	tmpDir := t.TempDir()

	// Build workflow with output
	buildWorkflow := `
name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	err := os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644)
	require.NoError(t, err)

	// Deploy workflow WITHOUT the expected input
	deployWorkflow := `
name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        type: string
`
	err = os.WriteFile(filepath.Join(tmpDir, ".github/workflows/deploy.yaml"), []byte(deployWorkflow), 0644)
	require.NoError(t, err)

	cfg := &config.TrunkConfig{
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "services", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	warnings := gen.Validate()

	assert.NotEmpty(t, warnings)
	// Callbacks define their own interface - we check for original output name, not prefixed
	assert.Contains(t, warnings[0], "image_tag")
}

func TestGenerator_FinalizeJob_ReleaseDisabled(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Release:      &config.ReleaseConfig{Disabled: boolPtr(true)},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	// Create temp dir with mock workflow
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	err := os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(`
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`), 0644)
	require.NoError(t, err)

	gen := NewGenerator(cfg, tmpDir)
	output, err := gen.Generate()
	require.NoError(t, err)

	// Should NOT contain changelog or release steps
	assert.NotContains(t, output, "generate-changelog", "Output should not contain generate-changelog when release disabled")
	assert.NotContains(t, output, "manage-release", "Output should not contain manage-release when release disabled")
	// Should still contain manifest update
	assert.Contains(t, output, "Update Manifest", "Output should still contain manifest update step")
}

func TestGenerator_FinalizeJob_ExternalRelease(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Release:      &config.ReleaseConfig{Tag: "goreleaser.tag"},
		Builds: []config.BuildConfig{
			{Name: "goreleaser", Workflow: ".github/workflows/goreleaser.yaml", Triggers: []string{"cmd/**"}},
		},
	}

	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	err := os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(workflowDir, "goreleaser.yaml"), []byte(`
on:
  workflow_call:
    outputs:
      tag:
        value: test
`), 0644)
	require.NoError(t, err)

	gen := NewGenerator(cfg, tmpDir)
	output, err := gen.Generate()
	require.NoError(t, err)

	// Should contain external release reference
	assert.Contains(t, output, "goreleaser", "Output should reference goreleaser callback")
	assert.Contains(t, output, "tag:", "Output should contain tag reference")
	// Verify action is "update" for external release
	assert.Contains(t, output, "action: update", "External release should use action: update")
	// Verify it references the goreleaser output
	assert.Contains(t, output, "needs.build-goreleaser.outputs.tag", "Should reference goreleaser output for tag")
}

func TestGenerator_FinalizeJob_CustomChangelog(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Changelog:    &config.ChangelogConfig{Workflow: ".github/workflows/custom-changelog.yaml"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	err := os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(`
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(workflowDir, "custom-changelog.yaml"), []byte(`
on:
  workflow_call:
    inputs:
      base_sha:
        type: string
      head_sha:
        type: string
    outputs:
      changelog:
        value: test
`), 0644)
	require.NoError(t, err)

	gen := NewGenerator(cfg, tmpDir)
	output, err := gen.Generate()
	require.NoError(t, err)

	// A reusable workflow cannot be invoked as a step `uses:`; it must be a
	// job-level `uses:`. The custom changelog is therefore hoisted into its own
	// `changelog` job that calls the reusable workflow.
	assert.Contains(t, output, "custom-changelog.yaml", "Output should reference custom changelog workflow")

	// The custom changelog must NOT be emitted as a step inside finalize.
	assert.NotContains(t, output, "Generate Changelog (Custom)",
		"Custom changelog must be a job, not a step (a reusable workflow cannot be a step uses:)")

	// It must be a job-level `uses:` with a normalized (./-prefixed) path so
	// actionlint resolves it as a reusable-workflow call.
	assert.Contains(t, output, "  changelog:\n", "Should emit a dedicated changelog job")
	assert.Contains(t, output, "uses: ./.github/workflows/custom-changelog.yaml",
		"Changelog job should call the reusable workflow via a normalized job-level uses:")

	// The changelog job depends on setup and passes the base SHA from the
	// output the setup job actually declares: changelog_base_sha (not base_sha).
	assert.Contains(t, output, "changelog_base_sha: ${{ needs.setup.outputs.changelog_base_sha }}",
		"Changelog job should pass changelog_base_sha keyed to the real setup output")
	assert.Contains(t, output, "head_sha: ${{ needs.setup.outputs.head_sha }}", "Should pass head_sha input parameter")
	assert.NotContains(t, output, "needs.setup.outputs.base_sha",
		"Setup job does not declare a base_sha output; that reference would be empty at runtime")

	// The release step must read the changelog from the job output, not a step.
	assert.Contains(t, output, "changelog: ${{ needs.changelog.outputs.changelog }}",
		"Release step should consume the changelog from the changelog job output")
	assert.NotContains(t, output, "changelog: ${{ steps.changelog.outputs.changelog }}",
		"Release step must not read a step output for the custom changelog case")
}

func TestGenerator_FinalizeJob_FrameworkManagedRelease(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Release:      &config.ReleaseConfig{}, // No tag specified = framework-managed
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	err := os.MkdirAll(workflowDir, 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(`
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`), 0644)
	require.NoError(t, err)

	gen := NewGenerator(cfg, tmpDir)
	output, err := gen.Generate()
	require.NoError(t, err)

	// Verify framework-managed release (default scenario) with version-based tagging
	assert.Contains(t, output, "action: update", "Framework-managed release should use action: update (handles create or update)")
	assert.Contains(t, output, "tag: ${{ needs.setup.outputs.version }}", "Framework-managed release should use version tag")
	assert.Contains(t, output, "create_tag: 'true'", "Framework-managed release should create git tag")
	assert.Contains(t, output, "previous_tag: ${{ needs.setup.outputs.previous_tag }}", "Should pass previous_tag for changelog comparison")
}

func TestGenerator_DeployChangeDetection(t *testing.T) {
	// Setup temp directory with mock workflows
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	// Create mock workflows
	mockWorkflow := `name: Mock
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(mockWorkflow), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app-deploy", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}},
			{Name: "cdk", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{".aws/cdk/**"}},
			{Name: "notify", Workflow: ".github/workflows/deploy.yaml"}, // No constraints
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// CLI-based setup: single command handles all detection
	assert.Contains(t, content, "cascade orchestrate setup")
	assert.Contains(t, content, "--gha-output")

	// Outputs reference CLI outputs. Output keys are normalized to underscores
	// (GitHub Actions parses hyphens in expressions as subtraction).
	assert.Contains(t, content, "run_deploy_app_deploy: ${{ steps.setup.outputs.run_deploy_app_deploy }}")
	assert.Contains(t, content, "run_deploy_cdk: ${{ steps.setup.outputs.run_deploy_cdk }}")
	assert.Contains(t, content, "run_deploy_notify: ${{ steps.setup.outputs.run_deploy_notify }}")
}

// TestGenerator_HyphenatedNameOutputKeysConsistent guards against the silent
// skip described in #127: a hyphenated build/deploy name must produce the same
// underscore-normalized run_build_*/run_deploy_* identifier at every site (the
// setup job-level outputs passthrough and the consuming job if: condition). If
// any site emits a hyphen, GitHub Actions parses the expression as subtraction
// and the consuming job's if: never matches, silently skipping the work.
func TestGenerator_HyphenatedNameOutputKeysConsistent(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
    outputs:
      result:
        value: test
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(mockWorkflow), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "shared-lib", Workflow: ".github/workflows/build.yaml", Triggers: []string{"libs/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "web-api", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"api/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// (a) The job-level outputs passthrough uses the underscore identifier on
	// BOTH the output key and the steps.setup.outputs.* reference.
	assert.Contains(t, content, "run_build_shared_lib: ${{ steps.setup.outputs.run_build_shared_lib }}",
		"build passthrough must use underscore output key and reference")
	assert.Contains(t, content, "run_deploy_web_api: ${{ steps.setup.outputs.run_deploy_web_api }}",
		"deploy passthrough must use underscore output key and reference")

	// (b) No hyphenated run_* output identifier appears anywhere. A hyphen in a
	// GHA expression is subtraction, so these would silently break.
	assert.NotContains(t, content, "run_build_shared-lib",
		"hyphenated build output identifier breaks GHA expression parsing")
	assert.NotContains(t, content, "run_deploy_web-api",
		"hyphenated deploy output identifier breaks GHA expression parsing")

	// (c) The consuming job if: condition references the SAME underscore key the
	// passthrough exposes (not an always-false hyphenated reference).
	assert.Contains(t, content, "needs.setup.outputs.run_build_shared_lib == 'true'",
		"build if: must reference the underscore-normalized key")
	assert.Contains(t, content, "needs.setup.outputs.run_deploy_web_api == 'true'",
		"deploy if: must reference the underscore-normalized key")
}

func TestGenerator_BuildLinkedDeployCondition(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(mockWorkflow), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app-deploy", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Build-linked deploy should depend on build job success, not setup detection
	assert.Contains(t, content, "needs.build-app.result == 'success'")
	// It should NOT check setup.outputs for build-linked deploys
	assert.NotContains(t, content, "needs.setup.outputs.run_app-deploy == 'true'")
}

func TestGenerator_PerDeployManifestUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
    outputs:
      result:
        value: test
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{Name: "cdk", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{".aws/cdk/**"}},
			{Name: "k8s", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{".k8s/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should update per-deploy SHAs in state section
	assert.Contains(t, content, ".state.$ENVIRONMENT.deploys.cdk.sha")
	assert.Contains(t, content, ".state.$ENVIRONMENT.deploys.k8s.sha")

	// Should have environment variables for deploy results
	assert.Contains(t, content, "CDK_RESULT:")
	assert.Contains(t, content, "K8S_RESULT:")

	// Should check deploy result before updating
	assert.Contains(t, content, "if [[ \"$CDK_RESULT\" == \"success\" ]]; then")
	assert.Contains(t, content, "if [[ \"$K8S_RESULT\" == \"success\" ]]; then")

	// Should update deployed_at/by for each deploy
	assert.Contains(t, content, ".state.$ENVIRONMENT.deploys.cdk.deployed_at")
	assert.Contains(t, content, ".state.$ENVIRONMENT.deploys.k8s.deployed_at")
	assert.Contains(t, content, ".state.$ENVIRONMENT.deploys.cdk.deployed_by")
}

// =============================================================================
// Per-Deployable Tracking Edge Case Tests for Main Workflow
// =============================================================================

func TestGenerator_MixedDeployTypesInSetup(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
    outputs:
      result:
        value: test
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(mockWorkflow), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app-deploy", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}}, // build-linked
			{Name: "cdk", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{".aws/cdk/**"}}, // trigger-based
			{Name: "notify", Workflow: ".github/workflows/deploy.yaml"},                                 // unconstrained
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// CLI-based setup: single command handles all detection
	assert.Contains(t, content, "cascade orchestrate setup")
	assert.Contains(t, content, "--gha-output")

	// Outputs reference CLI outputs. Output keys are normalized to underscores.
	assert.Contains(t, content, "run_deploy_app_deploy: ${{ steps.setup.outputs.run_deploy_app_deploy }}")
	assert.Contains(t, content, "run_deploy_cdk: ${{ steps.setup.outputs.run_deploy_cdk }}")
	assert.Contains(t, content, "run_deploy_notify: ${{ steps.setup.outputs.run_deploy_notify }}")

	// All should have deploy jobs
	assert.Contains(t, content, "deploy-app-deploy:")
	assert.Contains(t, content, "deploy-cdk:")
	assert.Contains(t, content, "deploy-notify:")
}

func TestGenerator_EmptyDeploysGeneratesValidWorkflow(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
    outputs:
      image_tag:
        value: test
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{}, // No deploys
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should still have basic structure
	assert.Contains(t, content, "name: Orchestrate CI/CD")
	assert.Contains(t, content, "setup:")
	assert.Contains(t, content, "build-app:")
	assert.Contains(t, content, "finalize:")

	// Should NOT have any deploy jobs
	assert.NotContains(t, content, "deploy-")
}

func TestGenerator_BuildLinkedDeployNeedsArray(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
    outputs:
      result:
        value: test
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(mockWorkflow), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app-deploy", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Build-linked deploy should need setup AND the build job
	assert.Contains(t, content, "needs: [setup, build-app]")
}

func TestGenerator_TriggerBasedDeployNeedsOnlySetup(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
    outputs:
      result:
        value: test
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{Name: "cdk", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{".aws/cdk/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Trigger-based deploy should only need setup
	assert.Contains(t, content, "deploy-cdk:")
	// Find the deploy-cdk job and check its needs
	lines := strings.Split(content, "\n")
	inDeployCdk := false
	for _, line := range lines {
		if strings.Contains(line, "deploy-cdk:") {
			inDeployCdk = true
		}
		if inDeployCdk && strings.Contains(line, "needs:") {
			assert.Contains(t, line, "needs: [setup]")
			break
		}
		if inDeployCdk && strings.HasPrefix(strings.TrimSpace(line), "if:") {
			break // Stop before condition
		}
	}
}

func TestGenerator_UnconstrainedDeployAlwaysRuns(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{Name: "notify", Workflow: ".github/workflows/deploy.yaml"}, // No triggers or depends_on
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// CLI-based setup handles detection
	assert.Contains(t, content, "cascade orchestrate setup")
	assert.Contains(t, content, "--gha-output")

	// Output declaration for unconstrained deploy
	assert.Contains(t, content, "run_deploy_notify: ${{ steps.setup.outputs.run_deploy_notify }}")
}

func TestGenerator_DeployNameEnvVarConversion(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{Name: "my-app-deploy", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Environment variable should have hyphens converted to underscores
	assert.Contains(t, content, "MY_APP_DEPLOY_RESULT")
}

func TestGenerator_FinalizeNeedsAllDeployJobs(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(mockWorkflow), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "cdk", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{".aws/cdk/**"}},
			{Name: "k8s", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{".k8s/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Finalize should need all build and deploy jobs (order may vary based on topology)
	assert.Contains(t, content, "deploy-cdk")
	assert.Contains(t, content, "deploy-k8s")
	assert.Contains(t, content, "build-app")
	// Check that finalize needs includes all jobs
	assert.Regexp(t, `finalize:.*needs: \[setup, .*deploy-cdk.*\]`, strings.ReplaceAll(content, "\n", " "))
}

func TestGenerator_PerDeployManifestUpdateSkipsOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should only update state on success
	assert.Contains(t, content, `if [[ "$APP_RESULT" == "success" ]]; then`)
	assert.Contains(t, content, ".state.$ENVIRONMENT.deploys.app.sha")
}

func TestGenerator_MultipleTriggerPatterns(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{Name: "infra", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"terraform/**", "cdk/**", "modules/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// CLI-based setup: single command handles all detection
	assert.Contains(t, content, "cascade orchestrate setup")
	assert.Contains(t, content, "--gha-output")

	// Output declaration for deploy
	assert.Contains(t, content, "run_deploy_infra: ${{ steps.setup.outputs.run_deploy_infra }}")
}

func TestGenerator_SingleDeployNoArrayBrackets(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Deploys: []config.DeployConfig{
			{Name: "only-one", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should still have proper YAML structure
	assert.Contains(t, content, "deploy-only-one:")
	assert.Contains(t, content, "ONLY_ONE_RESULT")
}

func TestGenerator_DeployWithBuildOutputs(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	buildWorkflow := `name: Build
on:
  workflow_call:
    outputs:
      image_tag:
        value: test-tag
      version:
        value: 1.0.0
`
	deployWorkflow := `name: Deploy
on:
  workflow_call:
    inputs:
      image_tag:
        type: string
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(buildWorkflow), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(deployWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app-deploy", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Build-linked deploy should pass build outputs
	assert.Contains(t, content, "app_image_tag: ${{ needs.build-app.outputs.image_tag }}")
}

func TestGenerator_BuildLinkedDeployDoesNotCheckSetupOutput(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	mockWorkflow := `name: Mock
on:
  workflow_call:
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(mockWorkflow), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "deploy.yaml"), []byte(mockWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app-deploy", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Build-linked deploy should NOT use setup output for condition
	assert.NotContains(t, content, "needs.setup.outputs.run_app-deploy")
	// It should use build result instead
	assert.Contains(t, content, "needs.build-app.result == 'success'")
}

// =============================================================================
// Publish callback (#39): artifact_id tracking in orchestrate finalize
// =============================================================================

func TestGenerator_BuildArtifactIDTracked(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	// Build workflow that exposes artifact_id output
	buildWorkflow := `name: Build
on:
  workflow_call:
    outputs:
      artifact_id:
        description: 'Immutable artifact identifier (e.g., Docker image digest)'
        value: ${{ jobs.build.outputs.artifact_id }}
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(buildWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Finalize should capture artifact_id from the build job output
	assert.Contains(t, content, "BUILD_ARTIFACT_APP: ${{ needs.build-app.outputs.artifact_id }}")

	// Finalize should write artifact_id into per-build state
	assert.Contains(t, content, ".state.$ENVIRONMENT.builds.app.artifact_id")
	assert.Contains(t, content, "$BUILD_ARTIFACT_APP")
}

func TestGenerator_BuildArtifactIDSkippedWhenNotDeclared(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	// Build workflow without artifact_id output
	buildWorkflow := `name: Build
on:
  workflow_call:
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "build.yaml"), []byte(buildWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// No artifact_id references when the workflow doesn't declare it
	assert.NotContains(t, content, "BUILD_ARTIFACT_APP")
	assert.NotContains(t, content, "builds.app.artifact_id")
}

// TestGenerator_ExtraTriggers_None verifies that omitting extra_triggers
// produces only push and workflow_dispatch in the on: block.
func TestGenerator_ExtraTriggers_None(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Builds:      []config.BuildConfig{{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}}},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "  push:\n", "push trigger must be present")
	assert.Contains(t, result, "  workflow_dispatch:\n", "workflow_dispatch trigger must be present")
	assert.NotContains(t, result, "  schedule:\n", "schedule must not appear when not configured")
	assert.NotContains(t, result, "  repository_dispatch:\n", "repository_dispatch must not appear when not configured")
	assert.NotContains(t, result, "  workflow_run:\n", "workflow_run must not appear when not configured")
	assert.NotContains(t, result, "  merge_group:\n", "merge_group must not appear when not configured")
}

// TestGenerator_ExtraTriggers_Schedule verifies that schedule entries are
// emitted as cron expressions under on: schedule:.
func TestGenerator_ExtraTriggers_Schedule(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Builds:      []config.BuildConfig{{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}}},
		ExtraTriggers: &config.ExtraTriggers{
			Schedule: []config.ScheduleEntry{
				{Cron: "0 6 * * 1"},
				{Cron: "0 12 * * 5"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "  schedule:\n", "schedule block must be present")
	assert.Contains(t, result, "    - cron: '0 6 * * 1'\n", "first cron entry must appear")
	assert.Contains(t, result, "    - cron: '0 12 * * 5'\n", "second cron entry must appear")
}

// TestGenerator_ExtraTriggers_RepositoryDispatch verifies repository_dispatch
// is emitted with its configured event types.
func TestGenerator_ExtraTriggers_RepositoryDispatch(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Builds:      []config.BuildConfig{{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}}},
		ExtraTriggers: &config.ExtraTriggers{
			RepositoryDispatch: &config.RepositoryDispatchTrigger{
				Types: []string{"external-update", "redeploy"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "  repository_dispatch:\n", "repository_dispatch block must be present")
	assert.Contains(t, result, "      - external-update\n", "first dispatch type must appear")
	assert.Contains(t, result, "      - redeploy\n", "second dispatch type must appear")
}

// TestGenerator_ExtraTriggers_RepositoryDispatch_NoTypes verifies that
// repository_dispatch emits correctly even when no event types are specified.
func TestGenerator_ExtraTriggers_RepositoryDispatch_NoTypes(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Builds:      []config.BuildConfig{{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}}},
		ExtraTriggers: &config.ExtraTriggers{
			RepositoryDispatch: &config.RepositoryDispatchTrigger{},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "  repository_dispatch:\n", "repository_dispatch block must appear when pointer is non-nil")
}

// TestGenerator_ExtraTriggers_WorkflowRun verifies workflow_run is emitted
// with its workflows and event types.
func TestGenerator_ExtraTriggers_WorkflowRun(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Builds:      []config.BuildConfig{{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}}},
		ExtraTriggers: &config.ExtraTriggers{
			WorkflowRun: &config.WorkflowRunTrigger{
				Workflows: []string{"Upstream CI"},
				Types:     []string{"completed"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "  workflow_run:\n", "workflow_run block must be present")
	assert.Contains(t, result, "      - 'Upstream CI'\n", "workflow name must appear")
	assert.Contains(t, result, "      - completed\n", "event type must appear")
}

// TestGenerator_ExtraTriggers_MergeGroup verifies that a non-nil MergeGroupTrigger
// emits the bare merge_group: entry (presence is sufficient; no sub-keys needed).
func TestGenerator_ExtraTriggers_MergeGroup(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Builds:      []config.BuildConfig{{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}}},
		ExtraTriggers: &config.ExtraTriggers{
			MergeGroup: &config.MergeGroupTrigger{},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "  merge_group:\n", "merge_group trigger must be emitted when MergeGroup is non-nil")
	// Lane behavior is a separate issue; no merge_queue: config involved here.
	assert.NotContains(t, result, "merge_queue:", "lane behavior config must not appear from trigger emission alone")
}

// TestGenerator_ExtraTriggers_AllCombined verifies all four extra trigger
// types can be emitted together in one config.
func TestGenerator_ExtraTriggers_AllCombined(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Builds:      []config.BuildConfig{{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}}},
		ExtraTriggers: &config.ExtraTriggers{
			Schedule: []config.ScheduleEntry{
				{Cron: "0 2 * * *"},
			},
			RepositoryDispatch: &config.RepositoryDispatchTrigger{
				Types: []string{"deploy-trigger"},
			},
			WorkflowRun: &config.WorkflowRunTrigger{
				Workflows: []string{"Build"},
				Types:     []string{"completed"},
			},
			MergeGroup: &config.MergeGroupTrigger{},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "  schedule:\n")
	assert.Contains(t, result, "  repository_dispatch:\n")
	assert.Contains(t, result, "  workflow_run:\n")
	assert.Contains(t, result, "  merge_group:\n")
	// Baseline triggers must still be present.
	assert.Contains(t, result, "  push:\n")
	assert.Contains(t, result, "  workflow_dispatch:\n")
}

// TestGenerator_BuildMatrix_TwoDimensions asserts that a build callback with a
// two-dimension matrix emits strategy.matrix with both axes.
func TestGenerator_BuildMatrix_TwoDimensions(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	// Workflow declares os and arch inputs so they are passed through.
	buildWorkflow := `on:
  workflow_call:
    inputs:
      os:
        type: string
      arch:
        type: string
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644))

	ptrFalse := false
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				Matrix: &config.MatrixConfig{
					Dimensions:  map[string][]string{"arch": {"amd64", "arm64"}, "os": {"linux", "darwin"}},
					MaxParallel: 2,
					FailFast:    &ptrFalse,
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "    strategy:\n", "build job must have strategy block")
	assert.Contains(t, result, "      matrix:\n", "strategy must contain matrix key")
	// Both dimensions must appear.
	assert.Contains(t, result, `        arch: ["amd64", "arm64"]`, "arch dimension must be emitted")
	assert.Contains(t, result, `        os: ["linux", "darwin"]`, "os dimension must be emitted")
	assert.Contains(t, result, "      max-parallel: 2", "max-parallel must be emitted when set")
	assert.Contains(t, result, "      fail-fast: false", "fail-fast must be emitted when explicitly false")
	// Matrix values must be forwarded to the reusable workflow via with:.
	assert.Contains(t, result, "      os: ${{ matrix.os }}", "os dimension value must be passed in with:")
	assert.Contains(t, result, "      arch: ${{ matrix.arch }}", "arch dimension value must be passed in with:")
}

// TestGenerator_BuildMatrix_MaxParallelOmittedWhenZero asserts that max-parallel
// is not emitted when unset (zero value).
func TestGenerator_BuildMatrix_MaxParallelOmittedWhenZero(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				Matrix: &config.MatrixConfig{
					Dimensions: map[string][]string{"os": {"linux"}},
					// MaxParallel zero and FailFast nil: neither should appear.
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "    strategy:\n", "strategy block must be present")
	assert.NotContains(t, result, "max-parallel", "max-parallel must be absent when zero")
	assert.NotContains(t, result, "fail-fast", "fail-fast must be absent when nil")
}

// TestGenerator_BuildMatrix_NoMatrixNoStrategy asserts that a build without
// matrix: produces no strategy block (non-breaking for existing single builds).
func TestGenerator_BuildMatrix_NoMatrixNoStrategy(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.NotContains(t, result, "strategy:", "no matrix → no strategy block")
}

// TestGenerator_PassthroughArtifact_NoArtifactNoSteps asserts that a build
// without artifact: produces neither upload-artifact nor download-artifact steps
// (non-breaking for existing configs).
func TestGenerator_PassthroughArtifact_NoArtifactNoSteps(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// The finalize job uses download-artifact for release assets; ensure we do
	// not accidentally count that. Check that neither passthrough action appears
	// outside the finalize/release context by verifying they are absent entirely
	// (no release config is set, so the finalize job emits no artifact steps either).
	assert.NotContains(t, result, "actions/upload-artifact@v7",
		"build without artifact: must not emit upload-artifact")
	// download-artifact only appears in finalize when HasReleaseArtifacts. It is
	// absent here because no release artifacts are declared and no passthrough is set.
	assert.NotContains(t, result, "actions/download-artifact@v8",
		"build without artifact: must not emit download-artifact")
}

// TestGenerator_PassthroughArtifact_ReusableWorkflowUploadJob asserts that a
// reusable-workflow build declaring artifact.upload gets a cascade-owned
// post-job ({job-id}-upload) that runs upload-artifact after the callback.
func TestGenerator_PassthroughArtifact_ReusableWorkflowUploadJob(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "compile",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				PassthroughArtifact: &config.PassthroughArtifact{
					Upload: "dist/",
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// A post-upload job must be emitted.
	assert.Contains(t, result, "build-compile-upload:",
		"reusable-workflow build with artifact.upload must emit a post-upload job")
	assert.Contains(t, result, "uses: actions/upload-artifact@v7",
		"post-upload job must use upload-artifact action")
	assert.Contains(t, result, "name: build-compile",
		"uploaded artifact must be named build-{build-name}")
	// The post-upload job must depend on the callback job.
	assert.Contains(t, result, "needs: [build-compile]",
		"post-upload job must declare needs: [build-compile]")
}

// TestGenerator_PassthroughArtifact_ReusableWorkflowDownloadJob asserts that a
// reusable-workflow build declaring artifact.downloads gets a cascade-owned
// pre-job ({job-id}-download) that fetches the artifacts, and the callback job
// depends on it.
func TestGenerator_PassthroughArtifact_ReusableWorkflowDownloadJob(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/compile.yaml"), []byte("on:\n  workflow_call:\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/sign.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "compile",
				Workflow: ".github/workflows/compile.yaml",
				Triggers: []string{"src/**"},
				PassthroughArtifact: &config.PassthroughArtifact{
					Upload: "dist/",
				},
			},
			{
				Name:      "sign",
				Workflow:  ".github/workflows/sign.yaml",
				DependsOn: []string{"compile"},
				Triggers:  []string{"src/**"},
				PassthroughArtifact: &config.PassthroughArtifact{
					Downloads: []string{"compile"},
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// A pre-download job must be emitted for the sign callback.
	assert.Contains(t, result, "build-sign-download:",
		"reusable-workflow build with artifact.downloads must emit a pre-download job")
	assert.Contains(t, result, "uses: actions/download-artifact@v8",
		"pre-download job must use download-artifact action")
	assert.Contains(t, result, "name: build-compile",
		"pre-download step must reference the producer's artifact name build-compile")
	// The sign callback job must depend on the pre-download job.
	assert.Contains(t, result, "build-sign-download",
		"sign callback job needs must include the pre-download job")

	// The compile callback must also emit its post-upload job.
	assert.Contains(t, result, "build-compile-upload:",
		"compile build with artifact.upload must emit its post-upload job")
}

// downloadJobNeeds extracts the comma-separated job IDs from the "needs: [...]"
// line of the named job block in a generated workflow. It returns the raw
// contents between the brackets (whitespace trimmed per entry). The job block is
// identified by its "  <jobID>:\n" header at two-space indentation.
func downloadJobNeeds(t *testing.T, workflow, jobID string) []string {
	t.Helper()
	header := "\n  " + jobID + ":\n"
	idx := strings.Index(workflow, header)
	require.NotEqual(t, -1, idx, "job %q not found in generated workflow", jobID)
	rest := workflow[idx+len(header):]
	// The needs line is emitted immediately after name: for these owned jobs.
	const marker = "needs: ["
	nidx := strings.Index(rest, marker)
	require.NotEqual(t, -1, nidx, "job %q has no needs: line", jobID)
	rest = rest[nidx+len(marker):]
	end := strings.Index(rest, "]")
	require.NotEqual(t, -1, end, "job %q needs: line is malformed", jobID)
	raw := rest[:end]
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// TestGenerator_PassthroughArtifact_DownloadNeedsUploadJob asserts that the
// cascade-owned download pre-job depends on the producer's -upload post-job for
// every artifact it consumes. Without this happens-after edge the download job
// races the upload and fails at runtime with "Artifact not found", which is what
// a live matrix-build orchestrate run exposed (the bundle build's download ran
// before the image build's upload completed). The producing build here is a
// matrix build and the consumer declares no depends_on, mirroring that manifest.
func TestGenerator_PassthroughArtifact_DownloadNeedsUploadJob(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build-image.yaml"), []byte("on:\n  workflow_call:\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build-bundle.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"staging"},
		Builds: []config.BuildConfig{
			{
				Name:     "image",
				Workflow: ".github/workflows/build-image.yaml",
				Triggers: []string{"src/**"},
				Matrix: &config.MatrixConfig{
					Dimensions: map[string][]string{
						"os":   {"linux"},
						"arch": {"amd64", "arm64"},
					},
				},
				PassthroughArtifact: &config.PassthroughArtifact{
					Upload: "dist/**",
				},
			},
			{
				// No depends_on: the artifact edge alone must sequence this
				// download after the producer's upload, exactly as 2env declares.
				Name:     "bundle",
				Workflow: ".github/workflows/build-bundle.yaml",
				Triggers: []string{"src/**"},
				PassthroughArtifact: &config.PassthroughArtifact{
					Downloads: []string{"image"},
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// The producer must emit its upload post-job.
	require.Contains(t, result, "build-image-upload:",
		"matrix build with artifact.upload must emit a post-upload job")

	// The consumer's download pre-job must wait for that upload job so the
	// artifact exists before the download runs.
	needs := downloadJobNeeds(t, result, "build-bundle-download")
	assert.Contains(t, needs, "build-image-upload",
		"download pre-job must depend on the producer's -upload job, otherwise it races the upload and fails with 'Artifact not found'")
}

// uploadJobBody returns the text of the "  <jobID>:\n" block from a generated
// workflow, spanning until the next two-space-indented job header (or EOF). It
// lets a test assert on the step ordering inside a single owned job.
func uploadJobBody(t *testing.T, workflow, jobID string) string {
	t.Helper()
	header := "\n  " + jobID + ":\n"
	idx := strings.Index(workflow, header)
	require.NotEqual(t, -1, idx, "job %q not found in generated workflow", jobID)
	start := idx + len(header)
	rest := workflow[start:]
	// Find the next job header at two-space indentation (a line "  word:").
	re := regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\n`)
	if loc := re.FindStringIndex(rest); loc != nil {
		return rest[:loc[0]]
	}
	return rest
}

// TestGenerator_PassthroughArtifact_MatrixUploadCollectsLegs asserts that a
// matrix build's cascade-owned -upload post-job collects the per-leg artifacts
// before consolidating them into the single build-<name> artifact consumers
// download. cascade cannot inject upload steps into the reusable callback's
// matrix legs, so each leg uploads a per-leg artifact following the
// "<build-name>-*" convention, and the upload post-job downloads that pattern
// (merge-multiple) into the upload path, then uploads the consolidated
// build-<name>. The manifest mirrors 2env: a matrix "image" build feeding a
// "bundle" consumer, where the legs upload image-<os>-<arch>.
func TestGenerator_PassthroughArtifact_MatrixUploadCollectsLegs(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build-image.yaml"), []byte("on:\n  workflow_call:\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build-bundle.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"staging"},
		Builds: []config.BuildConfig{
			{
				Name:     "image",
				Workflow: ".github/workflows/build-image.yaml",
				Triggers: []string{"src/**"},
				Matrix: &config.MatrixConfig{
					Dimensions: map[string][]string{
						"os":   {"linux"},
						"arch": {"amd64", "arm64"},
					},
				},
				PassthroughArtifact: &config.PassthroughArtifact{
					Upload: "dist/**",
				},
			},
			{
				Name:     "bundle",
				Workflow: ".github/workflows/build-bundle.yaml",
				Triggers: []string{"src/**"},
				PassthroughArtifact: &config.PassthroughArtifact{
					Downloads: []string{"image"},
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	body := uploadJobBody(t, result, "build-image-upload")

	// The post-job must collect the per-leg artifacts first.
	assert.Contains(t, body, "uses: actions/download-artifact@v8",
		"matrix upload post-job must download per-leg artifacts before uploading")
	assert.Contains(t, body, "pattern: image-*",
		"collect step must use the <build-name>-* convention pattern")
	assert.Contains(t, body, "merge-multiple: true",
		"collect step must merge per-leg artifacts into one path")
	assert.Contains(t, body, "path: dist",
		"collect step must download into the upload directory (glob stripped)")

	// The collect step must precede the upload step.
	collectIdx := strings.Index(body, "actions/download-artifact")
	uploadIdx := strings.Index(body, "actions/upload-artifact")
	require.NotEqual(t, -1, collectIdx)
	require.NotEqual(t, -1, uploadIdx)
	assert.Less(t, collectIdx, uploadIdx,
		"collect (download) step must come before the consolidating upload step")

	// The consolidation still uploads build-image, and the bundle download
	// references the same name: end-to-end name consistency.
	assert.Contains(t, body, "name: build-image",
		"consolidation must upload the build-<name> artifact consumers download")
	assert.Contains(t, result, "build-bundle-download:",
		"consumer must emit its download pre-job")

	downloadBody := uploadJobBody(t, result, "build-bundle-download")
	assert.Contains(t, downloadBody, "name: build-image",
		"bundle download must reference the consolidated build-image artifact")
}

// TestGenerator_PassthroughArtifact_NonMatrixUploadNoCollect asserts that a
// non-matrix build's -upload post-job uploads directly with no collect step,
// since its callback runs on a single runner whose artifacts already live at
// the upload path. A spurious download-artifact in a non-matrix upload job
// would look for per-leg artifacts that never exist.
func TestGenerator_PassthroughArtifact_NonMatrixUploadNoCollect(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "compile",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
				PassthroughArtifact: &config.PassthroughArtifact{
					Upload: "dist/",
				},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	body := uploadJobBody(t, result, "build-compile-upload")
	assert.NotContains(t, body, "actions/download-artifact",
		"non-matrix upload job must not emit a per-leg collect step")
	assert.Contains(t, body, "uses: actions/upload-artifact@v7",
		"non-matrix upload job must still upload directly")
}

// TestGenerator_DispatchInputs_StringType asserts that a string dispatch_input
// is emitted correctly in the workflow_dispatch.inputs block.
func TestGenerator_DispatchInputs_StringType(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		DispatchInputs: map[string]config.DispatchInput{
			"deploy_tag": {
				Type:        config.DispatchInputTypeString,
				Description: "Override image tag",
				Required:    boolPtr(false),
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "      deploy_tag:\n")
	assert.Contains(t, result, "        type: string\n")
	assert.Contains(t, result, `        description: "Override image tag"`)
	// required: omitted when false
	assert.NotContains(t, result, "        required: true\n")
}

// TestGenerator_DispatchInputs_BooleanType asserts a boolean dispatch_input is
// emitted with type: boolean.
func TestGenerator_DispatchInputs_BooleanType(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		DispatchInputs: map[string]config.DispatchInput{
			"force_rebuild": {
				Type:    config.DispatchInputTypeBoolean,
				Default: false,
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "      force_rebuild:\n")
	assert.Contains(t, result, "        type: boolean\n")
	assert.Contains(t, result, "        default: 'false'\n")
}

// TestGenerator_DispatchInputs_ChoiceType asserts a choice dispatch_input emits
// type: choice and all options.
func TestGenerator_DispatchInputs_ChoiceType(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		DispatchInputs: map[string]config.DispatchInput{
			"target_region": {
				Type:    config.DispatchInputTypeChoice,
				Options: []string{"us-east-1", "eu-west-1"},
				Default: "us-east-1",
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "      target_region:\n")
	assert.Contains(t, result, "        type: choice\n")
	assert.Contains(t, result, "        options:\n")
	assert.Contains(t, result, "          - us-east-1\n")
	assert.Contains(t, result, "          - eu-west-1\n")
	assert.Contains(t, result, "        default: 'us-east-1'\n")
}

// TestGenerator_DispatchInputs_RequiredFlag asserts required: true is emitted
// when the DispatchInput has Required: true.
func TestGenerator_DispatchInputs_RequiredFlag(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		DispatchInputs: map[string]config.DispatchInput{
			"release_notes": {
				Type:     config.DispatchInputTypeString,
				Required: boolPtr(true),
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "      release_notes:\n")
	assert.Contains(t, result, "        required: true\n")
}

// TestGenerator_DispatchInputs_OmittedWhenEmpty asserts the dispatch_inputs
// block produces no extra entries when DispatchInputs is nil.
func TestGenerator_DispatchInputs_OmittedWhenEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Only the generator-owned inputs should appear.
	assert.Contains(t, result, "      environment:\n")
	assert.Contains(t, result, "      dry_run:\n")
}

// TestGenerator_DispatchInputs_RoutedToCallback asserts that when a callback
// workflow declares a dispatch input by name, the generator threads
// ${{ inputs.<name> }} into the job's with: block.
func TestGenerator_DispatchInputs_RoutedToCallback(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))

	// Build workflow that declares the dispatch input as one of its inputs.
	buildWorkflow := `
on:
  workflow_call:
    inputs:
      target_region:
        type: string
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		DispatchInputs: map[string]config.DispatchInput{
			"target_region": {
				Type:    config.DispatchInputTypeChoice,
				Options: []string{"us-east-1", "eu-west-1"},
				Default: "us-east-1",
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// The dispatch input must appear in the on: block.
	assert.Contains(t, result, "      target_region:\n")
	// The build job must receive ${{ inputs.target_region }} via with:.
	assert.Contains(t, result, "      target_region: ${{ inputs.target_region }}\n")
}

// TestGenerator_DispatchInputs_NotRoutedWhenCallbackOmitsIt asserts that
// dispatch inputs are NOT added to a callback's with: block when the callback
// workflow does not declare that input.
func TestGenerator_DispatchInputs_NotRoutedWhenCallbackOmitsIt(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	// Build workflow that does NOT declare target_region.
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		DispatchInputs: map[string]config.DispatchInput{
			"target_region": {
				Type:    config.DispatchInputTypeChoice,
				Options: []string{"us-east-1", "eu-west-1"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// The dispatch input must appear in the on: block.
	assert.Contains(t, result, "      target_region:\n")
	// But must NOT be threaded into the build job's with: (callback doesn't declare it).
	assert.NotContains(t, result, "target_region: ${{ inputs.target_region }}")
}

// TestGenerator_DispatchInputs_SortedDeterministic asserts multiple dispatch
// inputs are emitted in alphabetical order for stable diffs.
func TestGenerator_DispatchInputs_SortedDeterministic(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		DispatchInputs: map[string]config.DispatchInput{
			"zzz_last":  {Type: config.DispatchInputTypeString},
			"aaa_first": {Type: config.DispatchInputTypeString},
			"mmm_mid":   {Type: config.DispatchInputTypeString},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	idxFirst := strings.Index(result, "      aaa_first:\n")
	idxMid := strings.Index(result, "      mmm_mid:\n")
	idxLast := strings.Index(result, "      zzz_last:\n")
	require.Greater(t, idxFirst, 0)
	require.Greater(t, idxMid, 0)
	require.Greater(t, idxLast, 0)
	assert.Less(t, idxFirst, idxMid, "aaa_first must appear before mmm_mid")
	assert.Less(t, idxMid, idxLast, "mmm_mid must appear before zzz_last")
}

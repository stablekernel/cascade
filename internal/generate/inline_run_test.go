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

// TestGenerator_InlineRunBuildEmitsStep asserts that a build callback declaring
// run: is emitted as a cascade-owned job with an inline run: step rather than a
// jobs.<id>.uses reusable-workflow call.
func TestGenerator_InlineRunBuildEmitsStep(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "smoke",
				Run:      "go test ./...",
				Triggers: []string{"src/**"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "build-smoke:")
	// inline body
	assert.Contains(t, result, "runs-on: ubuntu-latest")
	assert.Contains(t, result, "shell: bash")
	assert.Contains(t, result, "go test ./...")
	// must NOT be emitted as a reusable-workflow call or carry secrets: inherit
	buildJob := jobSection(result, "build-smoke:")
	assert.NotContains(t, buildJob, "uses:")
	assert.NotContains(t, buildJob, "secrets: inherit")
}

// TestGenerator_InlineRunShellHonored asserts shell: overrides the bash default.
func TestGenerator_InlineRunShellHonored(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "py",
				Run:      "print('hi')",
				Shell:    "python",
				Triggers: []string{"src/**"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-py:")
	assert.Contains(t, job, "shell: python")
	assert.NotContains(t, job, "shell: bash")
}

// TestGenerator_WorkflowCallbackStillUsesReusable asserts a workflow: callback
// is unchanged: it still emits jobs.<id>.uses + secrets: inherit.
func TestGenerator_WorkflowCallbackStillUsesReusable(t *testing.T) {
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
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-app:")
	assert.Contains(t, job, "uses: ./.github/workflows/build.yaml")
	assert.Contains(t, job, "secrets: inherit")
	assert.NotContains(t, job, "shell: bash")
}

// TestGenerator_InlineRunStandardInputsReachStep asserts that the standard
// inputs a reusable callback would receive via with: (environment, sha) reach
// an inline run: step as env: variables when the callback declares them.
func TestGenerator_InlineRunStandardInputsReachStep(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "smoke",
				Run:      "curl -sf https://$ENVIRONMENT.example.com/healthz",
				Triggers: []string{"src/**"},
				Inputs:   map[string]interface{}{"sha": nil},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-smoke:")
	assert.Contains(t, job, "env:")
	assert.Contains(t, job, "ENVIRONMENT: ${{ github.event.inputs.environment || 'dev' }}")
	assert.Contains(t, job, "SHA: ${{ needs.setup.outputs.head_sha }}")
}

// TestGenerator_InlineAndWorkflowMixEmitsBoth covers the e2e scenario: a manifest
// with one workflow: callback and one run: callback produces a workflow where
// the workflow: callback is a jobs.<id>.uses job and the run: callback is a
// run: step.
func TestGenerator_InlineAndWorkflowMixEmitsBoth(t *testing.T) {
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
			},
			{
				Name:     "smoke",
				Run:      "echo smoke",
				Triggers: []string{"src/**"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	appJob := jobSection(result, "build-app:")
	assert.Contains(t, appJob, "uses: ./.github/workflows/build.yaml")

	smokeJob := jobSection(result, "build-smoke:")
	assert.Contains(t, smokeJob, "run: |")
	assert.Contains(t, smokeJob, "echo smoke")
	assert.NotContains(t, smokeJob, "uses:")
}

// TestPromoteGenerator_InlineRunDeployEmitsStep asserts that a deploy callback
// declaring run: is emitted as a cascade-owned job with an inline run: step in
// the promote workflow (both the trigger-gated and prod deploy jobs), with the
// standard environment/sha inputs reaching the step as env: variables.
func TestPromoteGenerator_InlineRunDeployEmitsStep(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "notify",
				Run:      "echo deploying to $ENVIRONMENT @ $SHA",
				Triggers: []string{"deploy/**"},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	deployJob := jobSection(content, "deploy-notify:")
	require.NotEmpty(t, deployJob)
	assert.Contains(t, deployJob, "runs-on: ubuntu-latest")
	assert.Contains(t, deployJob, "shell: bash")
	assert.Contains(t, deployJob, "echo deploying to $ENVIRONMENT @ $SHA")
	assert.Contains(t, deployJob, "ENVIRONMENT: ${{ needs.preflight.outputs.target_env }}")
	assert.Contains(t, deployJob, "SHA: ${{ needs.preflight.outputs.source_sha }}")
	assert.NotContains(t, deployJob, "uses:")
	assert.NotContains(t, deployJob, "secrets: inherit")

	prodJob := jobSection(content, "deploy-notify-prod:")
	require.NotEmpty(t, prodJob)
	assert.Contains(t, prodJob, "run: |")
	assert.Contains(t, prodJob, "ENVIRONMENT: prod")
	assert.NotContains(t, prodJob, "uses:")
}

// TestPromoteGenerator_InlineRunDeployImageTag asserts an inline deploy that
// declares an image_tag input gets IMAGE_TAG in its env (the input-seeding fix
// for inline deploys, which have no reusable-workflow file to discover from).
func TestPromoteGenerator_InlineRunDeployImageTag(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "notify",
				Run:      "echo $IMAGE_TAG",
				Triggers: []string{"deploy/**"},
				Inputs:   map[string]interface{}{"image_tag": nil},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	deployJob := jobSection(content, "deploy-notify:")
	require.NotEmpty(t, deployJob)
	assert.Contains(t, deployJob, "IMAGE_TAG: ${{ needs.preflight.outputs.source_image_tag }}")

	prodJob := jobSection(content, "deploy-notify-prod:")
	require.NotEmpty(t, prodJob)
	assert.Contains(t, prodJob, "IMAGE_TAG: ${{ needs.preflight.outputs.prod_version }}")
}

// TestGenerator_InlineRunDepOutputEnvNameMangled asserts a hyphenated dependency
// output (e.g. image-tag) is surfaced as a shell-safe env var IMAGE_TAG, and a
// dep output named sha does not duplicate the standard SHA: env entry.
func TestGenerator_InlineRunDepOutputEnvNameMangled(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	// Build workflow that emits image-tag and sha outputs.
	buildWorkflow := `
on:
  workflow_call:
    outputs:
      image-tag:
        value: x
      sha:
        value: y
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte(buildWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
			},
			{
				Name:      "smoke",
				Run:       "echo $IMAGE_TAG $SHA",
				Triggers:  []string{"src/**"},
				DependsOn: []string{"app"},
				Inputs:    map[string]interface{}{"image-tag": nil, "sha": nil},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-smoke:")
	require.NotEmpty(t, job)
	assert.Contains(t, job, "IMAGE_TAG: ${{ needs.build-app.outputs.image-tag }}")
	// SHA must appear exactly once (standard input), not duplicated by the dep output.
	assert.Equal(t, 1, strings.Count(job, "SHA:"))
}

// jobSection returns the text of the job whose header line (e.g. "build-app:")
// starts the section, up to the next top-level (two-space-indented) job header.
func jobSection(yaml, header string) string {
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header && strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(strings.TrimSpace(l), ":") && len(strings.TrimSpace(l)) > 1 {
			// next job header at the same two-space indent
			trimmed := strings.TrimLeft(l, " ")
			if len(l)-len(trimmed) == 2 {
				end = i
				break
			}
		}
	}
	return strings.Join(lines[start:end], "\n")
}

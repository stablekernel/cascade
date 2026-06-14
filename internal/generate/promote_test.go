package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// concurrencyGroupLine extracts the "  group: ..." line from the top-level
// concurrency: block of a generated workflow. It lets concurrency tests assert on
// the group key in isolation, without false matches from input definitions or CLI
// flags elsewhere in the workflow that mention the same context expressions.
func concurrencyGroupLine(t *testing.T, content string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if line == "concurrency:" {
			require.Less(t, i+1, len(lines), "concurrency: block must have a group line")
			return lines[i+1]
		}
	}
	t.Fatalf("no top-level concurrency: block found in workflow")
	return ""
}

// stepRunBody extracts the run: script body of the step whose "- name: <name>"
// header matches stepName, from a generated workflow. It returns only the shell
// lines under that step's "run: |" block, stopping at the next step or key at the
// same or shallower indentation. This lets injection tests assert on what the
// shell actually sees, without false matches from a sibling env: mapping (which
// is the safe place for ${{ ... }} expansions) elsewhere in the same step.
func stepRunBody(t *testing.T, content, stepName string) string {
	t.Helper()
	lines := strings.Split(content, "\n")

	stepIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "- name: "+stepName) {
			stepIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, stepIdx, 0, "step %q not found in workflow", stepName)

	runIdx := -1
	for i := stepIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		// Stop if we hit the next step before finding this step's run: block.
		if strings.HasPrefix(trimmed, "- name: ") {
			break
		}
		if trimmed == "run: |" || trimmed == "run: |-" {
			runIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, runIdx, 0, "step %q has no block run: body", stepName)

	runIndent := len(lines[runIdx]) - len(strings.TrimLeft(lines[runIdx], " "))
	var body []string
	for i := runIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			body = append(body, line)
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent <= runIndent {
			break
		}
		body = append(body, line)
	}
	return strings.Join(body, "\n")
}

// TestPromoteGenerator_ModeInputNotInterpolatedIntoRun asserts that the
// workflow_dispatch "mode" input is not echoed into a run: shell body via
// ${{ github.event.inputs.mode }}. A mode value containing shell metacharacters
// would otherwise break out of the echo. It must be bound to env: and printed as
// a quoted shell variable.
func TestPromoteGenerator_ModeInputNotInterpolatedIntoRun(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	body := stepRunBody(t, content, "Validate Promotion")
	assert.NotContains(t, body, "${{ github.event.inputs.mode }}",
		"the mode input must not be interpolated into the Validate Promotion run: body")
	assert.Contains(t, content, "MODE: ${{ github.event.inputs.mode }}")
	assert.Contains(t, body, "echo \"Mode: $MODE\"")
}

func TestPromoteGenerator_Generate(t *testing.T) {
	tests := []struct {
		name         string
		environments []string
		// Mode dropdown contains "default" + all cascade targets directly
		wantModeOptions []string
	}{
		{
			name:         "two environments",
			environments: []string{"dev", "prod"},
			// With 2 envs: default + dev-to-prod
			wantModeOptions: []string{"default", "dev-to-prod"},
		},
		{
			name:         "three environments",
			environments: []string{"dev", "test", "prod"},
			// With 3 envs: default + cascade targets for all source-to-target combinations
			wantModeOptions: []string{"default", "dev-to-test", "dev-to-prod", "test-to-prod"},
		},
		{
			name:         "four environments",
			environments: []string{"dev", "staging", "uat", "prod"},
			// With 4 envs: default + cascade targets for all combinations
			wantModeOptions: []string{"default", "dev-to-staging", "dev-to-uat", "dev-to-prod", "staging-to-uat", "staging-to-prod", "uat-to-prod"},
		},
		{
			name:         "five environments",
			environments: []string{"dev", "staging", "uat", "perf", "prod"},
			// With 5 envs: default + cascade targets for all combinations
			wantModeOptions: []string{"default", "dev-to-staging", "dev-to-uat", "dev-to-perf", "dev-to-prod", "staging-to-uat", "staging-to-perf", "staging-to-prod", "uat-to-perf", "uat-to-prod", "perf-to-prod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.TrunkConfig{
				TrunkBranch:  "main",
				Environments: tt.environments,
			}

			gen := NewPromoteGenerator(cfg, "")
			content, err := gen.Generate()
			require.NoError(t, err)

			// Verify header
			assert.Contains(t, content, "# AUTO-GENERATED by cascade")
			assert.Contains(t, content, "name: Promote")

			// Verify environment documentation in header
			envLine := "# Environments: " + strings.Join(tt.environments, " → ")
			assert.Contains(t, content, envLine)

			// Verify all mode options (default + cascade targets) in single dropdown
			for _, opt := range tt.wantModeOptions {
				assert.Contains(t, content, "          - "+opt)
			}

			// Verify NO separate target dropdown (old format)
			assert.NotContains(t, content, "      target:")

			// Verify jobs exist
			assert.Contains(t, content, "  preflight:")
			assert.Contains(t, content, "  promote:")
			assert.Contains(t, content, "  finalize:")

			// Verify breaking change gate (handled by CLI now)
			assert.Contains(t, content, "allow_breaking_changes")
			assert.Contains(t, content, "cascade promote preflight")
			assert.Contains(t, content, "--allow-breaking")

			// Should NOT have old bash-based breaking change check
			assert.NotContains(t, content, "Check Breaking Changes")
		})
	}
}

func TestPromoteGenerator_EnvironmentCases(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "staging", "uat", "prod"},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// CLI-based preflight in preflight job
	assert.Contains(t, content, "cascade promote preflight")
	assert.Contains(t, content, "--gha-output")

	// CLI-based execution in promote job (validation only)
	assert.Contains(t, content, "cascade promote")

	// CLI-based finalize in finalize job
	assert.Contains(t, content, "cascade promote finalize")
	assert.Contains(t, content, "--commit-push")

	// All expected outputs are present
	assert.Contains(t, content, "source_env:")
	assert.Contains(t, content, "target_env:")
	assert.Contains(t, content, "deploys_to_run:")
	assert.Contains(t, content, "promotion_result:")
}

func TestPromoteGenerator_ValidYAML(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Basic YAML structure checks
	assert.Contains(t, content, "name: Promote\n\non:")
	assert.Contains(t, content, "on:\n  workflow_dispatch:")
	assert.Contains(t, content, "jobs:\n  preflight:")

	// Check indentation is consistent (2 spaces)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, " ") {
			// Count leading spaces
			trimmed := strings.TrimLeft(line, " ")
			spaces := len(line) - len(trimmed)
			assert.True(t, spaces%2 == 0, "Indentation should be multiples of 2: %q", line)
		}
	}
}

func TestPromoteGenerator_OrphanCleanup(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "staging", "uat", "perf", "prod"},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify orphan cleanup step exists
	assert.Contains(t, content, "Cleanup Orphaned Releases")
	assert.Contains(t, content, "skipped_envs")
	assert.Contains(t, content, "--action delete")
}

func TestPromoteGenerator_PublishOnProd(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify publish step only runs when target is final env (prod)
	assert.Contains(t, content, "Publish Release")
	assert.Contains(t, content, "is_final_env == 'true'")
	assert.Contains(t, content, "action: publish")

	// Verify prerelease step runs at second-to-last env (test)
	assert.Contains(t, content, "Create Prerelease")
	assert.Contains(t, content, "is_prerelease_env == 'true'")
	assert.Contains(t, content, "action: prerelease")

	// Verify release-data extraction from promotion result
	assert.Contains(t, content, "source_version")
	assert.Contains(t, content, "release-data")
	assert.Contains(t, content, "sem_version")
	assert.Contains(t, content, "rc_version")
}

// TestPromoteGenerator_PreflightDeclaresSourceImageTag verifies the preflight job
// declares a source_image_tag output. Deploy jobs reference
// needs.preflight.outputs.source_image_tag for the image_tag input, so the
// preflight outputs block must declare it or the reference resolves to an empty
// string on real GitHub (and actionlint flags an undefined property).
func TestPromoteGenerator_PreflightDeclaresSourceImageTag(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content,
		"source_image_tag: ${{ steps.preflight.outputs.source_image_tag }}",
		"preflight outputs block must declare source_image_tag so deploy jobs resolve a non-empty image_tag")
}

// deployWithImageDigestInput is a reusable deploy workflow that accepts both
// image_tag and image_digest, used to verify additive digest threading.
const deployWithImageDigestInput = `name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        required: false
        type: string
      sha:
        required: false
        type: string
      image_tag:
        required: false
        type: string
      image_digest:
        required: false
        type: string
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - run: 'true'
`

// TestPromoteGenerator_PreflightDeclaresSourceImageDigest asserts the preflight
// job outputs block declares source_image_digest so deploy jobs can resolve it.
func TestPromoteGenerator_PreflightDeclaresSourceImageDigest(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content,
		"source_image_digest: ${{ steps.preflight.outputs.source_image_digest }}",
		"preflight outputs block must declare source_image_digest so deploy jobs resolve a digest")
}

// TestPromoteGenerator_DeployThreadsImageDigestWhenDeclared asserts that when a
// reusable deploy workflow declares an image_digest input, the generated deploy
// job with: block threads BOTH image_tag and image_digest (additive).
func TestPromoteGenerator_DeployThreadsImageDigestWhenDeclared(t *testing.T) {
	tmpDir := t.TempDir()
	wfDir := filepath.Join(tmpDir, ".github/workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "deploy.yaml"),
		[]byte(deployWithImageDigestInput), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, content, "deploy-app")
	require.NotEmpty(t, block, "deploy-app job not found")

	assert.Contains(t, block,
		"image_tag: ${{ needs.preflight.outputs.source_image_tag }}",
		"deploy job must still thread image_tag (non-breaking)")
	assert.Contains(t, block,
		"image_digest: ${{ needs.preflight.outputs.source_image_digest }}",
		"deploy job must additively thread image_digest when the workflow declares it")
}

// TestPromoteGenerator_DeployOmitsImageDigestWhenNotDeclared asserts the
// non-breaking path: a deploy workflow that declares image_tag but NOT
// image_digest gets image_tag only, with no image_digest line emitted.
func TestPromoteGenerator_DeployOmitsImageDigestWhenNotDeclared(t *testing.T) {
	tmpDir := t.TempDir()
	wfDir := filepath.Join(tmpDir, ".github/workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	deployTagOnly := `name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        required: false
        type: string
      sha:
        required: false
        type: string
      image_tag:
        required: false
        type: string
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - run: 'true'
`
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "deploy.yaml"),
		[]byte(deployTagOnly), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, content, "deploy-app")
	require.NotEmpty(t, block, "deploy-app job not found")

	assert.Contains(t, block,
		"image_tag: ${{ needs.preflight.outputs.source_image_tag }}",
		"deploy job must thread image_tag")
	assert.NotContains(t, block, "image_digest:",
		"deploy job must NOT emit image_digest when the workflow does not declare it")
}

func TestPromoteGenerator_DryRunSupport(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify dry run input exists
	assert.Contains(t, content, "dry_run:")
	assert.Contains(t, content, "type: boolean")

	// Verify dry run condition on promote job (handles undefined inputs correctly)
	assert.Contains(t, content, "if: ${{ github.event.inputs.dry_run != 'true' }}")
}

func TestPromoteGenerator_DeployCheckboxes(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app-deploy", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"app"}},
			{Name: "cdk", Workflow: ".github/workflows/deploy-cdk.yaml", Triggers: []string{".aws/cdk/**"}},
			{Name: "k8s", Workflow: ".github/workflows/deploy-k8s.yaml", Triggers: []string{".k8s/**"}},
		},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify each deploy has a checkbox input
	assert.Contains(t, content, "deploy_app-deploy:")
	assert.Contains(t, content, "deploy_cdk:")
	assert.Contains(t, content, "deploy_k8s:")

	// Verify they are boolean inputs
	lines := strings.Split(content, "\n")
	foundAppDeploy := false
	foundCdk := false
	foundK8s := false
	for i, line := range lines {
		if strings.Contains(line, "deploy_app-deploy:") {
			foundAppDeploy = true
			// Next few lines should have description, type, and default
			// Note: deploy checkboxes are deprecated in favor of the 'deploys' input
			if i+1 < len(lines) {
				assert.Contains(t, lines[i+1], "[Deprecated] Include app-deploy deployment")
			}
			if i+2 < len(lines) {
				assert.Contains(t, lines[i+2], "type: boolean")
			}
			if i+3 < len(lines) {
				assert.Contains(t, lines[i+3], "default: true")
			}
		}
		if strings.Contains(line, "deploy_cdk:") {
			foundCdk = true
			if i+1 < len(lines) {
				assert.Contains(t, lines[i+1], "[Deprecated] Include cdk deployment")
			}
		}
		if strings.Contains(line, "deploy_k8s:") {
			foundK8s = true
			if i+1 < len(lines) {
				assert.Contains(t, lines[i+1], "[Deprecated] Include k8s deployment")
			}
		}
	}

	assert.True(t, foundAppDeploy, "deploy_app-deploy input not found")
	assert.True(t, foundCdk, "deploy_cdk input not found")
	assert.True(t, foundK8s, "deploy_k8s input not found")
}

func TestPromoteGenerator_DeployDetectionOutputs(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "cdk", Workflow: ".github/workflows/deploy-cdk.yaml", Triggers: []string{".aws/cdk/**"}},
		},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify preflight outputs include deploys_to_run
	assert.Contains(t, content, "deploys_to_run:")

	// Verify CLI-based deploy detection (now inside preflight command)
	assert.Contains(t, content, "cascade promote preflight")
	assert.Contains(t, content, "--gha-output")
}

func TestPromoteGenerator_ConditionalDeployJobs(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "cdk", Workflow: ".github/workflows/deploy-cdk.yaml", Triggers: []string{".aws/cdk/**"}},
			{Name: "k8s", Workflow: ".github/workflows/deploy-k8s.yaml", Triggers: []string{".k8s/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify deploy jobs exist
	assert.Contains(t, content, "deploy-cdk:")
	assert.Contains(t, content, "deploy-k8s:")

	// Verify conditional execution
	assert.Contains(t, content, "contains(fromJSON(needs.preflight.outputs.deploys_to_run), 'cdk')")
	assert.Contains(t, content, "contains(fromJSON(needs.preflight.outputs.deploys_to_run), 'k8s')")

	// Verify they call the correct workflows
	assert.Contains(t, content, "uses: ./.github/workflows/deploy-cdk.yaml")
	assert.Contains(t, content, "uses: ./.github/workflows/deploy-k8s.yaml")
}

func TestPromoteGenerator_FinalizeNeedsAllDeploys(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "cdk", Workflow: ".github/workflows/deploy-cdk.yaml"},
			{Name: "k8s", Workflow: ".github/workflows/deploy-k8s.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Finalize should need all deploy jobs (including prod deploy jobs for cascade)
	assert.Contains(t, content, "needs: [preflight, promote, deploy-cdk, deploy-k8s, deploy-cdk-prod, deploy-k8s-prod]")
}

func TestPromoteGenerator_PerDeployManifestUpdate(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "cdk", Workflow: ".github/workflows/deploy-cdk.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should use CLI finalize command which handles per-deploy state updates internally
	assert.Contains(t, content, "cascade promote finalize")
	assert.Contains(t, content, "--run-id")
	// Should NOT use bash yq updates - CLI handles it
	assert.NotContains(t, content, "yq eval -i")
}

// =============================================================================
// Per-Deployable Tracking Edge Case Tests
// =============================================================================

func TestPromoteGenerator_MixedDeployTypes(t *testing.T) {
	// Test with all three deploy types: build-linked, trigger-based, and unconstrained
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app-deploy", Workflow: ".github/workflows/deploy-app.yaml", DependsOn: []string{"app"}}, // build-linked
			{Name: "cdk", Workflow: ".github/workflows/deploy-cdk.yaml", Triggers: []string{".aws/cdk/**"}}, // trigger-based
			{Name: "notify", Workflow: ".github/workflows/notify.yaml"},                                     // unconstrained
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// All three should have checkboxes
	assert.Contains(t, content, "deploy_app-deploy:")
	assert.Contains(t, content, "deploy_cdk:")
	assert.Contains(t, content, "deploy_notify:")

	// CLI-based preflight handles all detection (no bash patterns)
	assert.Contains(t, content, "cascade promote preflight")
	assert.Contains(t, content, "--gha-output")

	// All three should have conditional deploy jobs
	assert.Contains(t, content, "deploy-app-deploy:")
	assert.Contains(t, content, "deploy-cdk:")
	assert.Contains(t, content, "deploy-notify:")

	// All three should be in finalize needs (including prod deploy jobs for cascade)
	assert.Contains(t, content, "needs: [preflight, promote, deploy-app-deploy, deploy-cdk, deploy-notify, deploy-app-deploy-prod, deploy-cdk-prod, deploy-notify-prod]")
}

func TestPromoteGenerator_DeployCheckboxEnvVarPassing(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "my-deploy", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
			{Name: "other-deploy", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"lib/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Verify deploy checkboxes are present in workflow inputs
	assert.Contains(t, content, "deploy_my-deploy:")
	assert.Contains(t, content, "deploy_other-deploy:")

	// Verify env vars are passed to preflight for checkbox filtering
	assert.Contains(t, content, "DEPLOY_MY_DEPLOY:")
	assert.Contains(t, content, "DEPLOY_OTHER_DEPLOY:")

	// CLI preflight command handles checkbox filtering internally
	assert.Contains(t, content, "cascade promote preflight")
}

func TestPromoteGenerator_EmptyDeploys(t *testing.T) {
	// Config with no deploys should still generate valid workflow
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{}, // Empty deploys
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should still have basic structure
	assert.Contains(t, content, "name: Promote")
	assert.Contains(t, content, "preflight:")
	assert.Contains(t, content, "promote:")
	assert.Contains(t, content, "finalize:")

	// Should NOT have deploy-specific inputs
	assert.NotContains(t, content, "deploy_")

	// Should NOT have deploys_to_run output (or it should be empty)
	// Finalize should only need preflight and promote
	assert.Contains(t, content, "needs: [preflight, promote]")
}

func TestPromoteGenerator_SingleDeploy(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "only-deploy", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Single deploy should still have checkbox
	assert.Contains(t, content, "deploy_only-deploy:")

	// Should have the deploy job
	assert.Contains(t, content, "deploy-only-deploy:")

	// Finalize needs should include the single deploy (and prod deploy job for cascade)
	assert.Contains(t, content, "needs: [preflight, promote, deploy-only-deploy, deploy-only-deploy-prod]")
}

func TestPromoteGenerator_DeployNameWithSpecialChars(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "my-app-deploy", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
			{Name: "cdk_infra", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"cdk/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Hyphens and underscores should be handled correctly in input names
	assert.Contains(t, content, "deploy_my-app-deploy:")
	assert.Contains(t, content, "deploy_cdk_infra:")

	// Environment variable names should convert hyphens to underscores for preflight env vars
	assert.Contains(t, content, "DEPLOY_MY_APP_DEPLOY:")
	assert.Contains(t, content, "DEPLOY_CDK_INFRA:")
}

func TestPromoteGenerator_DeployJobPassesEnvironmentAndSHA(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy-app.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Deploy job should pass environment and sha inputs
	assert.Contains(t, content, "environment: ${{ needs.preflight.outputs.target_env }}")
	assert.Contains(t, content, "sha: ${{ needs.preflight.outputs.source_sha }}")
}

func TestPromoteGenerator_DeployResultEnvVars(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "cdk", Workflow: ".github/workflows/deploy-cdk.yaml"},
			{Name: "k8s", Workflow: ".github/workflows/deploy-k8s.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Finalize job should use CLI to query deploy results and update state
	assert.Contains(t, content, "cascade promote finalize")
	assert.Contains(t, content, "--run-id")
	// CLI handles deploy result querying internally, no bash needed
	assert.NotContains(t, content, "DEPLOY_CDK_RESULT")
	assert.NotContains(t, content, "DEPLOY_K8S_RESULT")
}

func TestPromoteGenerator_DeployManifestUpdateTimestamp(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// CLI finalize command handles deploy state updates internally
	assert.Contains(t, content, "cascade promote finalize")
	assert.Contains(t, content, "--commit-push")
	// Should NOT have yq manifest updates - CLI handles it
	assert.NotContains(t, content, "yq eval -i")
}

func TestPromoteGenerator_PreflightOutputsDeploysToRun(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Preflight job should output deploys_to_run from CLI
	assert.Contains(t, content, "deploys_to_run: ${{ steps.preflight.outputs.deploys_to_run }}")

	// Should NOT contain old bash-based detection
	assert.NotContains(t, content, "steps.detect-deploys.outputs.deploys_to_run")
	assert.NotContains(t, content, `echo "deploys_to_run=$DEPLOYS_TO_RUN" >> "$GITHUB_OUTPUT"`)
}

func TestPromoteGenerator_BuildLinkedDeployInheritsTriggersForDetection(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Builds: []config.BuildConfig{
			{Name: "api", Workflow: ".github/workflows/build.yaml", Triggers: []string{"api/src/**", "api/go.mod"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "api-deploy", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"api"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Build-linked deploy change detection is now handled by CLI preflight
	assert.Contains(t, content, "cascade promote preflight")

	// Should NOT contain old bash-based pattern conversion
	assert.NotContains(t, content, "api/src/.*")
	assert.NotContains(t, content, "api/go.mod")
}

func TestPromoteGenerator_TriggerBasedDeployUsesOwnTriggers(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "infra", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"terraform/**", "cdk/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Trigger-based deploy detection is now handled by CLI preflight
	assert.Contains(t, content, "cascade promote preflight")

	// Should NOT contain old bash-based trigger pattern matching
	assert.NotContains(t, content, "terraform/.*")
	assert.NotContains(t, content, "cdk/.*")
}

func TestPromoteGenerator_UnconstrainedDeployAlwaysInList(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "notify", Workflow: ".github/workflows/notify.yaml"}, // No triggers or depends_on
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Deploy detection is now handled by CLI preflight command
	assert.Contains(t, content, "cascade promote preflight")
	assert.Contains(t, content, "DEPLOY_NOTIFY: ${{ github.event.inputs.deploy_notify }}")

	// Should NOT contain old bash-based deploy detection
	assert.NotContains(t, content, "# Check notify")
	assert.NotContains(t, content, `DEPLOYS_TO_RUN=$(echo "$DEPLOYS_TO_RUN" | jq -c '. + ["notify"]')`)
}

func TestPromoteGenerator_NeverDeployedEnvHandling(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Deploy detection (including never-deployed scenarios) is handled by CLI now
	assert.Contains(t, content, "cascade promote preflight")

	// Should NOT contain old bash-based never-deployed checks
	assert.NotContains(t, content, "never deployed to $TARGET_ENV - will deploy")
	assert.NotContains(t, content, `if [[ -z "$TARGET_DEPLOY_SHA" || "$TARGET_DEPLOY_SHA" == "null" ]]; then`)
}

func TestPromoteGenerator_DeployDryRunCondition(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Deploy jobs should respect dry_run (handles undefined inputs correctly)
	assert.Contains(t, content, "github.event.inputs.dry_run != 'true' && contains(fromJSON(needs.preflight.outputs.deploys_to_run), 'app')")
}

func TestPromoteGenerator_ManifestCommitOnlyIfChanged(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Manifest commit logic is now handled by the CLI finalize command
	assert.Contains(t, content, "cascade promote finalize")
	assert.Contains(t, content, "--commit-push")

	// Should NOT contain old bash-based commit logic
	assert.NotContains(t, content, "if ! git diff --quiet \"$MANIFEST_FILE\"; then")
}

func TestResolveDeployInputs(t *testing.T) {
	cfg := &config.TrunkConfig{
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Inputs: map[string]interface{}{
					"environment": "${{ matrix.environment }}",
					"sha":         "${{ matrix.sha }}",
					"cluster":     "dev-eks",
				},
				EnvInputs: map[string]map[string]interface{}{
					"prod": {"cluster": "prod-eks"},
				},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")

	// Test dev env - no override
	devInputs := gen.resolveDeployInputs("app", "dev", "abc123", "v1.0.0")
	assert.Equal(t, "dev", devInputs["environment"])
	assert.Equal(t, "abc123", devInputs["sha"])
	assert.Equal(t, "dev-eks", devInputs["cluster"])

	// Test prod env - with override
	prodInputs := gen.resolveDeployInputs("app", "prod", "def456", "v1.1.0")
	assert.Equal(t, "prod", prodInputs["environment"])
	assert.Equal(t, "def456", prodInputs["sha"])
	assert.Equal(t, "prod-eks", prodInputs["cluster"]) // overridden
}

func TestPreflightDeployMatrixOutputs(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/deploy-app.yaml",
				Inputs: map[string]interface{}{
					"environment": "${{ matrix.environment }}",
					"sha":         "${{ matrix.sha }}",
					"cluster":     "dev-eks",
				},
				EnvInputs: map[string]map[string]interface{}{
					"prod": {"cluster": "prod-eks"},
				},
			},
			{
				Name:     "infra",
				Workflow: ".github/workflows/deploy-infra.yaml",
				Inputs: map[string]interface{}{
					"environment": "${{ matrix.environment }}",
					"sha":         "${{ matrix.sha }}",
					"version":     "${{ matrix.version }}",
					"stack":       "main",
				},
			},
			{
				Name:     "no-inputs",
				Workflow: ".github/workflows/deploy-simple.yaml",
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should have matrix outputs for deploys with inputs
	assert.Contains(t, content, "deploy_app_matrix: ${{ steps.build-matrices.outputs.deploy_app_matrix }}")
	assert.Contains(t, content, "deploy_infra_matrix: ${{ steps.build-matrices.outputs.deploy_infra_matrix }}")

	// Should NOT have matrix output for deploy without inputs
	assert.NotContains(t, content, "deploy_no_inputs_matrix")

	// Should have the Build Deploy Matrices step
	assert.Contains(t, content, "Build Deploy Matrices")
	assert.Contains(t, content, "id: build-matrices")

	// Should parse promotion result from preflight (not determine)
	assert.Contains(t, content, "PROMOTION_RESULT: ${{ steps.preflight.outputs.promotion_result }}")
	assert.Contains(t, content, "PROMOTIONS=$(echo \"$PROMOTION_RESULT\" | jq -c '.promotions // []')")

	// Should have matrix building logic for app deploy
	assert.Contains(t, content, "# Build matrix for deploy: app")
	assert.Contains(t, content, "MATRIX_APP='['")
	assert.Contains(t, content, `DEFAULT_INPUTS='{"cluster":"dev-eks","environment":"${{ matrix.environment }}","sha":"${{ matrix.sha }}"}'`)
	assert.Contains(t, content, `ENV_INPUTS='{"prod":{"cluster":"prod-eks"}}'`)

	// Should have matrix building logic for infra deploy
	assert.Contains(t, content, "# Build matrix for deploy: infra")
	assert.Contains(t, content, "MATRIX_INFRA='['")

	// Should merge inputs and substitute variables
	assert.Contains(t, content, "# Merge default inputs with env-specific overrides")
	assert.Contains(t, content, "RESOLVED=$(echo \"$DEFAULT_INPUTS\" | jq -c \".\")")
	assert.Contains(t, content, "ENV_OVERRIDE=$(echo \"$ENV_INPUTS\" | jq -c --arg env \"$ENV\" '.[$env] // {}')")
	assert.Contains(t, content, "RESOLVED=$(echo \"$RESOLVED\" | jq -c --argjson override \"$ENV_OVERRIDE\" '. + $override')")

	// Should substitute special variables
	assert.Contains(t, content, "# Substitute special variables")
	assert.Contains(t, content, `gsub("\\$\\{\\{ matrix.environment \\}\\}"; $env)`)
	assert.Contains(t, content, `gsub("\\$\\{\\{ matrix.sha \\}\\}"; $sha)`)
	assert.Contains(t, content, `gsub("\\$\\{\\{ matrix.version \\}\\}"; $version)`)

	// Should output matrices
	assert.Contains(t, content, `echo "deploy_app_matrix=$MATRIX_APP" >> "$GITHUB_OUTPUT"`)
	assert.Contains(t, content, `echo "deploy_infra_matrix=$MATRIX_INFRA" >> "$GITHUB_OUTPUT"`)
	assert.Contains(t, content, `echo "::notice::Deploy app matrix: $MATRIX_APP"`)
	assert.Contains(t, content, `echo "::notice::Deploy infra matrix: $MATRIX_INFRA"`)
}

func TestPreflightNoMatrixStepWhenNoInputs(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:     "simple",
				Workflow: ".github/workflows/deploy.yaml",
				// No inputs
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should NOT have the Build Deploy Matrices step
	assert.NotContains(t, content, "Build Deploy Matrices")
	assert.NotContains(t, content, "id: build-matrices")
	assert.NotContains(t, content, "deploy_simple_matrix")
}

func TestDeployJobsMatrixStrategy(t *testing.T) {
	tests := []struct {
		name           string
		config         *config.TrunkConfig
		wantContain    []string
		wantNotContain []string
		description    string
	}{
		{
			name: "deploy with inputs uses matrix strategy",
			config: &config.TrunkConfig{
				TrunkBranch:  "main",
				Environments: []string{"dev", "test", "prod"},
				Deploys: []config.DeployConfig{
					{
						Name:     "app",
						Workflow: ".github/workflows/deploy-app.yaml",
						Inputs: map[string]interface{}{
							"environment": "${{ matrix.environment }}",
							"sha":         "${{ matrix.sha }}",
							"cluster":     "dev-eks",
						},
						EnvInputs: map[string]map[string]interface{}{
							"prod": {"cluster": "prod-eks"},
						},
					},
				},
			},
			wantContain: []string{
				"deploy-app:",
				"name: Deploy app (${{ matrix.environment }})",
				"strategy:",
				"fail-fast: false",
				"matrix:",
				"include: ${{ fromJSON(needs.preflight.outputs.deploy_app_matrix) }}",
				"uses: ./.github/workflows/deploy-app.yaml",
				"environment: ${{ matrix.environment }}",
				"sha: ${{ matrix.sha }}",
				"cluster: ${{ matrix.cluster }}",
				"if: ${{ github.event.inputs.dry_run != 'true' && needs.preflight.outputs.deploy_app_matrix != '[]' }}",
			},
			wantNotContain: []string{
				"contains(fromJSON(needs.preflight.outputs.deploys_to_run), 'app')",
			},
			description: "Deploy with inputs should use matrix strategy",
		},
		{
			name: "deploy without inputs uses single deploy",
			config: &config.TrunkConfig{
				TrunkBranch:  "main",
				Environments: []string{"dev", "prod"},
				Deploys: []config.DeployConfig{
					{
						Name:     "simple",
						Workflow: ".github/workflows/deploy-simple.yaml",
					},
				},
			},
			wantContain: []string{
				"deploy-simple:",
				"name: Deploy simple",
				"environment: ${{ needs.preflight.outputs.target_env }}",
				"sha: ${{ needs.preflight.outputs.source_sha }}",
				"if: ${{ github.event.inputs.dry_run != 'true' && contains(fromJSON(needs.preflight.outputs.deploys_to_run), 'simple') }}",
			},
			wantNotContain: []string{
				"strategy:",
				"matrix:",
				"matrix.environment",
				"deploy_simple_matrix",
			},
			description: "Deploy without inputs should use single deploy strategy",
		},
		{
			name: "mixed deploy types",
			config: &config.TrunkConfig{
				TrunkBranch:  "main",
				Environments: []string{"dev", "prod"},
				Deploys: []config.DeployConfig{
					{
						Name:     "app",
						Workflow: ".github/workflows/deploy-app.yaml",
						Inputs: map[string]interface{}{
							"environment": "${{ matrix.environment }}",
							"sha":         "${{ matrix.sha }}",
						},
					},
					{
						Name:     "simple",
						Workflow: ".github/workflows/deploy-simple.yaml",
					},
				},
			},
			wantContain: []string{
				// App with matrix
				"deploy-app:",
				"name: Deploy app (${{ matrix.environment }})",
				"strategy:",
				"matrix:",
				"include: ${{ fromJSON(needs.preflight.outputs.deploy_app_matrix) }}",
				"environment: ${{ matrix.environment }}",
				"sha: ${{ matrix.sha }}",
				// Simple without matrix
				"deploy-simple:",
				"name: Deploy simple",
				"environment: ${{ needs.preflight.outputs.target_env }}",
				"sha: ${{ needs.preflight.outputs.source_sha }}",
			},
			wantNotContain: []string{},
			description:    "Should support both matrix and single deploy strategies",
		},
		{
			name: "deploy with multiple custom inputs",
			config: &config.TrunkConfig{
				TrunkBranch:  "main",
				Environments: []string{"dev", "prod"},
				Deploys: []config.DeployConfig{
					{
						Name:     "infra",
						Workflow: ".github/workflows/deploy-infra.yaml",
						Inputs: map[string]interface{}{
							"environment": "${{ matrix.environment }}",
							"sha":         "${{ matrix.sha }}",
							"version":     "${{ matrix.version }}",
							"stack_name":  "main-stack",
							"region":      "us-west-2",
						},
					},
				},
			},
			wantContain: []string{
				"deploy-infra:",
				"name: Deploy infra (${{ matrix.environment }})",
				"environment: ${{ matrix.environment }}",
				"sha: ${{ matrix.sha }}",
				"version: ${{ matrix.version }}",
				"stack_name: ${{ matrix.stack_name }}",
				"region: ${{ matrix.region }}",
			},
			wantNotContain: []string{},
			description:    "Should pass all custom inputs from matrix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := NewPromoteGenerator(tt.config, "")
			content, err := gen.Generate()
			require.NoError(t, err)

			for _, want := range tt.wantContain {
				assert.Contains(t, content, want, "%s: should contain %q", tt.description, want)
			}

			for _, notWant := range tt.wantNotContain {
				assert.NotContains(t, content, notWant, "%s: should not contain %q", tt.description, notWant)
			}

			// For matrix deploy jobs, verify they don't have runs-on
			// (reusable workflow calls can't have runs-on)
			if strings.Contains(tt.description, "matrix strategy") {
				lines := strings.Split(content, "\n")
				for i, line := range lines {
					// Find deploy job with matrix
					if strings.Contains(line, "deploy-") && strings.HasSuffix(strings.TrimSpace(line), ":") {
						// Check next ~10 lines for matrix and runs-on
						hasMatrix := false
						hasRunsOn := false
						for j := i + 1; j < i+15 && j < len(lines); j++ {
							if strings.Contains(lines[j], "matrix:") {
								hasMatrix = true
							}
							if strings.Contains(lines[j], "runs-on:") && hasMatrix {
								hasRunsOn = true
								break
							}
							// Stop at next job
							if strings.HasPrefix(lines[j], "  ") && !strings.HasPrefix(lines[j], "    ") && strings.HasSuffix(strings.TrimSpace(lines[j]), ":") {
								break
							}
						}
						if hasMatrix {
							assert.False(t, hasRunsOn, "Matrix deploy job should not have runs-on (reusable workflows can't have runs-on)")
						}
					}
				}
			}
		})
	}
}

func TestPromoteGenerator_APIQueryStep(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.TrunkConfig
		wantContain []string
		wantNot     []string
		description string
	}{
		{
			name: "Finalize using CLI instead of bash",
			config: &config.TrunkConfig{
				TrunkBranch:  "main",
				Environments: []string{"dev", "test", "prod"},
				Deploys: []config.DeployConfig{
					{Name: "app", Workflow: ".github/workflows/deploy-app.yaml"},
					{Name: "infra", Workflow: ".github/workflows/deploy-infra.yaml"},
				},
			},
			wantContain: []string{
				"Finalize Promotion",
				"cascade promote finalize",
				"--promotion-result",
				"--repo",
				"--run-id",
				"--commit-push",
				"PROMOTION_RESULT: ${{ needs.preflight.outputs.promotion_result }}",
			},
			wantNot: []string{
				"Query Deploy Results",
				"id: deploy-results",
				"DEPLOY_APP_RESULTS='{}'",
				"gh api repos/${{ github.repository }}/actions/runs/${{ github.run_id }}/jobs",
			},
			description: "Should use CLI finalize instead of bash-based API query",
		},
		{
			name: "Finalize with single deploy uses CLI",
			config: &config.TrunkConfig{
				TrunkBranch:  "main",
				Environments: []string{"dev", "prod"},
				Deploys: []config.DeployConfig{
					{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
				},
			},
			wantContain: []string{
				"cascade promote finalize",
				"--commit-push",
			},
			wantNot: []string{
				"DEPLOY_APP_RESULTS",
			},
			description: "Should use CLI for single deploy finalize",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := NewPromoteGenerator(tt.config, "")
			content, err := gen.Generate()
			require.NoError(t, err)

			for _, want := range tt.wantContain {
				assert.Contains(t, content, want, "%s: should contain %q", tt.description, want)
			}
			for _, notWant := range tt.wantNot {
				assert.NotContains(t, content, notWant, "%s: should NOT contain %q", tt.description, notWant)
			}
		})
	}
}

func TestPromoteGenerator_CascadeDeployStateUpdate(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy-app.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// State updates are now handled by the CLI finalize command
	assert.Contains(t, content, "cascade promote finalize")
	assert.Contains(t, content, "--promotion-result")
	assert.Contains(t, content, "--commit-push")

	// Should NOT contain old bash-based state update logic
	assert.NotContains(t, content, "DEPLOY_APP_RESULTS=$(echo \"$DEPLOY_RESULTS\" | jq -r")
	assert.NotContains(t, content, "yq eval -i \".$MANIFEST_KEY.state.$ENV.deploys")
}

// Task 7: Test that preflight job uses CLI instead of bash
func TestPromoteGenerator_PreflightUsesCLI(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "uat", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
			{Name: "infra", Workflow: ".github/workflows/deploy-infra.yaml"},
		},
	}
	gen := NewPromoteGenerator(cfg, "/tmp")
	content, err := gen.Generate()

	require.NoError(t, err)

	// Should have a single CLI call
	assert.Contains(t, content, "cascade promote preflight")
	assert.Contains(t, content, "--gha-output")

	// Should NOT have the old bash-heavy logic
	assert.NotContains(t, content, "Determine Environments")
	assert.NotContains(t, content, "Validate Source Environment")
	assert.NotContains(t, content, "Check Breaking Changes")
	assert.NotContains(t, content, "Detect Deploy Changes")

	// Should still have required outputs but from CLI step
	assert.Contains(t, content, "source_env: ${{ steps.preflight.outputs.source_env }}")
	assert.Contains(t, content, "target_env: ${{ steps.preflight.outputs.target_env }}")
	assert.Contains(t, content, "deploys_to_run: ${{ steps.preflight.outputs.deploys_to_run }}")
	assert.Contains(t, content, "can_proceed: ${{ steps.preflight.outputs.can_proceed }}")
}

// Task 8: Test that finalize job uses CLI instead of bash
func TestPromoteGenerator_FinalizeUsesCLI(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "uat", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "infra", Workflow: ".github/workflows/deploy.yaml"},
			{Name: "app", Workflow: ".github/workflows/deploy-app.yaml"},
		},
	}
	gen := NewPromoteGenerator(cfg, "/tmp")
	content, err := gen.Generate()

	require.NoError(t, err)

	// Should use CLI for finalize
	assert.Contains(t, content, "cascade promote finalize")
	assert.Contains(t, content, "--commit-push")
	assert.Contains(t, content, "--promotion-result")

	// Should NOT have the old yq-based state updates
	assert.NotContains(t, content, "yq eval -i")
	assert.NotContains(t, content, "Update State")

	// Should still have changelog and release management steps
	assert.Contains(t, content, "Generate Changelog")
	assert.Contains(t, content, "Publish Release")
}

func TestPromoteGenerator_RollbackJobs(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy-app.yaml"},
			{Name: "infra", Workflow: ".github/workflows/deploy-infra.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should have rollback jobs for each deploy
	assert.Contains(t, content, "rollback-app:")
	assert.Contains(t, content, "rollback-infra:")
	assert.Contains(t, content, "name: Rollback app")
	assert.Contains(t, content, "name: Rollback infra")

	// Rollback jobs should use rollback_sha
	assert.Contains(t, content, "sha: ${{ needs.preflight.outputs.rollback_sha }}")

	// Rollback jobs should only run when rollback_on_failure is true
	assert.Contains(t, content, "needs.preflight.outputs.rollback_on_failure == 'true'")

	// Rollback jobs should only run when rollback_sha is not empty
	assert.Contains(t, content, "needs.preflight.outputs.rollback_sha != ''")

	// Rollback jobs should only run when their deploy succeeded
	assert.Contains(t, content, "needs.deploy-app.result == 'success'")
	assert.Contains(t, content, "needs.deploy-infra.result == 'success'")

	// Rollback jobs should run when any deploy failed
	assert.Contains(t, content, "needs.deploy-app.result == 'failure'")
	assert.Contains(t, content, "needs.deploy-infra.result == 'failure'")

	// Preflight outputs should include rollback_sha and rollback_on_failure
	assert.Contains(t, content, "rollback_sha: ${{ steps.preflight.outputs.rollback_sha }}")
	assert.Contains(t, content, "rollback_on_failure: ${{ steps.preflight.outputs.rollback_on_failure }}")
}

// TestPromoteGenerator_NoRollbackWhenNoEnvironments asserts that with
// environments: [] no rollback job is emitted, even when deploys: is non-empty.
// Deploy jobs are only written when len(Environments) > 0, so a rollback job
// would reference deploy jobs that do not exist (needs: deploy-<name>), which
// GitHub rejects at parse ("needs job X which does not exist").
func TestPromoteGenerator_NoRollbackWhenNoEnvironments(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy-app.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// No deploy jobs exist, so no rollback job may reference them.
	assert.NotContains(t, content, "rollback-app:",
		"no rollback job when there are no environments (no deploy jobs exist)")
	assert.NotContains(t, content, "deploy-app:",
		"sanity: no deploy job is emitted when environments is empty")
	assert.NotContains(t, content, "needs.deploy-app",
		"no needs: reference to a nonexistent deploy job")
	// The finalize job's needs: list must not reference deploy jobs either.
	assert.NotContains(t, content, "deploy-app-prod",
		"no needs: reference to a nonexistent prod deploy job")
	assert.NotContains(t, content, "DEPLOY_RESULT_APP",
		"no deploy-result env var dereferencing a nonexistent deploy job")

	// The emitted workflow must remain structurally valid YAML.
	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(content), &parsed),
		"emitted promote workflow must be valid YAML")
}

func TestPromoteGenerator_RollbackOnFailureInput(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should have rollback_on_failure input
	assert.Contains(t, content, "rollback_on_failure:")
	assert.Contains(t, content, "description: 'Revert successful deploys if any fails (atomic promotion)'")
	assert.Contains(t, content, "default: true")

	// Should pass rollback_on_failure to preflight command (with shell default)
	assert.Contains(t, content, "ROLLBACK_ON_FAILURE: ${{ github.event.inputs.rollback_on_failure }}")
	assert.Contains(t, content, "--rollback-on-failure=\"${ROLLBACK_ON_FAILURE:-true}\"")
}

func TestPromoteGenerator_ExternalDeployRollbackJobs(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy-app.yaml"},
		},
		External: []config.ExternalRepoConfig{
			{
				Repo: "example/satellite-cdk",
				Ref:  "main",
				Deploys: []config.ExternalDeployConfig{
					{Name: "cdk", Workflow: "example/satellite-cdk/.github/workflows/deploy.yaml"},
				},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Should have rollback jobs for local and external deploys
	assert.Contains(t, content, "rollback-app:")
	assert.Contains(t, content, "rollback-cdk:")
	assert.Contains(t, content, "name: Rollback cdk (external)")

	// External rollback should call the external workflow
	assert.Contains(t, content, "example/satellite-cdk/.github/workflows/deploy.yaml@main")
}

func TestBuildDeployMatrix(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.TrunkConfig
		deployName  string
		promotions  []map[string]string
		wantMatrix  []map[string]interface{}
		description string
	}{
		{
			name: "multiple promotions with env_inputs overrides",
			config: &config.TrunkConfig{
				Environments: []string{"dev", "test", "prod"},
				Deploys: []config.DeployConfig{
					{
						Name:     "app",
						Workflow: ".github/workflows/deploy-app.yaml",
						Inputs: map[string]interface{}{
							"environment": "${{ matrix.environment }}",
							"sha":         "${{ matrix.sha }}",
							"cluster":     "dev-cluster",
						},
						EnvInputs: map[string]map[string]interface{}{
							"prod": {"cluster": "prod-cluster"},
						},
					},
				},
			},
			deployName: "app",
			promotions: []map[string]string{
				{"environment": "dev", "sha": "abc123", "version": "v1.0.0-rc.1"},
				{"environment": "test", "sha": "abc123", "version": "v1.0.0-rc.1"},
				{"environment": "prod", "sha": "abc123", "version": "v1.0.0-rc.1"},
			},
			wantMatrix: []map[string]interface{}{
				{"environment": "dev", "sha": "abc123", "cluster": "dev-cluster"},
				{"environment": "test", "sha": "abc123", "cluster": "dev-cluster"},
				{"environment": "prod", "sha": "abc123", "cluster": "prod-cluster"},
			},
			description: "Each promotion should get resolved inputs with prod override applied",
		},
		{
			name: "single promotion",
			config: &config.TrunkConfig{
				Environments: []string{"dev", "prod"},
				Deploys: []config.DeployConfig{
					{
						Name:     "cdk",
						Workflow: ".github/workflows/deploy-cdk.yaml",
						Inputs: map[string]interface{}{
							"environment": "${{ matrix.environment }}",
							"sha":         "${{ matrix.sha }}",
							"version":     "${{ matrix.version }}",
						},
					},
				},
			},
			deployName: "cdk",
			promotions: []map[string]string{
				{"environment": "dev", "sha": "def456", "version": "v2.0.0"},
			},
			wantMatrix: []map[string]interface{}{
				{"environment": "dev", "sha": "def456", "version": "v2.0.0"},
			},
			description: "Single promotion should create single matrix entry",
		},
		{
			name: "deploy not found returns empty matrix",
			config: &config.TrunkConfig{
				Environments: []string{"dev", "prod"},
				Deploys: []config.DeployConfig{
					{
						Name:     "app",
						Workflow: ".github/workflows/deploy.yaml",
						Inputs:   map[string]interface{}{},
					},
				},
			},
			deployName: "nonexistent",
			promotions: []map[string]string{
				{"environment": "dev", "sha": "abc123", "version": "v1.0.0"},
			},
			wantMatrix:  []map[string]interface{}{},
			description: "Non-existent deploy should return empty matrix",
		},
		{
			name: "empty promotions returns empty matrix",
			config: &config.TrunkConfig{
				Environments: []string{"dev", "prod"},
				Deploys: []config.DeployConfig{
					{
						Name:     "app",
						Workflow: ".github/workflows/deploy.yaml",
						Inputs:   map[string]interface{}{},
					},
				},
			},
			deployName:  "app",
			promotions:  []map[string]string{},
			wantMatrix:  []map[string]interface{}{},
			description: "Empty promotions should return empty matrix",
		},
		{
			name: "version substitution",
			config: &config.TrunkConfig{
				Environments: []string{"dev", "test"},
				Deploys: []config.DeployConfig{
					{
						Name:     "app",
						Workflow: ".github/workflows/deploy.yaml",
						Inputs: map[string]interface{}{
							"environment": "${{ matrix.environment }}",
							"sha":         "${{ matrix.sha }}",
							"image_tag":   "${{ matrix.version }}",
						},
					},
				},
			},
			deployName: "app",
			promotions: []map[string]string{
				{"environment": "dev", "sha": "aaa111", "version": "v1.0.0-rc.1"},
				{"environment": "test", "sha": "bbb222", "version": "v1.0.0-rc.2"},
			},
			wantMatrix: []map[string]interface{}{
				{"environment": "dev", "sha": "aaa111", "image_tag": "v1.0.0-rc.1"},
				{"environment": "test", "sha": "bbb222", "image_tag": "v1.0.0-rc.2"},
			},
			description: "Matrix variables should be substituted correctly for each promotion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := NewPromoteGenerator(tt.config, "")
			matrix := gen.buildDeployMatrix(tt.deployName, tt.promotions)

			if len(tt.wantMatrix) == 0 {
				assert.Empty(t, matrix, tt.description)
				return
			}

			require.Len(t, matrix, len(tt.wantMatrix), tt.description)
			for i, want := range tt.wantMatrix {
				got := matrix[i]
				for key, wantVal := range want {
					assert.Equal(t, wantVal, got[key], "matrix[%d][%s]: %s", i, key, tt.description)
				}
			}
		})
	}
}

// =============================================================================
// Publish callback (#39)
// =============================================================================

func TestPromoteGenerator_PublishCallbackEmitted(t *testing.T) {
	tmpDir := t.TempDir()
	workflowDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0755))

	publishWorkflow := `name: Publish
on:
  workflow_call:
    inputs:
      build_name:
        type: string
        required: true
      old_version:
        type: string
        required: true
      new_version:
        type: string
        required: true
      sha:
        type: string
        required: true
      artifact_id:
        type: string
        required: false
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "publish.yaml"), []byte(publishWorkflow), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Publish: &config.PublishConfig{
			Workflow: ".github/workflows/publish.yaml",
		},
	}

	gen := NewPromoteGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// Publish job should be emitted
	assert.Contains(t, content, "- name: Publish Artifacts")

	// Should only run when is_final_env is true (publish path)
	assert.Contains(t, content, "needs.preflight.outputs.is_final_env == 'true'")

	// Should pass required inputs to the publish workflow
	assert.Contains(t, content, "build_name")
	assert.Contains(t, content, "old_version")
	assert.Contains(t, content, "new_version")
	assert.Contains(t, content, "artifact_id")
}

func TestPromoteGenerator_NoPublishCallbackWhenNotConfigured(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		// Publish is nil
	}

	gen := NewPromoteGenerator(cfg, tmpDir)
	content, err := gen.Generate()
	require.NoError(t, err)

	// No publish step when not configured
	assert.NotContains(t, content, "Publish Artifacts")
}

// TestPromoteGenerator_HasConcurrencyBlock asserts the generated promote workflow
// declares a top-level concurrency: block keyed by the bare workflow name. Every
// promote finalize pushes the same shared .github/manifest.yaml and shared release
// tags, so ALL promote runs race regardless of mode (#31); the group must serialize
// every promote run, not just same-mode runs. cancel-in-progress is false because
// dropping a mid-flight promote leaves durable env state partially written.
func TestPromoteGenerator_HasConcurrencyBlock(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "staging", "prod"},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "\nconcurrency:\n", "promote workflow must declare top-level concurrency:")
	group := concurrencyGroupLine(t, content)
	assert.Equal(t, "  group: \"${{ github.workflow }}\"", group, "promote concurrency group must be the bare workflow name to serialize all runs")
	assert.NotContains(t, group, "inputs.mode", "promote concurrency group must NOT scope by mode: different modes still push the same manifest")
	assert.Contains(t, content, "cancel-in-progress: false", "promote default must queue, not cancel")
}

// TestPromoteGenerator_ConcurrencyOverride asserts that a manifest-level
// concurrency config is forwarded to the generated promote workflow.
func TestPromoteGenerator_ConcurrencyOverride(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Concurrency: &config.ConcurrencyConfig{
			Group:            "my-custom-promote",
			CancelInProgress: true,
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "group: my-custom-promote", "custom group must propagate to promote")
	assert.Contains(t, content, "cancel-in-progress: true", "custom cancel_in_progress must propagate to promote")
}

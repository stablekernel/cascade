package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deployReusableStub is a minimal valid workflow_call reusable workflow that
// declares exactly the inputs the orchestrate/promote generators thread to a
// reusable deploy workflow (environment, sha, image_tag), so actionlint can
// validate the caller's with: block under full strictness without flagging
// undeclared inputs that are unrelated to the environment-on-uses regression.
const deployReusableStub = `name: Stub Deploy
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
      target_env:
        required: false
        type: string
      dry_run:
        required: false
        type: string
jobs:
  stub:
    runs-on: ubuntu-latest
    steps:
      - run: 'true'
`

// writeDeployReusableStub writes deployReusableStub at every local
// reusable-workflow reference (uses: ./...) found in the generated content, so
// actionlint can resolve each call site against a workflow that declares the
// inputs cascade passes.
func writeDeployReusableStub(t *testing.T, root, content string) {
	t.Helper()

	const marker = "uses: ./"
	seen := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		idx := strings.Index(trimmed, marker)
		if idx < 0 {
			continue
		}
		ref := strings.Fields(trimmed[idx+len("uses: "):])[0]
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		rel := strings.TrimPrefix(ref, "./")
		stubPath := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(stubPath), 0755))
		require.NoError(t, os.WriteFile(stubPath, []byte(deployReusableStub), 0644))
	}
}

// hasJobLevelEnvironment reports whether the given job block contains a
// job-level environment: key. The job-level key sits at exactly 4-space
// indentation ("    environment:"); reusable-workflow with: inputs sit at 6
// spaces ("      environment:") and must not count.
func hasJobLevelEnvironment(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "    environment:") && !strings.HasPrefix(line, "      environment:") {
			return true
		}
	}
	return false
}

// TestEnvGate_Orchestrate_ExternalDeploy_NoJobLevelEnvironment guards issue
// #137: when gha_environment is configured and a deploy uses an external
// reusable workflow (workflow:), the orchestrate caller job must NOT carry a
// job-level environment: key (GHA forbids it on a uses: job). The environment
// name is still passed via the with: environment: input.
func TestEnvGate_Orchestrate_ExternalDeploy_NoJobLevelEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "staging"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-app")
	require.NotEmpty(t, block, "deploy-app job not found")

	assert.Contains(t, block, "uses:", "external deploy must call the reusable workflow via uses:")
	assert.False(t, hasJobLevelEnvironment(block),
		"external (uses:) deploy job must NOT carry a job-level environment: key; block:\n%s", block)
}

// TestEnvGate_Promote_ExternalSingleDeploy_NoJobLevelEnvironment guards #137 for
// the promote single-deploy path: an external deploy caller job must not carry
// a job-level environment: key.
func TestEnvGate_Promote_ExternalSingleDeploy_NoJobLevelEnvironment(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-svc")
	require.NotEmpty(t, block, "deploy-svc job not found")

	assert.Contains(t, block, "uses:", "external deploy must call the reusable workflow via uses:")
	assert.False(t, hasJobLevelEnvironment(block),
		"external (uses:) promote deploy job must NOT carry a job-level environment: key; block:\n%s", block)
	// The environment name is still threaded as a with: input.
	assert.Contains(t, block, "      environment: ${{ needs.preflight.outputs.target_env }}",
		"promote external deploy must still pass environment as a with: input")
}

// TestEnvGate_Promote_ExternalProdDeploy_NoJobLevelEnvironment guards #137 for
// the promote prod-deploy path: the deploy-<n>-prod external caller job must not
// carry a job-level environment: key.
func TestEnvGate_Promote_ExternalProdDeploy_NoJobLevelEnvironment(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "svc", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	result, err := gen.Generate()
	require.NoError(t, err)

	block := jobBlock(t, result, "deploy-svc-prod")
	require.NotEmpty(t, block, "deploy-svc-prod job not found")

	assert.Contains(t, block, "uses:", "external prod deploy must call the reusable workflow via uses:")
	assert.False(t, hasJobLevelEnvironment(block),
		"external (uses:) prod deploy job must NOT carry a job-level environment: key; block:\n%s", block)
	// The environment name is still threaded as a with: input.
	assert.Contains(t, block, "      environment: prod",
		"promote external prod deploy must still pass environment as a with: input")
}

// TestEnvGate_Warning_ExternalDeployWithGHAEnvironment asserts that a
// generate-time warning fires when gha_environment is configured for an
// environment whose deploys are external reusable workflows, since cascade
// cannot set environment: on a uses: caller job.
func TestEnvGate_Warning_ExternalDeployWithGHAEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "staging"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	warnings := gen.Validate()

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "reusable workflow") && strings.Contains(w, "environment") {
			found = true
			break
		}
	}
	assert.True(t, found,
		"expected a warning about gha_environment with external reusable-workflow deploys, got: %v", warnings)
}

// TestEnvGate_Warning_InlineDeployWithGHAEnvironment asserts that the warning
// does NOT fire when the deploy is inline run: (environment: is valid there).
func TestEnvGate_Warning_InlineDeployWithGHAEnvironment(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "staging"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Run: "echo deploying", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	warnings := gen.Validate()

	for _, w := range warnings {
		if strings.Contains(w, "reusable workflow") && strings.Contains(w, "environment") {
			t.Errorf("warning should not fire for inline run: deploy, got: %q", w)
		}
	}
}

// TestEnvGate_Actionlint_ExternalDeployWithGHAEnvironment runs actionlint over
// the generated orchestrate and promote workflows for the external-deploy +
// gha_environment case and asserts there is no "environment is not available"
// error. Skipped when actionlint is not installed so the suite stays hermetic.
func TestEnvGate_Actionlint_ExternalDeployWithGHAEnvironment(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "deploy.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "staging"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{GHAEnvironment: "production"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}},
		},
	}

	orchestrate, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	promote, err := NewPromoteGenerator(cfg, "").Generate()
	require.NoError(t, err)

	for _, tc := range []struct {
		file, content string
	}{
		{"orchestrate.yaml", orchestrate},
		{"promote.yaml", promote},
	} {
		dir := t.TempDir()
		wfDir := filepath.Join(dir, ".github", "workflows")
		require.NoError(t, os.MkdirAll(wfDir, 0755))
		wfPath := filepath.Join(wfDir, tc.file)
		require.NoError(t, os.WriteFile(wfPath, []byte(tc.content), 0644))

		gitInit := exec.Command("git", "init", "-q")
		gitInit.Dir = dir
		require.NoError(t, gitInit.Run(), "git init for actionlint project root")

		// Stub the referenced reusable deploy workflow declaring exactly the
		// inputs the generator threads to it (environment, sha, image_tag) so
		// actionlint resolves the with: block honestly rather than flagging
		// undeclared inputs unrelated to this regression.
		writeDeployReusableStub(t, dir, tc.content)

		// Disable shellcheck: the inline run: bodies trip SC2129 style nits that
		// are orthogonal to this regression and exercised elsewhere.
		cmd := exec.Command(bin, "-shellcheck=", wfPath)
		cmd.Dir = dir
		out, runErr := cmd.CombinedOutput()
		// The regression: GitHub Actions forbids a job-level environment: key on a
		// reusable-workflow caller job. actionlint phrases this as
		// `"environment" is not available`. The fix removes the offending key.
		assert.NotContains(t, string(out), "\"environment\" is not available",
			"%s must not trigger the reusable-workflow environment error:\n%s", tc.file, string(out))
		assert.NoError(t, runErr, "actionlint reported issues for %s:\n%s", tc.file, string(out))
	}
}

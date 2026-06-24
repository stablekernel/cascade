package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Integration tests for the CLI commands

func TestMain(m *testing.M) {
	// Build the binary for testing
	cmd := exec.Command("go", "build", "-o", "cascade-test", ".")
	cmd.Dir = "."
	if err := cmd.Run(); err != nil {
		panic("Failed to build test binary: " + err.Error())
	}

	code := m.Run()

	// Cleanup
	_ = os.Remove("cascade-test")

	os.Exit(code)
}

func runCLI(args ...string) (string, string, error) {
	cmd := exec.Command("./cascade-test", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// runCLIIn runs the test binary with its working directory set to dir, so that
// relative output paths (the generator's default workflow locations) resolve
// under dir, and commands that read the process working directory (the status
// consistency branch lister) operate inside that repository rather than the
// cmd/cascade source dir. The binary path is made absolute because dir is not
// the build dir.
func runCLIIn(dir string, args ...string) (string, string, error) {
	bin, err := filepath.Abs("./cascade-test")
	if err != nil {
		return "", "", err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestVersionCommand(t *testing.T) {
	stdout, _, err := runCLI("version")
	if err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	if stdout == "" {
		t.Error("version command returned empty output")
	}
}

func TestHelpCommand(t *testing.T) {
	stdout, _, err := runCLI("--help")
	if err != nil {
		t.Fatalf("help command failed: %v", err)
	}

	if stdout == "" {
		t.Error("help command returned empty output")
	}
}

func TestParseConfigCommand(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "trunk-config.yaml")

	configContent := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
        triggers:
          - "src/**"
    deploys:
      - name: infra
        workflow: .github/workflows/deploy.yaml
        triggers:
          - "cdk/**"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	stdout, stderr, err := runCLI("parse-config", "--config", configPath)
	if err != nil {
		t.Fatalf("parse-config command failed: %v\nstderr: %s", err, stderr)
	}

	// Verify JSON output
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\noutput: %s", err, stdout)
	}

	// Check expected fields
	if result["valid"] != true {
		t.Errorf("Expected valid=true, got %v", result["valid"])
	}

	if _, ok := result["config"].(map[string]interface{}); !ok {
		t.Fatal("config field missing or wrong type")
	}

	buildNames, ok := result["build_names"].([]interface{})
	if !ok || len(buildNames) != 1 {
		t.Errorf("Expected 1 build name, got %v", result["build_names"])
	}

	deployNames, ok := result["deploy_names"].([]interface{})
	if !ok || len(deployNames) != 1 {
		t.Errorf("Expected 1 deploy name, got %v", result["deploy_names"])
	}
}

func TestParseConfigCommand_InvalidConfig(t *testing.T) {
	// Create an invalid config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid-config.yaml")

	// Missing required project field
	configContent := `
trunk_branch: main
builds: []
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	stdout, _, err := runCLI("parse-config", "--config", configPath)
	// The command should succeed but report validation errors
	if err != nil {
		t.Logf("Command returned error (expected for invalid config): %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\noutput: %s", err, stdout)
	}

	if result["valid"] != false {
		t.Errorf("Expected valid=false for invalid config, got %v", result["valid"])
	}

	errors, ok := result["errors"].([]interface{})
	if !ok || len(errors) == 0 {
		t.Error("Expected validation errors for invalid config")
	}
}

func TestParseConfigCommand_FileNotFound(t *testing.T) {
	stdout, _, err := runCLI("parse-config", "--config", "/nonexistent/path/config.yaml")

	// CLI returns JSON with valid=false for file errors
	if err != nil {
		t.Logf("Command returned error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\noutput: %s", err, stdout)
	}

	if result["valid"] != false {
		t.Errorf("Expected valid=false for nonexistent file, got %v", result["valid"])
	}

	errors, ok := result["errors"].([]interface{})
	if !ok || len(errors) == 0 {
		t.Error("Expected error message for nonexistent file")
	}
}

func TestDetectChangesCommand_MissingFlags(t *testing.T) {
	_, stderr, err := runCLI("detect-changes")

	if err == nil {
		t.Error("Expected error for missing required flags")
	}

	if stderr == "" {
		t.Error("Expected error message about missing flags")
	}
}

func TestGenerateChangelogCommand_MissingFlags(t *testing.T) {
	_, stderr, err := runCLI("generate-changelog")

	if err == nil {
		t.Error("Expected error for missing required flags")
	}

	if stderr == "" {
		t.Error("Expected error message about missing flags")
	}
}

func TestUnknownCommand(t *testing.T) {
	_, stderr, err := runCLI("unknown-command")

	if err == nil {
		t.Error("Expected error for unknown command")
	}

	if stderr == "" {
		t.Error("Expected error message for unknown command")
	}
}

// -------- status command integration tests --------

// fixtureManifestPath returns the path to the on-disk status fixture.
func fixtureManifestPath(t *testing.T) string {
	t.Helper()
	// Resolve relative to the source tree root (two levels up from cmd/cascade/).
	p, err := filepath.Abs("../../testdata/status/manifest.yaml")
	if err != nil {
		t.Fatalf("resolving fixture path: %v", err)
	}
	return p
}

func TestStatusCommand_AllEnvs(t *testing.T) {
	manifest := fixtureManifestPath(t)
	stdout, stderr, err := runCLI("status", "--config", manifest)
	if err != nil {
		t.Fatalf("status command failed: %v\nstderr: %s", err, stderr)
	}
	if !contains(stdout, "environment: dev") {
		t.Errorf("expected 'environment: dev' in output, got:\n%s", stdout)
	}
	if !contains(stdout, "v1.2.3-rc.1") {
		t.Errorf("expected version in output, got:\n%s", stdout)
	}
	if !contains(stdout, "latest_release:") {
		t.Errorf("expected latest_release section, got:\n%s", stdout)
	}
}

func TestStatusCommand_AllEnvs_JSON(t *testing.T) {
	manifest := fixtureManifestPath(t)
	stdout, stderr, err := runCLI("status", "--config", manifest, "--json")
	if err != nil {
		t.Fatalf("status --json failed: %v\nstderr: %s", err, stderr)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON parse error: %v\noutput: %s", err, stdout)
	}
	envs, ok := result["environments"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected 'environments' map in JSON output")
	}
	if _, ok := envs["dev"]; !ok {
		t.Error("expected 'dev' in environments")
	}
	if _, ok := result["latest_release"]; !ok {
		t.Error("expected 'latest_release' in JSON output")
	}
}

func TestStatusCommand_EnvSubcommand(t *testing.T) {
	manifest := fixtureManifestPath(t)
	stdout, stderr, err := runCLI("status", "env", "staging", "--config", manifest)
	if err != nil {
		t.Fatalf("status env failed: %v\nstderr: %s", err, stderr)
	}
	if !contains(stdout, "111aaa") {
		t.Errorf("expected sha in output, got:\n%s", stdout)
	}
	if !contains(stdout, "v1.2.2") {
		t.Errorf("expected version in output, got:\n%s", stdout)
	}
}

func TestStatusCommand_EnvSubcommand_JSON(t *testing.T) {
	manifest := fixtureManifestPath(t)
	stdout, stderr, err := runCLI("status", "env", "dev", "--config", manifest, "--json")
	if err != nil {
		t.Fatalf("status env --json failed: %v\nstderr: %s", err, stderr)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON parse error: %v\noutput: %s", err, stdout)
	}
	if result["environment"] != "dev" {
		t.Errorf("expected environment=dev, got %v", result["environment"])
	}
}

func TestStatusCommand_BuildSubcommand(t *testing.T) {
	manifest := fixtureManifestPath(t)
	stdout, stderr, err := runCLI("status", "build", "app", "--env", "dev", "--config", manifest)
	if err != nil {
		t.Fatalf("status build failed: %v\nstderr: %s", err, stderr)
	}
	if !contains(stdout, "sha256:aabbcc") {
		t.Errorf("expected artifact_id in output, got:\n%s", stdout)
	}
}

func TestStatusCommand_BuildSubcommand_JSON(t *testing.T) {
	manifest := fixtureManifestPath(t)
	stdout, stderr, err := runCLI("status", "build", "app", "--env", "dev", "--config", manifest, "--json")
	if err != nil {
		t.Fatalf("status build --json failed: %v\nstderr: %s", err, stderr)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON parse error: %v\noutput: %s", err, stdout)
	}
	if result["build"] != "app" {
		t.Errorf("expected build=app, got %v", result["build"])
	}
	state, ok := result["state"].(map[string]interface{})
	if !ok {
		t.Fatal("expected state map in JSON")
	}
	if state["artifact_id"] != "sha256:aabbcc" {
		t.Errorf("unexpected artifact_id: %v", state["artifact_id"])
	}
}

func TestStatusCommand_DeploySubcommand(t *testing.T) {
	manifest := fixtureManifestPath(t)
	stdout, stderr, err := runCLI("status", "deploy", "services", "--env", "dev", "--config", manifest)
	if err != nil {
		t.Fatalf("status deploy failed: %v\nstderr: %s", err, stderr)
	}
	if !contains(stdout, "2026-01-01T10:05:00Z") {
		t.Errorf("expected deployed_at in output, got:\n%s", stdout)
	}
}

func TestStatusCommand_DeploySubcommand_JSON(t *testing.T) {
	manifest := fixtureManifestPath(t)
	stdout, stderr, err := runCLI("status", "deploy", "services", "--env", "dev", "--config", manifest, "--json")
	if err != nil {
		t.Fatalf("status deploy --json failed: %v\nstderr: %s", err, stderr)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON parse error: %v\noutput: %s", err, stdout)
	}
	if result["deploy"] != "services" {
		t.Errorf("expected deploy=services, got %v", result["deploy"])
	}
}

func TestStatusCommand_EmptyState_NoPanic(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "manifest.yaml")
	content := "ci:\n  config:\n    trunk_branch: main\n    environments:\n      - dev\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	stdout, stderr, err := runCLI("status", "--config", path)
	if err != nil {
		t.Fatalf("status on empty state failed: %v\nstderr: %s", err, stderr)
	}
	if !contains(stdout, "dev") {
		t.Errorf("expected 'dev' in output, got:\n%s", stdout)
	}
}

func TestStatusCommand_MissingManifest(t *testing.T) {
	_, _, err := runCLI("status", "--config", "/no/such/manifest.yaml")
	if err == nil {
		t.Error("expected error for missing manifest file")
	}
}

func TestStatusCommand_BuildMissingEnvFlag(t *testing.T) {
	manifest := fixtureManifestPath(t)
	_, stderr, err := runCLI("status", "build", "app", "--config", manifest)
	if err == nil {
		t.Error("expected error when --env flag is missing")
	}
	if !contains(stderr, "env") {
		t.Errorf("expected --env mentioned in error, got: %s", stderr)
	}
}

func TestStatusCommand_DeployMissingEnvFlag(t *testing.T) {
	manifest := fixtureManifestPath(t)
	_, stderr, err := runCLI("status", "deploy", "services", "--config", manifest)
	if err == nil {
		t.Error("expected error when --env flag is missing")
	}
	if !contains(stderr, "env") {
		t.Errorf("expected --env mentioned in error, got: %s", stderr)
	}
}

// gitRun runs a git command in dir and fails the test on error. It is used to
// stand up a throwaway repository with an origin remote so the consistency
// command's branch lister has refs/remotes/origin/* to read.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// consistencyRepo builds a temp work repo wired to a bare origin remote that
// carries an orphan env/<name> branch (a branch with no matching divergence in
// the manifest). It returns the work-tree path and the manifest path inside it.
// The manifest records only an undiverged dev env, so env/orphan is surfaced as
// an orphan integration branch.
func consistencyRepo(t *testing.T) (workDir, manifestPath string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")

	gitRun(t, root, "init", "--bare", "-q", bare)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	gitRun(t, work, "init", "-q")
	gitRun(t, work, "config", "user.email", "test@example.com")
	gitRun(t, work, "config", "user.name", "test")
	gitRun(t, work, "config", "commit.gpgsign", "false")
	gitRun(t, work, "checkout", "-q", "-b", "main")

	manifestPath = filepath.Join(work, "manifest.yaml")
	content := "ci:\n  config:\n    environments: [dev]\n  state:\n    dev:\n      sha: trunkhead\n"
	if err := os.WriteFile(manifestPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	gitRun(t, work, "add", "manifest.yaml")
	gitRun(t, work, "commit", "-q", "-m", "init")
	gitRun(t, work, "remote", "add", "origin", bare)
	gitRun(t, work, "push", "-q", "origin", "main")
	gitRun(t, work, "checkout", "-q", "-b", "env/orphan")
	gitRun(t, work, "push", "-q", "origin", "env/orphan")
	gitRun(t, work, "checkout", "-q", "main")
	gitRun(t, work, "fetch", "-q", "origin")

	return work, manifestPath
}

func TestStatusCommand_Consistency_JSON(t *testing.T) {
	work, manifest := consistencyRepo(t)

	stdout, stderr, err := runCLIIn(work, "status", "consistency", "--config", manifest, "--json")
	if err != nil {
		t.Fatalf("status consistency --json failed: %v\nstderr: %s", err, stderr)
	}

	var result struct {
		OrphanEnvBranches []string `json:"orphan_env_branches"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON parse error: %v\noutput: %s", err, stdout)
	}
	if len(result.OrphanEnvBranches) != 1 || result.OrphanEnvBranches[0] != "env/orphan" {
		t.Errorf("expected [env/orphan], got %v", result.OrphanEnvBranches)
	}
}

func TestStatusCommand_Consistency_HumanReadable(t *testing.T) {
	work, manifest := consistencyRepo(t)

	stdout, stderr, err := runCLIIn(work, "status", "consistency", "--config", manifest)
	if err != nil {
		t.Fatalf("status consistency failed: %v\nstderr: %s", err, stderr)
	}
	if !contains(stdout, "env/orphan") {
		t.Errorf("expected orphan branch in output, got:\n%s", stdout)
	}
}

// -------- generate-workflow flag coverage --------

// genFixtureManifest writes a multi-environment manifest under <tmpDir>/.github/
// manifest.yaml and returns its path. The generator resolves output paths
// relative to the .github parent (the repo root), so callers can assert on
// files written under tmpDir.
func genFixtureManifest(t *testing.T, tmpDir string) string {
	t.Helper()
	githubDir := filepath.Join(tmpDir, ".github")
	if err := os.MkdirAll(githubDir, 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
        triggers:
          - "src/**"
    deploys:
      - name: services
        workflow: .github/workflows/deploy.yaml
        triggers:
          - "deploy/**"
        depends_on:
          - app
`
	path := filepath.Join(githubDir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// The orchestrate generator reads each referenced build/deploy workflow to
	// discover its job outputs, so the files the manifest points at must exist.
	workflowsDir := filepath.Join(githubDir, "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	stub := `name: stub
on:
  workflow_call:
jobs:
  run:
    runs-on: ubuntu-latest
    steps:
      - run: "true"
`
	for _, name := range []string{"build.yaml", "deploy.yaml"} {
		if err := os.WriteFile(filepath.Join(workflowsDir, name), []byte(stub), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return path
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestGenerateWorkflow_Output_WritesCustomOrchestratePath(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := genFixtureManifest(t, tmpDir)
	customPath := filepath.Join(tmpDir, ".github", "workflows", "custom-orchestrate.yaml")
	defaultPath := filepath.Join(tmpDir, ".github", "workflows", "orchestrate.yaml")

	// Run in tmpDir so the non-overridden default-path artifacts (promote,
	// hotfix, rollback) land under tmpDir rather than polluting the source tree.
	_, stderr, err := runCLIIn(tmpDir, "generate-workflow", "--config", manifest, "--output", customPath, "--force")
	if err != nil {
		t.Fatalf("generate-workflow --output failed: %v\nstderr: %s", err, stderr)
	}

	if !fileExists(t, customPath) {
		t.Errorf("expected custom orchestrate file at %s", customPath)
	}
	body, rerr := os.ReadFile(customPath)
	if rerr != nil {
		t.Fatalf("reading custom output: %v", rerr)
	}
	if !contains(string(body), "on:") || !contains(string(body), "name:") {
		t.Errorf("custom orchestrate file lacks workflow content, got:\n%s", body)
	}
	if fileExists(t, defaultPath) {
		t.Errorf("orchestrate should not have been written to the default path %s", defaultPath)
	}
}

func TestGenerateWorkflow_PromoteOutput_WritesCustomPromotePath(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := genFixtureManifest(t, tmpDir)
	customPromote := filepath.Join(tmpDir, ".github", "workflows", "custom-promote.yaml")
	defaultPromote := filepath.Join(tmpDir, ".github", "workflows", "promote.yaml")

	// Run in tmpDir so non-overridden default-path artifacts land under tmpDir.
	_, stderr, err := runCLIIn(tmpDir, "generate-workflow", "--config", manifest, "--promote-output", customPromote, "--force")
	if err != nil {
		t.Fatalf("generate-workflow --promote-output failed: %v\nstderr: %s", err, stderr)
	}

	if !fileExists(t, customPromote) {
		t.Errorf("expected custom promote file at %s", customPromote)
	}
	body, rerr := os.ReadFile(customPromote)
	if rerr != nil {
		t.Fatalf("reading custom promote output: %v", rerr)
	}
	if !contains(string(body), "on:") {
		t.Errorf("custom promote file lacks workflow content, got:\n%s", body)
	}
	if fileExists(t, defaultPromote) {
		t.Errorf("promote should not have been written to the default path %s", defaultPromote)
	}
}

func TestGenerateWorkflow_OrchestrateOnly_OmitsPromote(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := genFixtureManifest(t, tmpDir)
	// Anchor both outputs to absolute paths under tmpDir; the default flag
	// values are relative to the process working directory, not the manifest.
	orchestrate := filepath.Join(tmpDir, ".github", "workflows", "orchestrate.yaml")
	promote := filepath.Join(tmpDir, ".github", "workflows", "promote.yaml")

	// Run in tmpDir so non-overridden default-path artifacts land under tmpDir.
	_, stderr, err := runCLIIn(tmpDir, "generate-workflow", "--config", manifest,
		"--output", orchestrate, "--promote-output", promote,
		"--orchestrate-only", "--force")
	if err != nil {
		t.Fatalf("generate-workflow --orchestrate-only failed: %v\nstderr: %s", err, stderr)
	}

	if !fileExists(t, orchestrate) {
		t.Errorf("expected orchestrate file at %s", orchestrate)
	}
	if fileExists(t, promote) {
		t.Errorf("promote file should not exist with --orchestrate-only, found %s", promote)
	}
}

func TestGenerateWorkflow_PromoteOnly_OmitsOrchestrate(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := genFixtureManifest(t, tmpDir)
	orchestrate := filepath.Join(tmpDir, ".github", "workflows", "orchestrate.yaml")
	promote := filepath.Join(tmpDir, ".github", "workflows", "promote.yaml")

	// Run in tmpDir so non-overridden default-path artifacts land under tmpDir.
	_, stderr, err := runCLIIn(tmpDir, "generate-workflow", "--config", manifest,
		"--output", orchestrate, "--promote-output", promote,
		"--promote-only", "--force")
	if err != nil {
		t.Fatalf("generate-workflow --promote-only failed: %v\nstderr: %s", err, stderr)
	}

	if !fileExists(t, promote) {
		t.Errorf("expected promote file at %s", promote)
	}
	if fileExists(t, orchestrate) {
		t.Errorf("orchestrate file should not exist with --promote-only, found %s", orchestrate)
	}
}

func TestGenerateWorkflow_ActionFolder_NamesCompositeActionDir(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := genFixtureManifest(t, tmpDir)
	customAction := filepath.Join(tmpDir, ".github", "actions", "custom-release-action", "action.yaml")
	defaultAction := filepath.Join(tmpDir, ".github", "actions", "manage-release", "action.yaml")

	// Run in tmpDir so the default-path workflow artifacts land under tmpDir.
	_, stderr, err := runCLIIn(tmpDir, "generate-workflow", "--config", manifest, "--action-folder", "custom-release-action", "--force")
	if err != nil {
		t.Fatalf("generate-workflow --action-folder failed: %v\nstderr: %s", err, stderr)
	}

	if !fileExists(t, customAction) {
		t.Errorf("expected composite action at %s", customAction)
	}
	if fileExists(t, defaultAction) {
		t.Errorf("default action folder should not exist with --action-folder, found %s", defaultAction)
	}
}

// -------- version command format --------

func TestVersionCommand_ContainsVersionString(t *testing.T) {
	stdout, _, err := runCLI("version")
	if err != nil {
		t.Fatalf("version command failed: %v", err)
	}
	if !contains(stdout, "cascade") {
		t.Errorf("expected version output to contain \"cascade\", got: %q", stdout)
	}
}

// -------- schema command --------

func TestSchemaCommand_EmitsEmbeddedJSONSchema(t *testing.T) {
	stdout, stderr, err := runCLI("schema")
	if err != nil {
		t.Fatalf("schema command failed: %v\nstderr: %s", err, stderr)
	}
	if stdout == "" {
		t.Fatal("schema command returned empty output")
	}
	var doc map[string]interface{}
	if uerr := json.Unmarshal([]byte(stdout), &doc); uerr != nil {
		t.Fatalf("schema output is not valid JSON: %v\noutput: %s", uerr, stdout)
	}
	if _, ok := doc["$schema"]; !ok {
		t.Errorf("expected \"$schema\" key in JSON Schema output, keys: %v", keysOf(doc))
	}
}

// -------- branch-protection command --------

func TestBranchProtectionCommand_EmitsProtectionAndOperatorTodo(t *testing.T) {
	manifest := genFixtureManifest(t, t.TempDir())

	stdout, stderr, err := runCLI("branch-protection", "--config", manifest)
	if err != nil {
		t.Fatalf("branch-protection command failed: %v\nstderr: %s", err, stderr)
	}
	var doc map[string]interface{}
	if uerr := json.Unmarshal([]byte(stdout), &doc); uerr != nil {
		t.Fatalf("branch-protection output is not valid JSON: %v\noutput: %s", uerr, stdout)
	}
	if _, ok := doc["protection"]; !ok {
		t.Errorf("expected \"protection\" key, keys: %v", keysOf(doc))
	}
	if _, ok := doc["operator_todo"]; !ok {
		t.Errorf("expected \"operator_todo\" key, keys: %v", keysOf(doc))
	}
}

// -------- verify --quiet --------

func TestVerifyCommand_QuietClean_NoStdoutExitZero(t *testing.T) {
	tmpDir := t.TempDir()
	genFixtureManifest(t, tmpDir)
	// Run both commands with the working directory at tmpDir so the generator's
	// relative default output paths (orchestrate, promote, hotfix, rollback, the
	// composite action) and verify's planned paths resolve to the same files.
	manifestRel := filepath.Join(".github", "manifest.yaml")

	if _, stderr, err := runCLIIn(tmpDir, "generate-workflow", "--config", manifestRel, "--force"); err != nil {
		t.Fatalf("seed generate failed: %v\nstderr: %s", err, stderr)
	}

	stdout, stderr, err := runCLIIn(tmpDir, "verify", "--config", manifestRel, "--quiet")
	if err != nil {
		t.Fatalf("verify --quiet on clean tree should exit 0: %v\nstderr: %s", err, stderr)
	}
	// Quiet suppresses the "verify: N files, no drift" body on a clean tree.
	if stdout != "" {
		t.Errorf("expected no stdout in quiet clean mode, got: %q", stdout)
	}
}

func TestVerifyCommand_QuietDrift_NoReportBodyExitNonZero(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := genFixtureManifest(t, tmpDir)

	// No workflows generated, so every planned file is missing => drift.
	stdout, stderr, err := runCLI("verify", "--config", manifest, "--quiet")
	if err == nil {
		t.Fatal("verify --quiet with drift should exit non-zero")
	}
	if stdout != "" {
		t.Errorf("expected no stdout in quiet drift mode, got: %q", stdout)
	}
	// Quiet suppresses verify's per-file drift report and the drift summary
	// line. The process still surfaces the wrapped error from main, but none of
	// the report body should appear.
	if contains(stderr, "(missing)") {
		t.Errorf("expected per-file drift report suppressed in quiet mode, got: %q", stderr)
	}
	if contains(stderr, "file(s) drifted") {
		t.Errorf("expected drift summary suppressed in quiet mode, got: %q", stderr)
	}
}

// keysOf returns the top-level keys of a decoded JSON object, for diagnostics.
func keysOf(m map[string]interface{}) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

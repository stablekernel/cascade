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

func TestLintCommand(t *testing.T) {
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

	stdout, stderr, err := runCLI("lint", "--json", "--config", configPath)
	if err != nil {
		t.Fatalf("lint --json command failed: %v\nstderr: %s", err, stderr)
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

func TestLintCommand_InvalidConfig(t *testing.T) {
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

	stdout, _, err := runCLI("lint", "--json", "--config", configPath)
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

func TestLintCommand_FileNotFound(t *testing.T) {
	stdout, _, err := runCLI("lint", "--json", "--config", "/nonexistent/path/config.yaml")

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

// -------- graph command integration tests --------

// graphManifestContent is a two-environment manifest with a validate gate, one
// build, and a deploy that depends on the build, so the rendered graph carries
// every node kind and a hard edge.
const graphManifestContent = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    validate:
      workflow: .github/workflows/validate.yaml
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
        triggers:
          - "src/**"
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
        triggers:
          - "src/**"
        depends_on:
          - app
`

func writeGraphManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(graphManifestContent), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}
	return path
}

func TestGraphCommand_EmitsMermaid(t *testing.T) {
	manifest := writeGraphManifest(t)
	stdout, stderr, err := runCLI("graph", "--config", manifest)
	if err != nil {
		t.Fatalf("graph command failed: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{"flowchart TD", "validate", "build_app", "deploy_app", "deploy_app --> build_app"} {
		if !contains(stdout, want) {
			t.Errorf("expected %q in graph output, got:\n%s", want, stdout)
		}
	}
}

func TestGraphCommand_JSONWrapsDiagram(t *testing.T) {
	manifest := writeGraphManifest(t)
	stdout, stderr, err := runCLI("graph", "--config", manifest, "--json")
	if err != nil {
		t.Fatalf("graph --json command failed: %v\nstderr: %s", err, stderr)
	}
	var payload struct {
		Format  string `json:"format"`
		Diagram string `json:"diagram"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\noutput: %s", err, stdout)
	}
	if payload.Format != "mermaid" {
		t.Errorf("expected format mermaid, got %q", payload.Format)
	}
	if !contains(payload.Diagram, "flowchart TD") {
		t.Errorf("expected diagram in JSON envelope, got: %s", payload.Diagram)
	}
}

func TestGraphCommand_RejectsUnknownFormat(t *testing.T) {
	manifest := writeGraphManifest(t)
	_, stderr, err := runCLI("graph", "--config", manifest, "--format", "svg")
	if err == nil {
		t.Error("Expected non-zero exit for unknown format")
	}
	if !contains(stderr, "mermaid") {
		t.Errorf("expected error mentioning mermaid, got: %s", stderr)
	}
}

func TestGraphCommand_MissingManifest(t *testing.T) {
	_, _, err := runCLI("graph", "--config", "/nonexistent/path/manifest.yaml")
	if err == nil {
		t.Error("Expected non-zero exit for missing manifest")
	}
}

// graphEnvManifestContent is a two-environment manifest whose dev env state
// tracks a hotfix integration branch, so the env projection must render the
// promotion state machine with a divergence branch that rejoins at prod.
const graphEnvManifestContent = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
  state:
    dev:
      ref: hotfix/patch
    prod: {}
`

func writeGraphEnvManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(graphEnvManifestContent), 0644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}
	return path
}

func TestGraphCommand_EnvGranularityEmitsStateMachine(t *testing.T) {
	manifest := writeGraphEnvManifest(t)
	stdout, stderr, err := runCLI("graph", "--config", manifest, "--granularity", "env")
	if err != nil {
		t.Fatalf("graph --granularity env failed: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{
		"stateDiagram-v2",
		"[*] --> dev",
		"dev --> prod : promote",
		"dev --> dev_hotfix : diverge",
		"dev_hotfix --> prod : rejoin",
	} {
		if !contains(stdout, want) {
			t.Errorf("expected %q in env projection, got:\n%s", want, stdout)
		}
	}
}

func TestGraphCommand_StagesGranularityEmitsFlowchart(t *testing.T) {
	manifest := writeGraphManifest(t)
	stdout, stderr, err := runCLI("graph", "--config", manifest, "--granularity", "stages")
	if err != nil {
		t.Fatalf("graph --granularity stages failed: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{"flowchart TD", "trunk", "build", "deploy", "promote", "release"} {
		if !contains(stdout, want) {
			t.Errorf("expected %q in stages projection, got:\n%s", want, stdout)
		}
	}
	// The stage rollup must not carry per-callback job nodes.
	if contains(stdout, "build_app") || contains(stdout, "deploy_app") {
		t.Errorf("stages projection leaked per-callback nodes:\n%s", stdout)
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

// -------- graph theme integration tests --------

func TestGraphCommand_BlandThemeStylesNodes(t *testing.T) {
	manifest := writeGraphManifest(t)
	stdout, stderr, err := runCLI("graph", "--config", manifest, "--theme", "bland")
	if err != nil {
		t.Fatalf("graph --theme bland failed: %v\nstderr: %s", err, stderr)
	}
	// The bland palette is monotone: a neutral Mermaid base, a muted gray edge
	// color, and grayscale node fills. These tokens are absent from the branded
	// cascade default, so their presence proves the bland theme reached the
	// emitter rather than the default palette.
	for _, want := range []string{`"theme":"neutral"`, "#8c959f", "#f6f8fa"} {
		if !contains(stdout, want) {
			t.Errorf("expected bland token %q in output, got:\n%s", want, stdout)
		}
	}
	// The branded cascade accent must not appear under the bland theme.
	if contains(stdout, "#1f6feb") {
		t.Errorf("cascade accent leaked into bland render:\n%s", stdout)
	}
}

func TestGraphCommand_CustomThemeFileStylesNodes(t *testing.T) {
	manifest := writeGraphManifest(t)
	themePath := filepath.Join(t.TempDir(), "custom.json")
	themeBody := `{
  "name": "midnight",
  "base": "dark",
  "lineColor": "#abcdef",
  "nodeStyles": {
    "build": {"fill": "#123456", "stroke": "#012345", "text": "#fefefe"}
  }
}`
	if err := os.WriteFile(themePath, []byte(themeBody), 0o644); err != nil {
		t.Fatalf("write theme file: %v", err)
	}

	stdout, stderr, err := runCLI("graph", "--config", manifest, "--theme", themePath, "--json")
	if err != nil {
		t.Fatalf("graph --theme <file> failed: %v\nstderr: %s", err, stderr)
	}
	var payload struct {
		Theme   string `json:"theme"`
		Diagram string `json:"diagram"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\noutput: %s", err, stdout)
	}
	if payload.Theme != "midnight" {
		t.Errorf("expected theme name midnight, got %q", payload.Theme)
	}
	for _, want := range []string{`"theme":"dark"`, "#abcdef", "fill:#123456"} {
		if !contains(payload.Diagram, want) {
			t.Errorf("expected custom theme token %q in diagram, got:\n%s", want, payload.Diagram)
		}
	}
}

func TestGraphCommand_MalformedThemeFileErrors(t *testing.T) {
	manifest := writeGraphManifest(t)
	themePath := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(themePath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write theme file: %v", err)
	}

	_, stderr, err := runCLI("graph", "--config", manifest, "--theme", themePath)
	if err == nil {
		t.Error("expected non-zero exit for malformed theme file")
	}
	if !contains(stderr, "loading theme") {
		t.Errorf("expected wrapped 'loading theme' error, got: %s", stderr)
	}
}

// -------- simulate command integration tests --------

// simulatePromoteManifest seeds dev with a concrete sha and version and leaves
// uat empty, with prod present so the dev-to-uat crossing is a normal sequential
// deploy rather than the publish boundary. It declares one build and one deploy
// callback so a simulated promotion drives the stubbed deploy-result gating.
const simulatePromoteManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - uat
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
  state:
    dev:
      sha: a1b2c3d4e5f6
      version: v1.2.0-rc.1
      committed_at: "2026-01-01T10:00:00Z"
      committed_by: seed-user
    uat: {}
`

// simulateRollbackManifest holds a prod env with a current state and a single
// distinct prior snapshot in the deploy-history ring, so a rollback resolves the
// previous-ring target and reverts to it.
const simulateRollbackManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - uat
      - prod
  state:
    prod:
      sha: newsha0000000
      version: v2.0.0
      committed_at: "2026-02-01T10:00:00Z"
      committed_by: seed-user
      previous:
        - sha: oldsha0000000
          version: v1.0.0
          committed_at: "2026-01-01T10:00:00Z"
          committed_by: seed-user
`

func writeSimulateManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write manifest: %v", err)
	}
	return path
}

// simulateJSON is the subset of the simulate result envelope the CLI tests
// assert against. It mirrors the json tags on internal/simulate.Result.
type simulateJSON struct {
	Action   string `json:"action"`
	Describe string `json:"describe"`
	Diff     struct {
		Envs []struct {
			Environment string `json:"environment"`
			Version     struct {
				From    string `json:"from"`
				To      string `json:"to"`
				Changed bool   `json:"changed"`
			} `json:"version"`
			SHA struct {
				From    string `json:"from"`
				To      string `json:"to"`
				Changed bool   `json:"changed"`
			} `json:"sha"`
		} `json:"envs"`
	} `json:"diff"`
	Effects []struct {
		Disposition string `json:"disposition"`
		Action      string `json:"action"`
		Target      string `json:"target"`
		Detail      string `json:"detail"`
	} `json:"effects"`
	Note string `json:"note"`
}

func TestSimulateCommand_PromoteEmitsDiffAndEffects(t *testing.T) {
	manifest := writeSimulateManifest(t, simulatePromoteManifest)
	stdout, stderr, err := runCLI("simulate", "promote", "--config", manifest)
	if err != nil {
		t.Fatalf("simulate promote failed: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{
		"Simulating: promote (mode=default)",
		"State diff:",
		"uat:",
		"version:  (none) -> v1.2.0-rc.1",
		"sha:      (none) -> a1b2c3d",
		"Effects (in order):",
		"write state",
		// The orchestration-not-deploys note must accompany the effect sequence.
		"build and deploy results are simulated",
	} {
		if !contains(stdout, want) {
			t.Errorf("expected %q in simulate output, got:\n%s", want, stdout)
		}
	}
}

func TestSimulateCommand_PromoteJSONMatchesHumanRender(t *testing.T) {
	manifest := writeSimulateManifest(t, simulatePromoteManifest)

	human, stderr, err := runCLI("simulate", "promote", "--config", manifest)
	if err != nil {
		t.Fatalf("simulate promote failed: %v\nstderr: %s", err, stderr)
	}

	jsonOut, stderr, err := runCLI("simulate", "promote", "--config", manifest, "--json")
	if err != nil {
		t.Fatalf("simulate promote --json failed: %v\nstderr: %s", err, stderr)
	}

	var env simulateJSON
	if err := json.Unmarshal([]byte(jsonOut), &env); err != nil {
		t.Fatalf("Failed to parse JSON envelope: %v\noutput: %s", err, jsonOut)
	}
	if env.Action != "promote" {
		t.Errorf("expected action promote, got %q", env.Action)
	}
	if env.Describe == "" {
		t.Error("expected non-empty describe in JSON envelope")
	}
	if len(env.Diff.Envs) == 0 {
		t.Fatalf("expected at least one env in the diff, got none")
	}
	if len(env.Effects) == 0 {
		t.Fatalf("expected at least one effect, got none")
	}

	// The JSON envelope must agree with the human render: the uat sha the diff
	// advances to in JSON is the same string the human output prints.
	var uatTo string
	for _, e := range env.Diff.Envs {
		if e.Environment == "uat" {
			uatTo = e.SHA.To
		}
	}
	if uatTo != "a1b2c3d4e5f6" {
		t.Errorf("expected uat sha.to a1b2c3d4e5f6 in JSON, got %q", uatTo)
	}
	// The human render shortens shas to 7 characters, so it carries the 7-char
	// prefix of the full sha the JSON envelope reports.
	shortUATTo := uatTo[:7]
	if !contains(human, shortUATTo) {
		t.Errorf("human render is missing the shortened JSON-reported sha %q:\n%s", shortUATTo, human)
	}
	if env.Note == "" || !contains(human, "build and deploy results are simulated") {
		t.Errorf("note should appear in both renders; json note=%q", env.Note)
	}
}

func TestSimulateCommand_DeployResultGatesFinalize(t *testing.T) {
	manifest := writeSimulateManifest(t, simulatePromoteManifest)
	stdout, stderr, err := runCLI("simulate", "promote", "--config", manifest,
		"--deploy-result", "services=failure", "--json")
	if err != nil {
		t.Fatalf("simulate promote --deploy-result failed: %v\nstderr: %s", err, stderr)
	}

	var env simulateJSON
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("Failed to parse JSON envelope: %v\noutput: %s", err, stdout)
	}

	var (
		sawFailedDeploy bool
		gatedFinalize   bool
	)
	for _, e := range env.Effects {
		if e.Action == "deploy" && e.Target == "services" {
			sawFailedDeploy = contains(e.Detail, "simulated failure")
		}
		if e.Action == "write state" {
			gatedFinalize = e.Disposition == "gate"
		}
	}
	if !sawFailedDeploy {
		t.Errorf("expected the services deploy to record a simulated failure, effects: %+v", env.Effects)
	}
	if !gatedFinalize {
		t.Errorf("expected a failed deploy to gate the write-state finalize, effects: %+v", env.Effects)
	}
}

func TestSimulateCommand_RollbackEmitsRevert(t *testing.T) {
	manifest := writeSimulateManifest(t, simulateRollbackManifest)
	stdout, stderr, err := runCLI("simulate", "rollback", "--env", "prod", "--config", manifest)
	if err != nil {
		t.Fatalf("simulate rollback failed: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{
		"Simulating: rollback",
		"prod:",
		"newsha0 -> oldsha0",
		"v2.0.0 -> v1.0.0",
		"revert",
		"write state",
	} {
		if !contains(stdout, want) {
			t.Errorf("expected %q in rollback output, got:\n%s", want, stdout)
		}
	}
}

func TestSimulateCommand_RollbackMissingEnv(t *testing.T) {
	manifest := writeSimulateManifest(t, simulateRollbackManifest)
	_, stderr, err := runCLI("simulate", "rollback", "--config", manifest)
	if err == nil {
		t.Error("expected non-zero exit when --env is missing")
	}
	if !contains(stderr, "env") {
		t.Errorf("expected error mentioning env, got: %s", stderr)
	}
}

func TestSimulateCommand_HotfixMissingEnv(t *testing.T) {
	manifest := writeSimulateManifest(t, simulatePromoteManifest)
	_, stderr, err := runCLI("simulate", "hotfix", "--config", manifest)
	if err == nil {
		t.Error("expected non-zero exit when --env is missing")
	}
	if !contains(stderr, "env") {
		t.Errorf("expected error mentioning env, got: %s", stderr)
	}
}

// simulateMonorepoManifest records state component-scoped under
// state.components.<comp>.<env>, the layout a multi-component repo uses. Component
// api and component web each seed dev with a distinct sha and leave staging empty,
// so a component-scoped promotion advances that component's dev into staging and
// must never surface the sibling component's rows. This is the end-to-end fixture
// for the simulator's monorepo support.
const simulateMonorepoManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - staging
      - prod
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-v
      web:
        path: services/web
        tag_grammar:
          prefix: web-v
  state:
    components:
      api:
        dev:
          sha: apidevsha0001
          version: api-v1.2.0-rc.1
          committed_at: "2026-01-01T10:00:00Z"
          committed_by: seed-user
        staging: {}
      web:
        dev:
          sha: webdevsha0001
          version: web-v3.0.0-rc.1
          committed_at: "2026-01-01T10:00:00Z"
          committed_by: seed-user
        staging: {}
`

// simulateMonorepoRollbackManifest seeds component api's prod with a current state
// and a single distinct prior snapshot in the deploy-history ring, so a
// component-scoped rollback resolves the previous-ring target and reverts to it.
const simulateMonorepoRollbackManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - staging
      - prod
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-v
      web:
        path: services/web
        tag_grammar:
          prefix: web-v
  state:
    components:
      api:
        prod:
          sha: apicur1000001
          version: api-v2.0.0
          committed_at: "2026-02-01T10:00:00Z"
          committed_by: seed-user
          previous:
            - sha: apiold1000001
              version: api-v1.0.0
              committed_at: "2026-01-01T10:00:00Z"
              committed_by: seed-user
      web:
        prod:
          sha: webprodnew001
          version: web-v9.0.0
          committed_at: "2026-02-01T10:00:00Z"
          committed_by: seed-user
`

func TestSimulateCommand_MonorepoPromoteScopedToComponent(t *testing.T) {
	manifest := writeSimulateManifest(t, simulateMonorepoManifest)
	stdout, stderr, err := runCLI("simulate", "promote", "--component", "api", "--config", manifest)
	if err != nil {
		t.Fatalf("simulate promote --component api failed: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{
		"staging:",
		"version:  (none) -> api-v1.2.0-rc.1",
		"sha:      (none) -> apidevs",
		"write state",
	} {
		if !contains(stdout, want) {
			t.Errorf("expected %q in api-scoped promote output, got:\n%s", want, stdout)
		}
	}
	// The sibling component's rows must never leak into an api-scoped simulation.
	if contains(stdout, "webdevs") {
		t.Errorf("api-scoped simulation leaked web's sha:\n%s", stdout)
	}
}

func TestSimulateCommand_MonorepoPromoteIsolatesSibling(t *testing.T) {
	manifest := writeSimulateManifest(t, simulateMonorepoManifest)
	stdout, stderr, err := runCLI("simulate", "promote", "--component", "web", "--config", manifest)
	if err != nil {
		t.Fatalf("simulate promote --component web failed: %v\nstderr: %s", err, stderr)
	}
	if !contains(stdout, "webdevs") {
		t.Errorf("web-scoped simulation should carry web's sha, got:\n%s", stdout)
	}
	if contains(stdout, "apidevs") {
		t.Errorf("web-scoped simulation leaked api's sha:\n%s", stdout)
	}
}

func TestSimulateCommand_MonorepoRequiresComponent(t *testing.T) {
	manifest := writeSimulateManifest(t, simulateMonorepoManifest)
	_, stderr, err := runCLI("simulate", "promote", "--config", manifest)
	if err == nil {
		t.Error("expected non-zero exit when --component is omitted on a monorepo manifest")
	}
	if !contains(stderr, "--component") {
		t.Errorf("expected error directing the user to pass --component, got: %s", stderr)
	}
}

func TestSimulateCommand_MonorepoUnknownComponent(t *testing.T) {
	manifest := writeSimulateManifest(t, simulateMonorepoManifest)
	_, stderr, err := runCLI("simulate", "promote", "--component", "billing", "--config", manifest)
	if err == nil {
		t.Error("expected non-zero exit for an undeclared component")
	}
	if !contains(stderr, "unknown component") {
		t.Errorf("expected an unknown-component error, got: %s", stderr)
	}
}

func TestSimulateCommand_MonorepoRollbackScopedToComponent(t *testing.T) {
	manifest := writeSimulateManifest(t, simulateMonorepoRollbackManifest)
	stdout, stderr, err := runCLI("simulate", "rollback", "--component", "api", "--env", "prod", "--config", manifest)
	if err != nil {
		t.Fatalf("simulate rollback --component api failed: %v\nstderr: %s", err, stderr)
	}
	for _, want := range []string{
		"prod:",
		"apicur1 -> apiold1",
		"api-v2.0.0 -> api-v1.0.0",
		"revert",
	} {
		if !contains(stdout, want) {
			t.Errorf("expected %q in api-scoped rollback output, got:\n%s", want, stdout)
		}
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

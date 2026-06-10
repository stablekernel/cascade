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

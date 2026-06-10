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

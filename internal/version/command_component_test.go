package version

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// componentRepo initializes a git repo with a two-component manifest and both
// components tagged at a shared v1.0.0 base, changes into it for the test, and
// returns the config path.
func componentRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if cerr := os.Chdir(orig); cerr != nil {
			t.Fatalf("restore cwd: %v", cerr)
		}
	})
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"config", "commit.gpgsign", "false"},
	} {
		runGitV(t, args...)
	}
	cfgPath := filepath.Join(dir, ".github", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-
      web:
        path: services/web
        tag_grammar:
          prefix: web-
`
	if err := os.WriteFile(cfgPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	writeCommitV(t, "README.md", "root", "chore: seed")
	runGitV(t, "tag", "api-1.0.0")
	runGitV(t, "tag", "web-1.0.0")
	return cfgPath
}

func runGitV(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeCommitV(t *testing.T, repoPath, content, message string) {
	t.Helper()
	if d := filepath.Dir(repoPath); d != "." {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(repoPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", repoPath, err)
	}
	runGitV(t, "add", repoPath)
	runGitV(t, "commit", "-m", message)
}

// runNextVersion drives the next-version command for a component and returns its
// stdout (the printed version), capturing os.Stdout since the command prints
// directly there.
func runNextVersion(t *testing.T, cfgPath, component string) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	cmd := NewCommand()
	cmd.SetArgs([]string{"--config", cfgPath, "--environment", "dev", "--component", component})
	runErr := cmd.Execute()

	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	os.Stdout = orig

	if runErr != nil {
		t.Fatalf("next-version --component %s: %v", component, runErr)
	}
	return strings.TrimSpace(buf.String())
}

// TestNextVersion_ComponentScoped_AgreesWithOrchestrator proves the CLI
// next-version path, scoped to a component, computes the same isolated per-component
// version that orchestrate.calculateComponentVersion does for the same repo shape
// (see orchestrate.TestCalculateComponentVersion_IsolatesComponents, which asserts
// the identical literals). This is the "two next-version paths agree" invariant.
func TestNextVersion_ComponentScoped_AgreesWithOrchestrator(t *testing.T) {
	cfgPath := componentRepo(t)

	writeCommitV(t, filepath.Join("services", "api", "handler.go"), "package api", "feat: add api handler")

	if got := runNextVersion(t, cfgPath, "api"); got != "api-1.1.0-rc.0" {
		t.Errorf("api next-version = %q, want %q", got, "api-1.1.0-rc.0")
	}
	if got := runNextVersion(t, cfgPath, "web"); got != "web-1.0.0-rc.0" {
		t.Errorf("web next-version = %q, want %q (api's commit must not move web)", got, "web-1.0.0-rc.0")
	}
}

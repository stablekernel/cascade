package orchestrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// componentRepo initializes a git repo in a temp dir with a two-component
// manifest, changes into it for the test, and returns the config path. Both
// components are seeded with a v1.0.0 release tag at a shared base commit so each
// has an isolated namespace to advance from.
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
		runGitT(t, args...)
	}

	configPath := filepath.Join(dir, ".github", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		t.Fatalf("mkdir .github: %v", err)
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
        tag_prefix: api-
      web:
        path: services/web
        tag_prefix: web-
`
	if err := os.WriteFile(configPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Base commit shared by both components' v1.0.0 release tags.
	writeCommitT(t, "README.md", "root", "chore: seed")
	runGitT(t, "tag", "api-1.0.0")
	runGitT(t, "tag", "web-1.0.0")

	return configPath
}

func runGitT(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeCommitT(t *testing.T, repoPath, content, message string) {
	t.Helper()
	if d := filepath.Dir(repoPath); d != "." {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(repoPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", repoPath, err)
	}
	runGitT(t, "add", repoPath)
	runGitT(t, "commit", "-m", message)
}

func componentVersion(t *testing.T, configPath, component string) string {
	t.Helper()
	orch, err := NewOrchestrator(configPath, "ci", "dev", WithComponent(component))
	if err != nil {
		t.Fatalf("NewOrchestrator(%s): %v", component, err)
	}
	v, err := orch.calculateVersion()
	if err != nil {
		t.Fatalf("calculateVersion(%s): %v", component, err)
	}
	return v
}

// TestCalculateComponentVersion_IsolatesComponents proves that advancing one
// component's path bumps only that component's version, in its own tag namespace,
// while a sibling with no path-scoped commits stays at its own base. This is the
// core per-component isolation invariant (HLD Section 4 row 3, Section 5).
func TestCalculateComponentVersion_IsolatesComponents(t *testing.T) {
	configPath := componentRepo(t)

	// Advance only the api subtree with a feature commit.
	writeCommitT(t, filepath.Join("services", "api", "handler.go"), "package api", "feat: add api handler")

	if got := componentVersion(t, configPath, "api"); got != "api-1.1.0-rc.0" {
		t.Errorf("api version = %q, want %q (minor bump from its own commit)", got, "api-1.1.0-rc.0")
	}
	// web saw no commit under services/web, so its base does not advance and it
	// stays on its own namespace, unaffected by api's feature.
	if got := componentVersion(t, configPath, "web"); got != "web-1.0.0-rc.0" {
		t.Errorf("web version = %q, want %q (api's commit must not move web)", got, "web-1.0.0-rc.0")
	}

	// Now advance only the web subtree with a fix; api must not move.
	writeCommitT(t, filepath.Join("services", "web", "server.go"), "package web", "fix: web bug")

	if got := componentVersion(t, configPath, "web"); got != "web-1.0.1-rc.0" {
		t.Errorf("web version = %q, want %q (patch bump from its own commit)", got, "web-1.0.1-rc.0")
	}
	if got := componentVersion(t, configPath, "api"); got != "api-1.1.0-rc.0" {
		t.Errorf("api version = %q, want %q (web's commit must not move api)", got, "api-1.1.0-rc.0")
	}
}

// TestCalculateComponentVersion_StrictNamespace proves a component reads only its
// own tag namespace: a sibling's higher-sorting release tag does not become the
// component's base version.
func TestCalculateComponentVersion_StrictNamespace(t *testing.T) {
	configPath := componentRepo(t)

	// A much higher web release tag must be invisible to api's base derivation.
	runGitT(t, "tag", "web-9.9.9")
	writeCommitT(t, filepath.Join("services", "api", "handler.go"), "package api", "feat: add api handler")

	if got := componentVersion(t, configPath, "api"); got != "api-1.1.0-rc.0" {
		t.Errorf("api version = %q, want %q (must not read web-9.9.9 as base)", got, "api-1.1.0-rc.0")
	}
}

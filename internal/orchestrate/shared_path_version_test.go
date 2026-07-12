package orchestrate

import (
	"os"
	"path/filepath"
	"testing"
)

// sharedPathRepo initializes a git repo with a two-component manifest where the
// api component declares extra_paths covering a shared library and web does not.
// Both components are seeded with a v1.0.0 release tag at a shared base. topSugar
// selects the shared_paths top-level form instead of per-component extra_paths.
func sharedPathRepo(t *testing.T, topSugar bool) string {
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

	var manifest string
	if topSugar {
		// Top-level shared_paths fans out to every component (api and web).
		manifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    shared_paths:
      - libs/shared/**
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
	} else {
		// Only api declares the shared library under its extra_paths.
		manifest = `ci:
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
        extra_paths:
          - libs/shared/**
      web:
        path: services/web
        tag_grammar:
          prefix: web-
`
	}
	if err := os.WriteFile(configPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	writeCommitT(t, "README.md", "root", "chore: seed")
	runGitT(t, "tag", "api-1.0.0")
	runGitT(t, "tag", "web-1.0.0")

	return configPath
}

// TestCalculateComponentVersion_SharedPathBumpsConsumer proves a breaking change
// touching only a shared library the api component declares under extra_paths
// bumps api to a major version, while the non-consuming web component (which does
// not declare the shared path) stays at its base. This is the shared-path
// correctness fix: the commit is outside every component's own path, so before
// the extra_paths threading it bumped nothing.
func TestCalculateComponentVersion_SharedPathBumpsConsumer(t *testing.T) {
	configPath := sharedPathRepo(t, false)

	// A breaking change under the shared library only, no service subtree touched.
	writeCommitT(t, filepath.Join("libs", "shared", "proto.go"), "package shared", "feat!: change shared proto contract")

	if got := componentVersion(t, configPath, "api"); got != "api-2.0.0-rc.0" {
		t.Errorf("api version = %q, want %q (major bump from breaking shared-path change)", got, "api-2.0.0-rc.0")
	}
	// web does not declare the shared path, so the shared commit is out of its
	// range and it stays at its base.
	if got := componentVersion(t, configPath, "web"); got != "web-1.0.0-rc.0" {
		t.Errorf("web version = %q, want %q (non-consumer must not move)", got, "web-1.0.0-rc.0")
	}
}

// TestCalculateComponentVersion_SharedPathsSugarBumpsAll proves the top-level
// shared_paths sugar fans out to every component: a fix under the shared library
// bumps both api and web (both declare it via the sugar).
func TestCalculateComponentVersion_SharedPathsSugarBumpsAll(t *testing.T) {
	configPath := sharedPathRepo(t, true)

	writeCommitT(t, filepath.Join("libs", "shared", "util.go"), "package shared", "fix: shared util bug")

	if got := componentVersion(t, configPath, "api"); got != "api-1.0.1-rc.0" {
		t.Errorf("api version = %q, want %q (patch bump from shared_paths fan-out)", got, "api-1.0.1-rc.0")
	}
	if got := componentVersion(t, configPath, "web"); got != "web-1.0.1-rc.0" {
		t.Errorf("web version = %q, want %q (patch bump from shared_paths fan-out)", got, "web-1.0.1-rc.0")
	}
}

// TestCalculateComponentVersion_SharedPathNotDeclaredNoBump is the control that
// pins the pre-fix behavior for an undeclared shared path: a component whose
// extra_paths does not cover the shared library sees no bump from a shared-only
// commit. It documents that threading extra_paths is exactly what turns the
// no-bump bug into the correct bump asserted above.
func TestCalculateComponentVersion_SharedPathNotDeclaredNoBump(t *testing.T) {
	configPath := sharedPathRepo(t, false)

	writeCommitT(t, filepath.Join("libs", "shared", "proto.go"), "package shared", "feat!: change shared proto contract")

	// web does not declare libs/shared/** anywhere, so the shared commit is out of
	// range: no bump, base version retained.
	if got := componentVersion(t, configPath, "web"); got != "web-1.0.0-rc.0" {
		t.Errorf("web version = %q, want %q (undeclared shared path yields no bump)", got, "web-1.0.0-rc.0")
	}
}

// TestDetectChanges_WithExtraPaths proves a callback whose own triggers do not
// list a shared path still runs when that shared path changes, once the
// component's extra paths are unioned in; and that an empty trigger list is left
// untouched (it already means run-on-any-change).
func TestDetectChanges_WithExtraPaths(t *testing.T) {
	// Non-empty triggers gain the shared path.
	got := withExtraPaths([]string{"services/api/**"}, []string{"libs/shared/**"})
	if want := []string{"services/api/**", "libs/shared/**"}; !equalStrings(got, want) {
		t.Errorf("withExtraPaths(non-empty) = %v, want %v", got, want)
	}
	// Empty triggers are returned unchanged (run-on-any-change preserved).
	if got := withExtraPaths(nil, []string{"libs/shared/**"}); got != nil {
		t.Errorf("withExtraPaths(empty triggers) = %v, want nil (unchanged)", got)
	}
	// No extra paths: original returned unchanged.
	orig := []string{"services/api/**"}
	if got := withExtraPaths(orig, nil); !equalStrings(got, orig) {
		t.Errorf("withExtraPaths(no extra) = %v, want %v", got, orig)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

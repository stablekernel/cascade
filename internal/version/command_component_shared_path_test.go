package version

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sharedPathComponentRepo initializes a git repo with a two-component manifest
// where the api component declares extra_paths covering a shared library and web
// does not, changes into it for the test, and returns the config path. Both
// components are seeded with a v1.0.0 release tag at a shared base. It mirrors
// orchestrate's sharedPathRepo so the two packages assert the same repo shape.
func sharedPathComponentRepo(t *testing.T) string {
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
        extra_paths:
          - libs/shared/**
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

// runNextVersionErr drives the next-version command for a component and returns
// its stdout and the execution error, without failing the test on error.
func runNextVersionErr(t *testing.T, cfgPath, component string) (string, error) {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--config", cfgPath, "--environment", "dev", "--component", component})
	runErr := cmd.Execute()

	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	os.Stdout = orig

	return strings.TrimSpace(buf.String()), runErr
}

// TestNextVersion_ComponentScoped_SharedPathBumpsConsumer proves the CLI
// next-version path counts a commit that touches only a shared path the
// component declares under extra_paths, reporting the same version orchestrate
// computes for the identical repo shape
// (orchestrate.TestCalculateComponentVersion_SharedPathBumpsConsumer asserts the
// same literals). Before the shared scope derivation, the CLI scoped commits to
// the component path only and previewed a lower version than production mints.
func TestNextVersion_ComponentScoped_SharedPathBumpsConsumer(t *testing.T) {
	cfgPath := sharedPathComponentRepo(t)

	// A breaking change under the shared library only, no service subtree touched.
	writeCommitV(t, filepath.Join("libs", "shared", "proto.go"), "package shared", "feat!: change shared proto contract")

	if got := runNextVersion(t, cfgPath, "api"); got != "api-2.0.0-rc.0" {
		t.Errorf("api next-version = %q, want %q (major bump from breaking shared-path change, matching orchestrate)", got, "api-2.0.0-rc.0")
	}
	// web does not declare the shared path, so the shared commit is out of its
	// range and it stays at its base.
	if got := runNextVersion(t, cfgPath, "web"); got != "web-1.0.0-rc.0" {
		t.Errorf("web next-version = %q, want %q (non-consumer must not move)", got, "web-1.0.0-rc.0")
	}
}

// TestNextVersion_ComponentScoped_TagLookupErrorSurfaces proves a failing tag
// lookup surfaces as a command error instead of being read as "no tags": a
// swallowed failure would silently preview v0.1.0-rc.0, the same regressive-mint
// hazard orchestrate's component path guards against.
func TestNextVersion_ComponentScoped_TagLookupErrorSurfaces(t *testing.T) {
	// A manifest in a directory that is not a git repository: every git tag
	// lookup fails here.
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
`
	if err := os.WriteFile(cfgPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	out, runErr := runNextVersionErr(t, cfgPath, "api")
	if runErr == nil {
		t.Fatalf("next-version --component in a non-repo dir succeeded with %q, want a surfaced tag-lookup error", out)
	}
	if !strings.Contains(runErr.Error(), "reading latest tag for component") {
		t.Errorf("error = %v, want it to surface the tag lookup failure", runErr)
	}
}

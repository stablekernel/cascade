package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These tests run the built binary end to end and prove that --dry-run gates
// the state-mutation paths. The temp dir is deliberately not a git repo: if
// the dry-run guard is bypassed, the command rewrites the manifest on disk
// and then fails attempting a real commit and push, so the assertions below
// catch both the mutation and the non-zero exit.

// TestOrchestrateFinalize_DryRunDoesNotMutateState proves that
// `orchestrate finalize --dry-run` writes no state: the manifest stays
// byte-identical, the command exits 0, and the dry-run preview is printed.
func TestOrchestrateFinalize_DryRunDoesNotMutateState(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, ".github", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatalf("creating .github dir: %v", err)
	}

	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.0
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}

	stdout, stderr, err := runCLIIn(tmpDir,
		"orchestrate", "finalize",
		"--config", manifestPath,
		"--environment", "dev",
		"--version", "v1.2.3",
		"--deploy-results", "app:success",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run finalize must not fail: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("re-reading manifest: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("dry-run finalize mutated the manifest on disk:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "Would update state") {
		t.Errorf("expected dry-run preview output, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestExternalUpdate_DryRunDoesNotMutateState proves that
// `external update --dry-run` writes no state: the manifest stays
// byte-identical, the command exits 0, and the dry-run preview is printed.
func TestExternalUpdate_DryRunDoesNotMutateState(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
    external:
      - repo: org/cdk-infra
        deploys:
          - name: cdk
            workflow: .github/workflows/deploy-cdk.yaml
  state:
    dev:
      sha: abc123
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}

	stdout, stderr, err := runCLIIn(tmpDir,
		"external", "update",
		"--config", manifestPath,
		"--source-repo", "org/cdk-infra",
		"--deploy-name", "cdk",
		"--environment", "dev",
		"--sha", "deadbeef",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run external update must not fail: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("re-reading manifest: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("dry-run external update mutated the manifest on disk:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "Would commit and push") {
		t.Errorf("expected dry-run preview output, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// runCLIInEnv runs the test binary like runCLIIn but with extra environment
// variables appended, so a test can point GITHUB_API_URL at a local recording
// server and prove no API mutation leaves the process.
func runCLIInEnv(dir string, env []string, args ...string) (string, string, error) {
	bin, err := filepath.Abs("./cascade-test")
	if err != nil {
		return "", "", err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

// newRecordingGitHubAPI starts a GitHub-shaped test server that answers read
// requests with plausible bodies (so a leaked mutation flow proceeds far
// enough to attempt its write) and records every non-GET request. The second
// return value lists the recorded mutations.
func newRecordingGitHubAPI(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var mutations []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			switch {
			case strings.Contains(r.URL.Path, "/releases/tags/"):
				_, _ = w.Write([]byte(`{"id":1,"tag_name":"v1.2.3","draft":true,"url":"u","html_url":"h"}`))
			case strings.Contains(r.URL.Path, "/git/refs/tags"):
				_, _ = w.Write([]byte(`[]`))
			case strings.Contains(r.URL.Path, "/releases"):
				_, _ = w.Write([]byte(`[]`))
			default:
				_, _ = w.Write([]byte(`{}`))
			}
			return
		}

		mu.Lock()
		mutations = append(mutations, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte(`{"id":1,"tag_name":"v1.2.3","draft":false,"url":"u","html_url":"h"}`))
		}
	}))
	t.Cleanup(srv.Close)

	recorded := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), mutations...)
	}
	return srv, recorded
}

// TestManageReleaseDelete_DryRunDoesNotMutate proves that
// `manage-release --action delete --dry-run` issues NO delete (or any other
// mutating) request: the recording server sees only reads, the command exits
// 0, and a dry-run preview is printed. This is a data-safety regression test:
// before the fix the command deleted the real release under --dry-run.
func TestManageReleaseDelete_DryRunDoesNotMutate(t *testing.T) {
	srv, recorded := newRecordingGitHubAPI(t)

	stdout, stderr, err := runCLIInEnv(t.TempDir(),
		[]string{"GITHUB_API_URL=" + srv.URL, "GITHUB_TOKEN=test-token"},
		"manage-release",
		"--action", "delete",
		"--repo", "owner/repo",
		"--environment", "prod",
		"--tag", "v1.2.3",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run manage-release delete must not fail: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if muts := recorded(); len(muts) > 0 {
		t.Fatalf("dry-run manage-release delete issued real API mutations: %v", muts)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "Dry run") {
		t.Errorf("expected dry-run preview output, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestManageReleasePublish_DryRunDoesNotMutate proves that
// `manage-release --action publish --dry-run` issues NO publish PATCH, tag
// create, or tag delete: only reads reach the recording server, the command
// exits 0, and a dry-run preview is printed.
func TestManageReleasePublish_DryRunDoesNotMutate(t *testing.T) {
	srv, recorded := newRecordingGitHubAPI(t)

	stdout, stderr, err := runCLIInEnv(t.TempDir(),
		[]string{"GITHUB_API_URL=" + srv.URL, "GITHUB_TOKEN=test-token"},
		"manage-release",
		"--action", "publish",
		"--repo", "owner/repo",
		"--environment", "prod",
		"--sha", "abc123",
		"--tag", "v1.2.3",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run manage-release publish must not fail: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if muts := recorded(); len(muts) > 0 {
		t.Fatalf("dry-run manage-release publish issued real API mutations: %v", muts)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "Dry run") {
		t.Errorf("expected dry-run preview output, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestBranchProtectionApply_DryRunDoesNotMutate proves that
// `branch-protection --apply --dry-run` issues NO protection PUT: the
// recording server sees no requests, the command exits 0, and a dry-run
// preview is printed. Before the fix the command PUT live branch protection
// under --dry-run.
func TestBranchProtectionApply_DryRunDoesNotMutate(t *testing.T) {
	srv, recorded := newRecordingGitHubAPI(t)

	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")
	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
  state:
    dev:
      sha: abc123
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	stdout, stderr, err := runCLIIn(tmpDir,
		"branch-protection",
		"--config", manifestPath,
		"--apply",
		"--repo", "owner/repo",
		"--token", "test-token",
		"--api-url", srv.URL,
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run branch-protection --apply must not fail: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if muts := recorded(); len(muts) > 0 {
		t.Fatalf("dry-run branch-protection --apply issued real API mutations: %v", muts)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "Dry run") {
		t.Errorf("expected dry-run preview output, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestPromoteFinalize_DryRunDoesNotMutateState proves that
// `promote finalize --dry-run` writes no state: the manifest stays
// byte-identical, no commit or push is attempted (the temp dir is not a git
// repo, so a leaked mutation fails loudly), and the dry-run preview prints.
// The gate exists in the command; this test is what keeps it from being
// deleted silently.
func TestPromoteFinalize_DryRunDoesNotMutateState(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")
	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
  state:
    dev:
      sha: abc123
      version: v1.0.0-rc.0
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}

	stdout, stderr, err := runCLIIn(tmpDir,
		"promote", "finalize",
		"--config", manifestPath,
		"--promotion-result", `{"success":true,"final_env":"dev","promotions":[{"environment":"dev","version":"v1.2.3","sha":"abc123"}]}`,
		"--commit-push",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run promote finalize must not fail: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("re-reading manifest: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("dry-run promote finalize mutated the manifest on disk:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	combined := stdout + stderr
	if !strings.Contains(combined, "Dry run") {
		t.Errorf("expected dry-run preview output, got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

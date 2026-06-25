package orchestrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// gitEnv returns a deterministic git identity so commits work in CI runners
// that have no global git config.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=cascade-test",
		"GIT_AUTHOR_EMAIL=cascade-test@example.com",
		"GIT_COMMITTER_NAME=cascade-test",
		"GIT_COMMITTER_EMAIL=cascade-test@example.com",
	)
}

// runGit runs a git command in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFile writes a file under dir, creating parent directories.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// initRepo creates a real git repo with an initial non-src commit (so HEAD~1
// exists) followed by a src/** commit (HEAD). It returns the repo root and the
// HEAD SHA of the src commit.
func initRepo(t *testing.T) (repoDir, headSHA string) {
	t.Helper()
	repoDir = t.TempDir()
	runGit(t, repoDir, "init", "-b", "main")

	// Initial, non-src commit so HEAD~1 exists.
	writeFile(t, repoDir, "README.md", "# fixture\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "chore: init")

	// A src/** commit becomes HEAD.
	writeFile(t, repoDir, "src/app.go", "package main\n\nfunc main() {}\n")
	runGit(t, repoDir, "add", "src/app.go")
	runGit(t, repoDir, "commit", "-m", "feat: add app")

	headSHA = runGit(t, repoDir, "rev-parse", "HEAD")
	return repoDir, headSHA
}

// writeManifest writes a no-environment manifest under <repo>/.github/manifest.yaml
// with a single build on src/** triggers and records prerelease state at the
// given SHA. It returns the manifest path.
func writeManifest(t *testing.T, repoDir, stateSHA string) string {
	t.Helper()
	manifestPath := filepath.Join(repoDir, ".github", "manifest.yaml")
	manifest := `ci:
  config:
    project: nochange-test
    trunk_branch: main
    environments: []
    builds:
      - name: app
        workflow: build.yaml
        triggers: ["src/**"]
    deploys: []
  state:
    prerelease:
      sha: ` + stateSHA + `
      version: v0.1.0-rc.0
`
	writeFile(t, repoDir, ".github/manifest.yaml", manifest)
	return manifestPath
}

// TestSetup_NoChangeReDispatch_SkipsBuild verifies that when HEAD is unchanged
// since the last orchestrate (envState.SHA == HEAD), a build with no dependent
// deploy is SKIPPED rather than re-run. This is the no-change-skip bug: the old
// base ladder fell back to HEAD~1, which still contains the last src commit, so
// the build re-ran on every dispatch.
func TestSetup_NoChangeReDispatch_SkipsBuild(t *testing.T) {
	repoDir, headSHA := initRepo(t)

	// State records the build was already produced at HEAD (env-level SHA).
	manifestPath := writeManifest(t, repoDir, headSHA)

	orch, err := NewOrchestrator(manifestPath, "ci", "prerelease")
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	res, err := orch.Setup(headSHA)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if res.RunBuilds["app"] {
		t.Errorf("RunBuilds[app] = true, want false (no-change re-dispatch should skip the build)")
	}
}

// TestSetup_NewCommit_RunsBuild is the positive counterpart: once HEAD advances
// past the recorded state SHA with a src change, the build must run again.
func TestSetup_NewCommit_RunsBuild(t *testing.T) {
	repoDir, oldHead := initRepo(t)

	// State still points at the previous HEAD.
	manifestPath := writeManifest(t, repoDir, oldHead)

	// Advance HEAD with a new src commit.
	writeFile(t, repoDir, "src/feature.go", "package main\n\nfunc Feature() {}\n")
	runGit(t, repoDir, "add", "src/feature.go")
	runGit(t, repoDir, "commit", "-m", "feat: add feature")
	newHead := runGit(t, repoDir, "rev-parse", "HEAD")

	orch, err := NewOrchestrator(manifestPath, "ci", "prerelease")
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	res, err := orch.Setup(newHead)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if !res.RunBuilds["app"] {
		t.Errorf("RunBuilds[app] = false, want true (a new src commit must re-run the build)")
	}
}

// TestFinalize_RecordsPerBuildSHA verifies that a successful build's SHA is
// recorded into envState.Builds so the build base ladder can consult it on the
// next dispatch.
func TestFinalize_RecordsPerBuildSHA(t *testing.T) {
	repoDir, headSHA := initRepo(t)
	manifestPath := writeManifest(t, repoDir, "")

	// Finalize commits and pushes the updated manifest. Wire up a bare remote
	// and an upstream so the push has a destination in this hermetic fixture.
	remoteDir := t.TempDir()
	runGit(t, remoteDir, "init", "--bare", "-b", "main")
	runGit(t, repoDir, "remote", "add", "origin", remoteDir)
	runGit(t, repoDir, "push", "-u", "origin", "main")

	orch, err := NewOrchestrator(manifestPath, "ci", "prerelease")
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	// Run Finalize in dry-run-free mode but avoid the commit/push by checking
	// state in-memory before the write side effects matter. Finalize writes the
	// config file and attempts a commit; in a fresh repo with the manifest
	// staged this still records state in memory, which is what we assert.
	err = orch.Finalize(headSHA, "v0.1.0-rc.0",
		map[string]string{},
		map[string]string{"app": "success"},
	)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	envState := orch.cicdFile.State["prerelease"]
	if envState == nil {
		t.Fatalf("prerelease state is nil after Finalize")
	}
	bs := envState.Builds["app"]
	if bs == nil {
		t.Fatalf("envState.Builds[app] is nil; expected a recorded build state")
	}
	if bs.SHA != headSHA {
		t.Errorf("envState.Builds[app].SHA = %q, want %q", bs.SHA, headSHA)
	}
	if bs.BuiltAt == "" {
		t.Errorf("envState.Builds[app].BuiltAt is empty; expected a timestamp")
	}
	_ = config.BuildState{} // keep config import meaningful if assertions change
}

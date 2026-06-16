package external

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs a git command in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// initClone clones src into a fresh temp dir and configures a committer identity
// plus rebase-on-pull, matching the CI checkout the external-update workflow runs in.
func initClone(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "clone", src, dir).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	runGit(t, dir, "config", "user.email", "ci@example.com")
	runGit(t, dir, "config", "user.name", "CI")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "pull.rebase", "true")
	return dir
}

const primaryManifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, test, prod]
    external:
      - repo: org/cdk-infra
        deploys:
          - name: cdk
            workflow: .github/workflows/deploy-cdk.yaml
      - repo: org/lambda-svc
        deploys:
          - name: lambda
            workflow: .github/workflows/deploy-lambda.yaml
  state:
    dev:
      sha: base
`

// withWorkdir changes the working directory to dir for the test duration. The
// external command resolves git operations against the process working directory,
// so the test must chdir into the stale clone before invoking runUpdate.
func withWorkdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}

// TestUpdateCommand_RecoversFromConcurrentUpdate exercises the concurrent
// external-update race end-to-end through runUpdate: a competing update lands on
// origin while this run holds a stale checkout, so the state-write push is
// rejected non-fast-forward. The command must self-heal (rebase and retry) and
// preserve BOTH artifacts' external slots. With the plain push it loses the race
// and drops a slot; with the retrying push both slots survive.
func TestUpdateCommand_RecoversFromConcurrentUpdate(t *testing.T) {
	origin := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	// Seed origin with the primary manifest.
	seedDir := initClone(t, origin)
	if err := os.WriteFile(filepath.Join(seedDir, "cascade.yaml"), []byte(primaryManifest), 0o600); err != nil {
		t.Fatalf("write seed manifest: %v", err)
	}
	runGit(t, seedDir, "add", "cascade.yaml")
	runGit(t, seedDir, "commit", "-m", "seed manifest")
	runGit(t, seedDir, "push", "origin", "HEAD:main")

	// This run's checkout (stale once the competing update lands).
	work := initClone(t, origin)

	// A competing external update for "cdk" lands on origin first.
	competitor := initClone(t, origin)
	if err := runUpdateIn(t, competitor, "org/cdk-infra", "cdk", "dev", "cdksha", "v1.0.0", ""); err != nil {
		t.Fatalf("competing update: %v", err)
	}

	// This run updates "lambda" against the now-stale checkout.
	if err := runUpdateIn(t, work, "org/lambda-svc", "lambda", "dev", "lambdasha", "v2.0.0", ""); err != nil {
		t.Fatalf("runUpdate() error = %v, want nil (must rebase over competing cdk update and push)", err)
	}

	// Origin HEAD must carry BOTH external slots: no artifact was dropped.
	out, err := exec.Command("git", "-C", origin, "show", "HEAD:cascade.yaml").Output()
	if err != nil {
		t.Fatalf("git show HEAD:cascade.yaml: %v", err)
	}
	manifest := string(out)
	if !strings.Contains(manifest, "lambda") {
		t.Errorf("origin manifest missing lambda slot; got:\n%s", manifest)
	}
	if !strings.Contains(manifest, "cdk") {
		t.Errorf("origin manifest missing cdk slot - concurrent state loss; got:\n%s", manifest)
	}
}

// TestUpdateCommand_SequentialStaleCheckout exercises a sequential stale-checkout:
// a competing update lands on origin AFTER this run cloned, so this run's checkout
// is stale by push time even though the two updates never overlap in wall-clock
// time. The application-level retry must fetch the remote tip, reset onto it, and
// re-apply, preserving BOTH external slots.
func TestUpdateCommand_SequentialStaleCheckout(t *testing.T) {
	origin := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	// Seed origin with the primary manifest.
	seedDir := initClone(t, origin)
	if err := os.WriteFile(filepath.Join(seedDir, "cascade.yaml"), []byte(primaryManifest), 0o600); err != nil {
		t.Fatalf("write seed manifest: %v", err)
	}
	runGit(t, seedDir, "add", "cascade.yaml")
	runGit(t, seedDir, "commit", "-m", "seed manifest")
	runGit(t, seedDir, "push", "origin", "HEAD:main")

	// This run's checkout is taken first, before the competing update lands.
	work := initClone(t, origin)

	// A separate clone pushes a cdk update, making the work checkout stale.
	competitor := initClone(t, origin)
	if err := runUpdateIn(t, competitor, "org/cdk-infra", "cdk", "dev", "cdksha", "v1.0.0", ""); err != nil {
		t.Fatalf("competing update: %v", err)
	}

	// This run updates lambda from the now-stale work checkout.
	if err := runUpdateIn(t, work, "org/lambda-svc", "lambda", "dev", "lambdasha", "v2.0.0", ""); err != nil {
		t.Fatalf("runUpdate() error = %v, want nil (must reset onto remote and push)", err)
	}

	// Origin HEAD must carry BOTH external slots: no artifact was dropped.
	out, err := exec.Command("git", "-C", origin, "show", "HEAD:cascade.yaml").Output()
	if err != nil {
		t.Fatalf("git show HEAD:cascade.yaml: %v", err)
	}
	manifest := string(out)
	if !strings.Contains(manifest, "lambda") {
		t.Errorf("origin manifest missing lambda slot; got:\n%s", manifest)
	}
	if !strings.Contains(manifest, "cdk") {
		t.Errorf("origin manifest missing cdk slot - sequential state loss; got:\n%s", manifest)
	}
}

// TestUpdateCommand_IdempotentReapply runs the same external deploy twice from one
// checkout. The second run re-reads the manifest (whose HEAD already matches origin)
// and updates the slot in place. The result is exactly one cdk slot carrying the
// latest sha and version, with no error on either call.
func TestUpdateCommand_IdempotentReapply(t *testing.T) {
	origin := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	// Seed origin with the primary manifest.
	seedDir := initClone(t, origin)
	if err := os.WriteFile(filepath.Join(seedDir, "cascade.yaml"), []byte(primaryManifest), 0o600); err != nil {
		t.Fatalf("write seed manifest: %v", err)
	}
	runGit(t, seedDir, "add", "cascade.yaml")
	runGit(t, seedDir, "commit", "-m", "seed manifest")
	runGit(t, seedDir, "push", "origin", "HEAD:main")

	work := initClone(t, origin)

	if err := runUpdateIn(t, work, "org/cdk-infra", "cdk", "dev", "sha1", "v1.0.0", ""); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if err := runUpdateIn(t, work, "org/cdk-infra", "cdk", "dev", "sha2", "v2.0.0", ""); err != nil {
		t.Fatalf("second update: %v", err)
	}

	out, err := exec.Command("git", "-C", origin, "show", "HEAD:cascade.yaml").Output()
	if err != nil {
		t.Fatalf("git show HEAD:cascade.yaml: %v", err)
	}
	manifest := string(out)

	// Exactly one cdk slot survives, carrying the latest values.
	if got := strings.Count(manifest, "cdk:"); got != 1 {
		t.Errorf("expected exactly one cdk slot, got %d; manifest:\n%s", got, manifest)
	}
	if !strings.Contains(manifest, "sha2") {
		t.Errorf("origin manifest missing latest sha2; got:\n%s", manifest)
	}
	if !strings.Contains(manifest, "v2.0.0") {
		t.Errorf("origin manifest missing latest version v2.0.0; got:\n%s", manifest)
	}
	if strings.Contains(manifest, "sha1") {
		t.Errorf("origin manifest retains stale sha1; got:\n%s", manifest)
	}
}

// runUpdateIn runs runUpdate from within dir, pointing the command at dir's
// cascade.yaml. It saves and restores the package-level flag state and working
// directory so tests stay isolated.
func runUpdateIn(t *testing.T, dir, sourceRepo, deployName, environment, sha, version, artifacts string) error {
	t.Helper()
	withWorkdir(t, dir)

	prevConfig, prevKey := configPath, manifestKey
	t.Cleanup(func() { configPath, manifestKey = prevConfig, prevKey })
	configPath = filepath.Join(dir, "cascade.yaml")
	manifestKey = "ci"

	return runUpdate(sourceRepo, deployName, environment, sha, version, artifacts)
}

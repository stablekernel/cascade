package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rejectingRemote builds a bare origin seeded with one commit, then installs a
// pre-receive hook that counts every push attempt and rejects it with a GH013
// marker, the shape of a workflow-scope refusal, branch protection, or an auth
// failure. It returns the seed clone (upstream already configured by the seed
// push) and the path of the hook's attempt counter.
func rejectingRemote(t *testing.T) (seed, countFile string) {
	t.Helper()

	origin := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", origin).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	// Pin both repos' hooks directories so the test is hermetic: a developer's
	// global core.hooksPath would otherwise shadow the origin's pre-receive hook
	// and inject client-side hooks into the push under test.
	gitAt(t, origin, "config", "core.hooksPath", filepath.Join(origin, "hooks"))

	seed = t.TempDir()
	gitAt(t, "", "clone", origin, seed)
	configRepo(t, seed)
	gitAt(t, seed, "config", "core.hooksPath", t.TempDir())
	if err := os.WriteFile(filepath.Join(seed, "state.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	gitAt(t, seed, "add", "state.txt")
	gitAt(t, seed, "commit", "-m", "seed state")
	gitAt(t, seed, "push", "origin", "main")

	countFile = filepath.Join(origin, "push-count")
	hook := `#!/bin/sh
dir="$(cd "$(dirname "$0")/.." && pwd)"
n=$(cat "$dir/push-count" 2>/dev/null || echo 0)
echo $((n+1)) > "$dir/push-count"
echo "GH013: refusing state write without workflow scope" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(origin, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatalf("install pre-receive hook: %v", err)
	}
	return seed, countFile
}

// TestPushWithRebaseRetry_FailsFastOnRemoteRejection proves a push the remote
// itself declines (a GH013 workflow-scope refusal, branch protection, auth) is
// not blind-retried behind no-op rebases: no rebase can make it succeed, so the
// loop must fail on the first attempt and surface the remote's actual output
// instead of a generic exhaustion summary with zero diagnostic content.
func TestPushWithRebaseRetry_FailsFastOnRemoteRejection(t *testing.T) {
	seed, countFile := rejectingRemote(t)

	if err := os.WriteFile(filepath.Join(seed, "state.txt"), []byte("local change\n"), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	gitAt(t, seed, "add", "state.txt")
	gitAt(t, seed, "commit", "-m", "local change")

	err := PushWithRebaseRetry(WithDir(seed), WithBackoff(time.Millisecond))
	if err == nil {
		t.Fatal("PushWithRebaseRetry() against a rejecting remote = nil, want an error")
	}
	if !strings.Contains(err.Error(), "GH013") {
		t.Errorf("PushWithRebaseRetry() error %q must carry the remote's rejection output", err)
	}

	count, readErr := os.ReadFile(countFile)
	if readErr != nil {
		t.Fatalf("read push counter: %v", readErr)
	}
	if got := strings.TrimSpace(string(count)); got != "1" {
		t.Errorf("remote saw %s push attempts, want 1: a non-retryable rejection must fail fast", got)
	}
}

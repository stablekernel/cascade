package statewrite

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// ghRacerStub scripts a gh CLI whose backing "repository" advances after every
// invocation, the way a concurrent finalize commits between two API calls. It
// answers all three Contents API read shapes (raw body, --jq .sha, and the plain
// JSON document) from the state it holds at call time, then bumps the state, so
// any client that reads the content and the blob SHA in separate invocations
// observes a torn snapshot: content from commit N paired with the SHA of commit
// N+1.
const ghRacerStub = `#!/bin/sh
set -eu
n=$(cat "$GH_STUB_DIR/state")
body="manifest-state-$n"
case "$*" in
*"application/vnd.github.raw"*)
	printf '%s' "$body"
	;;
*"--jq"*)
	printf 'sha-%s\n' "$n"
	;;
*)
	b64=$(printf '%s' "$body" | base64 | tr -d '\n')
	printf '{"content":"%s","encoding":"base64","sha":"sha-%s"}\n' "$b64" "$n"
	;;
esac
echo $((n+1)) > "$GH_STUB_DIR/state"
`

// installGHStub writes script as an executable gh command in a fresh directory,
// prepends that directory to PATH, and points GH_STUB_DIR at a state directory
// seeded with counter 1.
func installGHStub(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the scripted gh stub needs a POSIX shell")
	}

	binDir := t.TempDir()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write gh stub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "state"), []byte("1\n"), 0o600); err != nil {
		t.Fatalf("seed stub state: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_STUB_DIR", stateDir)
	return stateDir
}

// ghRecorderStub scripts a gh CLI that records its argv one element per line
// and succeeds, so a test can assert the exact request a client assembles.
const ghRecorderStub = `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$GH_STUB_DIR/argv"
echo '{}'
`

// TestGHContents_PutContent_StampsIdentityAndLock verifies the Contents API
// state write attributes the commit to the resolved identity as both author and
// committer (falling back to the github-actions[bot] default) and passes the
// optimistic-lock sha only when one is held, so an empty sha creates the file
// rather than guarding an update.
func TestGHContents_PutContent_StampsIdentityAndLock(t *testing.T) {
	stateDir := installGHStub(t, ghRecorderStub)
	argvPath := filepath.Join(stateDir, "argv")

	err := ghContents{}.PutContent("acme/widgets", "m.yaml", "main", "deadbeef",
		"chore: update state after rollback of prod [skip ci]", []byte("content"), Identity{})
	if err != nil {
		t.Fatalf("PutContent() error: %v", err)
	}
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	for _, want := range []string{
		"author[name]=github-actions[bot]",
		"author[email]=github-actions[bot]@users.noreply.github.com",
		"committer[name]=github-actions[bot]",
		"committer[email]=github-actions[bot]@users.noreply.github.com",
		"sha=deadbeef",
	} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(want) + `$`).Match(argv) {
			t.Errorf("PutContent argv missing %q:\n%s", want, argv)
		}
	}

	err = ghContents{}.PutContent("acme/widgets", "m.yaml", "main", "cafef00d",
		"msg", []byte("content"), Identity{Name: "release-bot", Email: "release-bot@example.com"})
	if err != nil {
		t.Fatalf("PutContent() with custom identity error: %v", err)
	}
	argv, err = os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	for _, want := range []string{
		"author[name]=release-bot",
		"committer[email]=release-bot@example.com",
		"sha=cafef00d",
	} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(want) + `$`).Match(argv) {
			t.Errorf("PutContent argv missing %q:\n%s", want, argv)
		}
	}
}

// TestGHContents_GetContent_SingleSnapshot drives the production gh-backed
// client against the racing stub and requires the returned content and blob SHA
// to belong to the SAME commit. A torn read (stale content paired with a newer
// SHA) lets the subsequent PUT pass the optimistic lock while silently dropping
// the keys a concurrent finalize committed in between, which is exactly the
// lost-update the statewrite retry loop exists to prevent.
func TestGHContents_GetContent_SingleSnapshot(t *testing.T) {
	installGHStub(t, ghRacerStub)

	content, sha, err := ghContents{}.GetContent("owner/repo", "manifest.yaml", "main")
	if err != nil {
		t.Fatalf("GetContent() error: %v", err)
	}

	m := regexp.MustCompile(`^manifest-state-(\d+)$`).FindStringSubmatch(string(content))
	if m == nil {
		t.Fatalf("GetContent() content = %q, want a manifest-state-N body", content)
	}
	wantSHA := fmt.Sprintf("sha-%s", m[1])
	if sha != wantSHA {
		t.Fatalf("GetContent() returned content of commit %s with sha %q: content and optimistic-lock token must come from one snapshot (want %q)",
			m[1], sha, wantSHA)
	}
}

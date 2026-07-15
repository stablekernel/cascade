package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ghContentsAPIStub scripts the gh CLI's Contents API surface backed by files
// under GH_STUB_DIR, with compare-and-swap PUT semantics and one concurrent
// writer baked in: after the first read, a racing finalize lands its own env
// leaf (the prepared racer manifest) and advances the blob SHA, exactly as a
// sibling environment's finalize job does on a real trunk.
//
// GET answers all three read shapes (raw body, --jq .sha, plain JSON document)
// from the state at call time. PUT enforces the optimistic lock: a stale sha
// yields the branch-ref 409 body real GitHub produces; a matching sha commits
// the decoded content and advances the SHA.
const ghContentsAPIStub = `#!/bin/sh
set -eu
dir="$GH_STUB_DIR"

is_put=0
putsha=""
b64=""
for a in "$@"; do
	case "$a" in
	PUT) is_put=1 ;;
	sha=*) putsha="${a#sha=}" ;;
	content=*) b64="${a#content=}" ;;
	esac
done

cursha=$(cat "$dir/sha")

if [ "$is_put" = "1" ]; then
	if [ "$putsha" != "$cursha" ]; then
		printf '{"message":"main is at %s but expected %s","status":"409"}\n' "$cursha" "$putsha"
		echo "gh: HTTP 409: Conflict" >&2
		exit 1
	fi
	printf '%s' "$b64" | base64 --decode > "$dir/content"
	n=$(cat "$dir/puts"); n=$((n+1)); echo "$n" > "$dir/puts"
	echo "sha-put-$n" > "$dir/sha"
	printf '{"content":{"sha":"sha-put-%s"}}\n' "$n"
	exit 0
fi

case "$*" in
*"application/vnd.github.raw"*)
	cat "$dir/content"
	;;
*"--jq"*)
	cat "$dir/sha"
	;;
*)
	b64out=$(base64 < "$dir/content" | tr -d '\n')
	printf '{"content":"%s","encoding":"base64","sha":"%s"}\n' "$b64out" "$(cat "$dir/sha")"
	;;
esac

if [ ! -f "$dir/racer_done" ]; then
	cp "$dir/racer" "$dir/content"
	echo "sha-racer" > "$dir/sha"
	touch "$dir/racer_done"
fi
`

// TestPromoteFinalizeMergesConcurrentStateWrite builds the real cascade CLI and
// drives `cascade promote finalize --commit-push` against a scripted GitHub
// Contents API whose trunk advances between this finalize's read and its PUT,
// the concurrent-wave shape the state-write retry loop exists for. Two
// finalizes race: a sibling prod finalize commits its leaf first (the stub's
// baked-in racer), then this staging finalize writes.
//
// The finalize under test must land a manifest carrying BOTH leaves. The two
// failure modes this guards against are a silent lost update (the client reads
// its content and its optimistic-lock SHA in separate calls, so a racer
// committing in between yields fresh-SHA-plus-stale-content and the PUT passes
// the lock while dropping the racer's keys) and a spurious hard failure
// (a one-shot writer that never retries the 409). Both leaves surviving with a
// zero exit proves the read-modify-write snapshot and the retry loop end to
// end through the shipped binary.
//
// Like the simulate what-if test, this exercises the binary with no containers:
// the act+gitea harness cannot reach this path because finalize only takes the
// Contents API route on real GitHub, so the gh surface is scripted instead.
func TestPromoteFinalizeMergesConcurrentStateWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the scripted gh stub needs a POSIX shell")
	}
	t.Parallel()

	projectRoot, err := filepath.Abs("..")
	require.NoError(t, err, "resolve project root")

	// Build the CLI for the host so the test can run it directly.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "cascade")
	build := exec.Command("go", "build", "-o", bin, "./cmd/cascade")
	build.Dir = projectRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cascade CLI: %v\n%s", err, out)
	}

	const baseManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - staging
      - prod
  state:
    dev:
      sha: devsha000000000000000000000000000000001
      version: v1.1.0-rc.1
`

	// The racer's committed trunk: a concurrent prod finalize landed its leaf
	// while this staging finalize was in flight.
	const racerManifest = baseManifest + `    prod:
      sha: prodracersha00000000000000000000000001
      version: v1.0.0
      committed_by: prod-finalize
`

	// A git repo holding the checked-out manifest: finalize diffs the on-disk
	// file against the committed tree before deciding to write.
	repoDir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "manifest.yaml"), []byte(baseManifest), 0o600))
	for _, args := range [][]string{
		{"add", "manifest.yaml"},
		{"commit", "-q", "-m", "seed manifest"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// The scripted gh and its backing state.
	stubDir := t.TempDir()
	stateDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "gh"), []byte(ghContentsAPIStub), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "content"), []byte(baseManifest), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "racer"), []byte(racerManifest), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "sha"), []byte("sha-base"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "puts"), []byte("0"), 0o600))

	promotionResult := `{"success":true,"is_cascade":false,` +
		`"promotions":[{"environment":"staging","source_env":"dev",` +
		`"sha":"stagingsha0000000000000000000000000001","version":"v1.1.0-rc.1","needs_deploy":false}],` +
		`"final_env":"staging"}`

	run := exec.Command(bin, "promote", "finalize",
		"--config", "manifest.yaml",
		"--promotion-result", promotionResult,
		"--commit-push",
	)
	run.Dir = repoDir
	run.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_STUB_DIR="+stateDir,
		"GITHUB_SERVER_URL=https://github.com",
		"GITHUB_REPOSITORY=owner/repo",
		"GITHUB_REF=refs/heads/main",
	)
	out, err := run.CombinedOutput()
	require.NoErrorf(t, err, "promote finalize failed:\n%s", out)

	final, err := os.ReadFile(filepath.Join(stateDir, "content"))
	require.NoError(t, err, "read final trunk manifest")
	got := string(final)

	require.Contains(t, got, "stagingsha0000000000000000000000000001",
		"this finalize's staging leaf must land on trunk")
	require.Contains(t, got, "prodracersha00000000000000000000000001",
		"the concurrent prod finalize's committed leaf must survive this write")
	require.Contains(t, got, "devsha000000000000000000000000000000001",
		"untouched sibling env state must survive")

	// The write must have converged through the optimistic lock: exactly one
	// successful PUT after observing the racer's commit.
	puts, err := os.ReadFile(filepath.Join(stateDir, "puts"))
	require.NoError(t, err)
	require.Equal(t, "1", strings.TrimSpace(string(puts)), "exactly one PUT may pass the optimistic lock")
}

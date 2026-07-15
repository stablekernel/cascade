package statewrite

import (
	"strings"
	"testing"
)

// ghNotFoundStub scripts a gh CLI answering a Contents API GET for a file that
// does not exist: the JSON error body on stdout and the CLI summary on stderr,
// exactly as gh reports an HTTP 404.
const ghNotFoundStub = `#!/bin/sh
echo '{"message":"Not Found","status":"404"}'
echo "gh: Not Found (HTTP 404)" >&2
exit 1
`

// ghRateLimitStub scripts a gh CLI failing with a non-404 API error, the shape
// of an exhausted rate limit, an auth failure, or a 5xx.
const ghRateLimitStub = `#!/bin/sh
echo "gh: API rate limit exceeded for installation (HTTP 403)" >&2
exit 1
`

// TestGHContents_GetContent_NotFoundIsTypedNotFoundError requires a real HTTP
// 404 to surface as a typed NotFoundError instead of file-absent (nil, "", nil)
// semantics. GitHub answers 404 both for a file that does not exist and for a
// private repository the token cannot read, so mapping the status to "absent"
// turns an access failure into blind retries that end in a misleading
// empty-manifest error with the real cause discarded.
func TestGHContents_GetContent_NotFoundIsTypedNotFoundError(t *testing.T) {
	installGHStub(t, ghNotFoundStub)

	content, sha, err := ghContents{}.GetContent("owner/repo", "manifest.yaml", "main")
	if err == nil {
		t.Fatal("GetContent() on a 404 = nil error, want a typed NotFoundError")
	}
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false, want true for an HTTP 404", err)
	}
	if content != nil || sha != "" {
		t.Fatalf("GetContent() on a 404 = (%q, %q), want (nil, \"\")", content, sha)
	}
	for _, want := range []string{"owner/repo", "manifest.yaml", "main", "token lacks repo access"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("GetContent() 404 error %q must mention %q so the operator sees where the lookup failed and why", err, want)
		}
	}
}

// TestGHContents_PutContent_RejectsEmptySHA pins the removal of the
// create-on-first-write contract: a state write only ever targets an already
// committed manifest, so an empty optimistic-lock SHA is a caller bug that
// must fail loudly instead of silently creating (or clobbering) the file.
func TestGHContents_PutContent_RejectsEmptySHA(t *testing.T) {
	installGHStub(t, "#!/bin/sh\nexit 0\n")

	err := ghContents{}.PutContent("owner/repo", "manifest.yaml", "main", "", "msg", []byte("x"), Identity{})
	if err == nil {
		t.Fatal("PutContent() with an empty sha = nil error, want a refusal (create-on-first-write is not supported)")
	}
	if !strings.Contains(err.Error(), "optimistic-lock sha") {
		t.Fatalf("PutContent() empty-sha error %q must name the missing optimistic-lock sha", err)
	}
}

// TestGHContents_GetContent_NonNotFoundFailureSurfaces requires every gh
// failure that is not a real 404 to propagate with the CLI's stderr attached.
// Mapping a rate limit, auth failure, or 5xx to "file absent" sends the retry
// loop into blind backoff and ends in a generic error with the actual cause
// discarded, and it makes the create-on-first-write branch indistinguishable
// from an outage.
func TestGHContents_GetContent_NonNotFoundFailureSurfaces(t *testing.T) {
	installGHStub(t, ghRateLimitStub)

	_, _, err := ghContents{}.GetContent("owner/repo", "manifest.yaml", "main")
	if err == nil {
		t.Fatal("GetContent() on an HTTP 403 = nil error, want the failure surfaced instead of file-absent semantics")
	}
	if !strings.Contains(err.Error(), "rate limit") && !strings.Contains(err.Error(), "403") {
		t.Fatalf("GetContent() error %q must carry the gh stderr detail", err)
	}
	if IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = true, want false: a 403 is transient and must stay retryable", err)
	}
}

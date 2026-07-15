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

// TestGHContents_GetContent_NotFoundMeansAbsent keeps the create-on-first-write
// contract: a genuine HTTP 404 reads as file-absent (nil bytes, empty SHA, nil
// error) so the first state write creates the manifest instead of failing.
func TestGHContents_GetContent_NotFoundMeansAbsent(t *testing.T) {
	installGHStub(t, ghNotFoundStub)

	content, sha, err := ghContents{}.GetContent("owner/repo", "manifest.yaml", "main")
	if err != nil {
		t.Fatalf("GetContent() on a 404 = %v, want nil (create-on-first-write semantics)", err)
	}
	if content != nil || sha != "" {
		t.Fatalf("GetContent() on a 404 = (%q, %q), want (nil, \"\")", content, sha)
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
}

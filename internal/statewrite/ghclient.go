package statewrite

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ghContents is the production ContentsClient. It shells out to the gh CLI to
// read and write manifest blobs through the GitHub Contents REST API, producing
// signed (Verified) commits that, with a bypass-capable token, can update a
// protected trunk branch.
type ghContents struct{}

// NewGHClient returns the production ContentsClient backed by the gh CLI.
func NewGHClient() ContentsClient {
	return ghContents{}
}

// GetContent returns the current manifest bytes and blob SHA at ref, decoded
// from a SINGLE Contents API response so the bytes and the optimistic-lock
// token are one snapshot. Reading them in separate calls opens a torn-read
// window: a concurrent finalize that commits between the two calls yields stale
// content paired with the fresh SHA, and the subsequent PUT then passes the
// optimistic lock while silently dropping the racer's committed keys, which is
// precisely the lost update the retry loop exists to prevent.
//
// A real HTTP 404 (or a response carrying no blob SHA) returns a typed
// NotFoundError naming the repo, path, and ref. GitHub answers 404 both for a
// file that does not exist and for a private repository the token cannot read,
// so mapping the status to "file absent" would hide an access failure behind
// blind retries; the caller fails fast on it instead. Every other failure
// (auth, rate limit, 5xx, network) propagates with the gh CLI's stderr
// attached so it stays distinguishable and retryable.
func (ghContents) GetContent(repo, path, ref string) ([]byte, string, error) {
	apiPath := fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, path, ref)

	out, err := exec.Command("gh", "api", apiPath).Output()
	if err != nil {
		detail := ghFailureDetail(err, out)
		if isNotFound(detail) {
			return nil, "", &NotFoundError{Repo: repo, Path: path, Ref: ref, Detail: detail}
		}
		return nil, "", fmt.Errorf("gh api %s: %s: %w", apiPath, detail, err)
	}
	return decodeContentsSnapshot(out, repo, path, ref)
}

// ghFailureDetail collects the diagnostic text a failed gh invocation produced:
// the stderr captured on the ExitError plus whatever landed on stdout (gh
// prints the API's JSON error body there). Falls back to the bare error string
// so the detail is never empty.
func ghFailureDetail(err error, out []byte) string {
	var parts []string
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if s := strings.TrimSpace(string(exitErr.Stderr)); s != "" {
			parts = append(parts, s)
		}
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return err.Error()
	}
	return strings.Join(parts, "; ")
}

// isNotFound reports whether a gh failure's diagnostic text is a real HTTP 404,
// the only failure that legitimately means the file is absent.
func isNotFound(detail string) bool {
	return strings.Contains(detail, "HTTP 404") || strings.Contains(detail, "Not Found")
}

// contentsSnapshot is the subset of a Contents API GET response the state
// writer needs: the encoded body and the blob SHA it belongs to, taken from one
// response so the pair can never mix two commits.
type contentsSnapshot struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	SHA      string `json:"sha"`
}

// decodeContentsSnapshot parses one Contents API response into (bytes, sha).
// A response carrying no blob SHA offers nothing to lock the write against, so
// it reads as not-found rather than as content. A body the API declined to
// inline (encoding "none", returned for oversized files) is fetched through
// the blob endpoint keyed by the SAME snapshot SHA, so the bytes still belong
// to the SHA used as the optimistic-lock token.
func decodeContentsSnapshot(out []byte, repo, path, ref string) ([]byte, string, error) {
	var snap contentsSnapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		return nil, "", fmt.Errorf("parsing contents API response: %w", err)
	}
	if snap.SHA == "" {
		return nil, "", &NotFoundError{Repo: repo, Path: path, Ref: ref, Detail: "contents response carried no blob SHA"}
	}

	if snap.Content == "" && snap.Encoding != "" && snap.Encoding != "base64" {
		blobOut, err := exec.Command("gh", "api", fmt.Sprintf("repos/%s/git/blobs/%s", repo, snap.SHA)).Output()
		if err != nil {
			return nil, "", fmt.Errorf("fetching blob %s: %s: %w", snap.SHA, ghFailureDetail(err, blobOut), err)
		}
		var blob contentsSnapshot
		if err := json.Unmarshal(blobOut, &blob); err != nil {
			return nil, "", fmt.Errorf("parsing blob response for %s: %w", snap.SHA, err)
		}
		snap.Content = blob.Content
	}

	content, err := decodeBase64Body(snap.Content)
	if err != nil {
		return nil, "", fmt.Errorf("decoding content for blob %s: %w", snap.SHA, err)
	}
	return content, snap.SHA, nil
}

// decodeBase64Body decodes the newline-wrapped base64 body the Contents and
// blob endpoints return.
func decodeBase64Body(s string) ([]byte, error) {
	s = strings.NewReplacer("\n", "", "\r", "").Replace(s)
	return base64.StdEncoding.DecodeString(s)
}

// PutContent writes content at ref through the Contents API, guarded by sha as
// the optimistic-lock token. A state write only ever targets an already
// committed manifest, so an empty sha is a caller bug and is refused: without
// the lock the Contents API would create or blindly overwrite the file,
// exactly the unguarded write this package exists to prevent. It stamps author
// with both the commit author and committer so the state commit is attributed
// to the bot identity rather than the token owner, and classifies a 409
// optimistic-lock failure as a ConflictError so the retry loop recognizes it.
func (ghContents) PutContent(repo, path, ref, sha, message string, content []byte, author Identity) error {
	if sha == "" {
		return fmt.Errorf("refusing to write %s/%s@%s without an optimistic-lock sha: a state write only targets an already committed manifest", repo, path, ref)
	}
	author = author.OrDefault()
	b64 := base64.StdEncoding.EncodeToString(content)
	args := []string{
		"api", fmt.Sprintf("repos/%s/contents/%s", repo, path), "-X", "PUT",
		"-f", "message=" + message,
		"-f", "content=" + b64,
		"-f", "branch=" + ref,
		"-f", "author[name]=" + author.Name,
		"-f", "author[email]=" + author.Email,
		"-f", "committer[name]=" + author.Name,
		"-f", "committer[email]=" + author.Email,
		"-f", "sha=" + sha,
	}
	out, err := exec.Command("gh", args...).CombinedOutput()
	return classifyPutError(string(out), err)
}

// classifyPutError maps a gh PUT result to a typed error, applying the
// write-path taxonomy shared with the generated state-write shell
// (internal/generate/state_write.go) and internal/git's
// retryablePushRejection; keep the three classifiers in sync. A nil err is
// success. An optimistic-lock failure becomes a ConflictError so the retry
// loop re-fetches and re-applies; a transient failure (a rate limit, an HTTP
// 5xx, a transport blip with no HTTP status) becomes a TransientError so the
// same loop retries it with backoff; any other 4xx (401 revoked token, 403
// authorization, 404, 422 validation) is wrapped verbatim so the loop fails
// fast with the real cause.
//
// GitHub returns two 409 shapes for a stale write: a blob If-Match mismatch
// whose body carries "does not match", and a branch-ref compare-and-swap
// failure whose body reads "... is at X but expected Y ..." with no "does not
// match". Either lock marker alongside a 409 or "Conflict" status is
// recognized so both shapes drive a retry.
func classifyPutError(out string, err error) error {
	if err == nil {
		return nil
	}
	wrapped := fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	if (strings.Contains(out, "does not match") || strings.Contains(out, "is at")) &&
		(strings.Contains(out, "409") || strings.Contains(out, "Conflict")) {
		return &ConflictError{Err: wrapped}
	}
	if transientPutFailure(out) {
		return &TransientError{Err: wrapped}
	}
	return wrapped
}

// transientPutFailure mirrors the emitted shell classifier's arm order: rate
// limits are matched before the blanket 4xx test, so a rate-limited 403 or a
// 429 stays retryable while a plain 403 authorization refusal does not.
// GitHub's secondary and abuse limits surface as 403/429 with a rate-limit
// message, never as auth failures, which is why the message decides where a
// 403 lands. A bare 409 without a lock marker still retries (the status is a
// conflict even when the body carries neither lock shape), and anything with
// no 4xx status at all (an HTTP 5xx, a transport failure that never reached
// the API) defaults to transient, exactly like the shell's final arm.
func transientPutFailure(out string) bool {
	for _, marker := range []string{
		"HTTP 429", "Too Many Requests",
		"rate limit", "secondary rate", "abuse",
		"Retry-After", "retry-after",
		"HTTP 409", "Conflict",
	} {
		if strings.Contains(out, marker) {
			return true
		}
	}
	return !strings.Contains(out, "HTTP 4")
}

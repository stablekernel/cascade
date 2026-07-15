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
// A file that does not yet exist (a genuine HTTP 404, or an empty SHA in the
// response) returns nil bytes, an empty SHA, and a nil error so the first write
// creates it rather than failing. Every other failure (auth, rate limit, 5xx,
// network) propagates with the gh CLI's stderr attached: mapping an outage to
// "file absent" would hide the real cause behind blind retries and make the
// create-on-first-write branch indistinguishable from a failure.
func (ghContents) GetContent(repo, path, ref string) ([]byte, string, error) {
	apiPath := fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, path, ref)

	out, err := exec.Command("gh", "api", apiPath).Output()
	if err != nil {
		detail := ghFailureDetail(err, out)
		if isNotFound(detail) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("gh api %s: %s: %w", apiPath, detail, err)
	}
	return decodeContentsSnapshot(out, repo)
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
// An empty SHA reads as file-absent (create-on-first-write semantics). A body
// the API declined to inline (encoding "none", returned for oversized files) is
// fetched through the blob endpoint keyed by the SAME snapshot SHA, so the
// bytes still belong to the SHA used as the optimistic-lock token.
func decodeContentsSnapshot(out []byte, repo string) ([]byte, string, error) {
	var snap contentsSnapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		return nil, "", fmt.Errorf("parsing contents API response: %w", err)
	}
	if snap.SHA == "" {
		return nil, "", nil
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

// PutContent writes content at ref through the Contents API. When sha is
// non-empty the write is an update guarded by that optimistic-lock token; an
// empty sha creates the file. It stamps author with both the commit author and
// committer so the state commit is attributed to the bot identity rather than
// the token owner, and classifies a 409 optimistic-lock failure as a
// ConflictError so the retry loop recognizes it.
func (ghContents) PutContent(repo, path, ref, sha, message string, content []byte, author Identity) error {
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
	}
	if sha != "" {
		args = append(args, "-f", "sha="+sha)
	}
	out, err := exec.Command("gh", args...).CombinedOutput()
	return classifyPutError(string(out), err)
}

// classifyPutError maps a gh PUT result to a typed error. A nil err is success.
// An optimistic-lock failure becomes a ConflictError so the retry loop re-fetches
// and re-applies; any other failure is wrapped verbatim. GitHub returns two 409
// shapes for a stale write: a blob If-Match mismatch whose body carries "does not
// match", and a branch-ref compare-and-swap failure whose body reads "... is at X
// but expected Y ..." with no "does not match". Either lock marker alongside a
// 409 or "Conflict" status is recognized so both shapes drive a retry.
func classifyPutError(out string, err error) error {
	if err == nil {
		return nil
	}
	if (strings.Contains(out, "does not match") || strings.Contains(out, "is at")) &&
		(strings.Contains(out, "409") || strings.Contains(out, "Conflict")) {
		return &ConflictError{Err: fmt.Errorf("%s: %w", strings.TrimSpace(out), err)}
	}
	return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
}

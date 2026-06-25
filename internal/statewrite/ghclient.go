package statewrite

import (
	"encoding/base64"
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

// GetContent returns the current manifest bytes and blob SHA at ref. It fetches
// the raw file and the blob SHA in two gh calls. A file that does not yet exist
// (either gh call errors, or the SHA is empty) returns nil bytes, an empty SHA,
// and a nil error so the first write creates it rather than failing.
func (ghContents) GetContent(repo, path, ref string) ([]byte, string, error) {
	apiPath := fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, path, ref)

	raw, err := exec.Command("gh", "api", apiPath, "-H", "Accept: application/vnd.github.raw").Output()
	if err != nil {
		return nil, "", nil
	}

	shaOut, err := exec.Command("gh", "api", apiPath, "--jq", ".sha").Output()
	if err != nil {
		return nil, "", nil
	}
	sha := strings.TrimSpace(string(shaOut))
	if sha == "" {
		return nil, "", nil
	}
	return raw, sha, nil
}

// PutContent writes content at ref through the Contents API. When sha is
// non-empty the write is an update guarded by that optimistic-lock token; an
// empty sha creates the file. It stamps author with both the commit author and
// committer so the state commit is attributed to the bot identity rather than
// the token owner, and classifies a 409 optimistic-lock failure as a
// ConflictError so the retry loop recognizes it.
func (ghContents) PutContent(repo, path, ref, sha, message string, content []byte, author Identity) error {
	author = author.orDefault()
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
// A 409 optimistic-lock failure (the body carries "does not match" alongside a
// 409, "Conflict", or "is at" marker) becomes a ConflictError so the retry loop
// re-fetches and re-applies; any other failure is wrapped verbatim.
func classifyPutError(out string, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(out, "does not match") &&
		(strings.Contains(out, "409") || strings.Contains(out, "Conflict") || strings.Contains(out, "is at")) {
		return &ConflictError{Err: fmt.Errorf("%s: %w", strings.TrimSpace(out), err)}
	}
	return fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
}

package branchprotection

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultGitHubAPIURL is the public GitHub REST API base used when neither the
// --api-url flag nor the GITHUB_API_URL environment variable is set.
const defaultGitHubAPIURL = "https://api.github.com"

// applyTimeout bounds a single branch-protection PUT so a stalled apply cannot
// hang the CLI indefinitely.
const applyTimeout = 30 * time.Second

// applier issues the branch-protection PUT against a GitHub-compatible REST API.
// It is intentionally small so the apply path can be exercised hermetically by
// pointing baseURL at an httptest server.
type applier struct {
	client  *http.Client
	baseURL string
	token   string
}

// newApplier builds an applier for the given API base URL and token. The base
// URL has any trailing slash trimmed so endpoint joining stays unambiguous.
func newApplier(baseURL, token string) *applier {
	return &applier{
		client:  &http.Client{Timeout: applyTimeout},
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
	}
}

// resolveAPIURL picks the API base URL in precedence order: the explicit flag
// value, then GITHUB_API_URL, then the public GitHub default. A blank result is
// never returned.
func resolveAPIURL(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("GITHUB_API_URL"); env != "" {
		return env
	}
	return defaultGitHubAPIURL
}

// resolveRepo picks the target repository in precedence order: the explicit flag
// value, then GITHUB_REPOSITORY (the owner/repo GitHub Actions injects).
func resolveRepo(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("GITHUB_REPOSITORY")
}

// resolveToken picks the apply token in precedence order: the explicit flag
// value, then GITHUB_TOKEN. The apply path requires a token, so a blank result
// is surfaced as an error by the caller.
func resolveToken(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("GITHUB_TOKEN")
}

// apply PUTs the protection body to
// {baseURL}/repos/{repo}/branches/{branch}/protection with a Bearer token. Only
// the Protection object is sent; the operator_todo guidance is never part of the
// request. A non-2xx response is returned as an error that includes the status
// and a bounded slice of the response body so a 403 from an under-scoped token
// is legible to the operator.
func (a *applier) apply(ctx context.Context, repo, branch string, protection Protection) error {
	body, err := json.Marshal(protection)
	if err != nil {
		return fmt.Errorf("encoding protection body: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/branches/%s/protection", a.baseURL, repo, branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building branch-protection request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("applying branch protection to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	return fmt.Errorf("applying branch protection to %s: %s: %s",
		url, resp.Status, snippet(resp.Body))
}

// snippet reads up to 512 bytes of a response body for inclusion in an error so
// GitHub's own rejection message (for example a 403 "Resource not accessible")
// reaches the operator without risking an unbounded read.
func snippet(r io.Reader) string {
	const max = 512
	data, err := io.ReadAll(io.LimitReader(r, max))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

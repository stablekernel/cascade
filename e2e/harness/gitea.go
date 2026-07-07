package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/stablekernel/cascade/internal/taggrammar"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	AdminUsername = "testadmin"
	AdminPassword = "admin123"
	AdminEmail    = "admin@test.local"
)

// GiteaContainer wraps a Gitea container instance
type GiteaContainer struct {
	container  testcontainers.Container
	url        string
	adminToken string
}

// Repo represents a Gitea repository
type Repo struct {
	Name     string
	CloneURL string
	SSHURL   string
}

// NewGiteaContainer starts a new Gitea container
func NewGiteaContainer(ctx context.Context, networkName string, net *testcontainers.DockerNetwork) (*GiteaContainer, error) {
	var networks []string
	var networkAliases map[string][]string
	if net != nil && networkName != "" {
		networks = []string{networkName}
		networkAliases = map[string][]string{
			networkName: {"gitea"},
		}
	}

	req := testcontainers.ContainerRequest{
		Image:          "gitea/gitea:1.21",
		ExposedPorts:   []string{"3000/tcp"},
		Networks:       networks,
		NetworkAliases: networkAliases,
		Env: map[string]string{
			"GITEA__security__INSTALL_LOCK":        "true",
			"GITEA__server__ROOT_URL":              "http://localhost:3000",
			"GITEA__server__HTTP_PORT":             "3000",
			"GITEA__database__DB_TYPE":             "sqlite3",
			"GITEA__service__DISABLE_REGISTRATION": "true",
			"USER_UID":                             "1000",
			"USER_GID":                             "1000",
			"GITEA__DEFAULT__RUN_MODE":             "prod",
			"GITEA__server__DISABLE_SSH":           "true",
		},
		WaitingFor: wait.ForHTTP("/api/v1/version").
			WithPort("3000/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start gitea container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get container host: %w", err)
	}

	port, err := container.MappedPort(ctx, "3000/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get mapped port: %w", err)
	}

	url := fmt.Sprintf("http://%s:%s", host, port.Port())

	g := &GiteaContainer{
		container: container,
		url:       url,
	}

	// Create admin user and get token
	if err := g.setupAdmin(ctx); err != nil {
		_ = container.Terminate(ctx)
		return nil, err
	}

	return g, nil
}

// setupAdmin creates admin user and generates API token
func (g *GiteaContainer) setupAdmin(ctx context.Context) error {
	// Create admin user via CLI (run as git user)
	exitCode, reader, err := g.container.Exec(ctx, []string{
		"su-exec", "git", "gitea", "admin", "user", "create",
		"--username", AdminUsername,
		"--password", AdminPassword,
		"--email", AdminEmail,
		"--admin",
	})
	if err != nil {
		return fmt.Errorf("failed to create admin user: %w", err)
	}
	if exitCode != 0 {
		// Read output for debugging
		output := make([]byte, 1024)
		n, _ := reader.Read(output)
		return fmt.Errorf("admin user creation command exited with code %d: %s", exitCode, string(output[:n]))
	}

	g.adminToken = AdminUsername + ":" + AdminPassword

	// The CLI writes the user to SQLite directly while the gitea API server
	// runs in a separate process. /api/v1/version (used as the readiness probe)
	// returns OK before the API can authenticate the freshly-written admin.
	// observed in CI as "401 user does not exist [uid: 0, name: testadmin]"
	// on the first request. Poll an authenticated endpoint until it succeeds.
	return g.waitForAdminAuth(ctx, 30*time.Second)
}

// waitForAdminAuth polls /api/v1/user (which requires authentication) until
// the admin credentials are accepted or the deadline passes.
func (g *GiteaContainer) waitForAdminAuth(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastStatus string
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", g.url+"/api/v1/user", nil)
		if err != nil {
			return fmt.Errorf("build auth probe request: %w", err)
		}
		req.SetBasicAuth(AdminUsername, AdminPassword)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			lastStatus = resp.Status
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("admin auth not ready after %s (last status: %s)", timeout, lastStatus)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// URL returns the Gitea server URL
func (g *GiteaContainer) URL() string {
	return g.url
}

// InternalURL returns the internal URL for container-to-container communication
func (g *GiteaContainer) InternalURL() string {
	return "gitea:3000"
}

// AdminToken returns the admin authentication credential
func (g *GiteaContainer) AdminToken() string {
	return g.adminToken
}

// Container returns the underlying testcontainers.Container
func (g *GiteaContainer) Container() testcontainers.Container {
	return g.container
}

// newJSONRequest builds an authenticated gitea API request carrying the given
// JSON body. It is the request factory passed to doRetry: each call returns a
// fresh *http.Request with an unread body so a throttled call can be replayed.
// A nil body produces a request with no payload (used for GET/DELETE).
func (g *GiteaContainer) newJSONRequest(ctx context.Context, method, url string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", method, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth(AdminUsername, AdminPassword)
	return req, nil
}

// Terminate stops and removes the container
func (g *GiteaContainer) Terminate(ctx context.Context) error {
	return g.container.Terminate(ctx)
}

// CreateRepo creates a new repository in Gitea
func (g *GiteaContainer) CreateRepo(ctx context.Context, name string) (*Repo, error) {
	payload := map[string]interface{}{
		"name":           name,
		"private":        false,
		"auto_init":      true,
		"default_branch": "main",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", g.url+"/api/v1/user/repos", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create repo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create repo: %s - %s", resp.Status, string(respBody))
	}

	var result struct {
		Name     string `json:"name"`
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &Repo{
		Name:     result.Name,
		CloneURL: result.CloneURL,
		SSHURL:   result.SSHURL,
	}, nil
}

// CreateCommit creates a single commit containing all files via Gitea's
// multi-file contents API. Earlier this iterated the files map and made one
// API call per file, which produced one commit per file in random order. The
// orchestrator's change detection (`git diff HEAD~1 HEAD` rooted at HEAD)
// then saw only the last file's diff, so a "breaking change" touching both
// `src/app.ts` and `cdk/stack.ts` non-deterministically lost one trigger and
// skipped the matching build/deploy job.
func (g *GiteaContainer) CreateCommit(ctx context.Context, repo *Repo, message string, files map[string]string) (string, error) {
	return g.CreateCommitOnBranch(ctx, repo, "main", message, files)
}

// CreateCommitOnBranch creates a single commit containing all files on the
// given branch via Gitea's multi-file contents API. The contents API is atomic
// so all files land in one commit, preserving the orchestrator's change
// detection across multi-file edits.
func (g *GiteaContainer) CreateCommitOnBranch(ctx context.Context, repo *Repo, branch, message string, files map[string]string) (string, error) {
	if len(files) == 0 {
		return g.GetBranchSHA(ctx, repo, branch)
	}

	type fileOp struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Content   string `json:"content"`
		SHA       string `json:"sha,omitempty"`
	}

	// Sort paths for deterministic request shape (the API itself is atomic;
	// this keeps logs/replays stable).
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	ops := make([]fileOp, 0, len(files))
	for _, path := range paths {
		op, sha, err := g.classifyFileOp(ctx, repo, branch, path)
		if err != nil {
			return "", err
		}
		ops = append(ops, fileOp{
			Operation: op,
			Path:      path,
			Content:   base64.StdEncoding.EncodeToString([]byte(files[path])),
			SHA:       sha,
		})
	}

	payload := map[string]interface{}{
		"message": message,
		"branch":  branch,
		"files":   ops,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal change-files payload: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents", g.url, AdminUsername, repo.Name)
	resp, err := doRetry(ctx, func() (*http.Request, error) {
		return g.newJSONRequest(ctx, "POST", url, body)
	})
	if err != nil {
		return "", fmt.Errorf("change-files request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("change-files failed: %s - %s", resp.Status, string(respBody))
	}

	var result struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode change-files response: %w", err)
	}
	if result.Commit.SHA != "" {
		return result.Commit.SHA, nil
	}
	return g.GetBranchSHA(ctx, repo, branch)
}

// classifyFileOp determines whether a file needs a "create" or "update"
// operation on the given branch, and returns the existing SHA (required for
// updates).
func (g *GiteaContainer) classifyFileOp(ctx context.Context, repo *Repo, branch, path string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s?ref=%s", g.url, AdminUsername, repo.Name, path, branch), nil)
	if err != nil {
		return "", "", fmt.Errorf("build file-check request: %w", err)
	}
	req.SetBasicAuth(AdminUsername, AdminPassword)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("file-check request for %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		var existing struct {
			SHA string `json:"sha"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&existing); err != nil {
			return "", "", fmt.Errorf("decode existing file %s: %w", path, err)
		}
		return "update", existing.SHA, nil
	}
	return "create", "", nil
}

// getHeadSHA returns the current HEAD SHA of the main branch.
func (g *GiteaContainer) getHeadSHA(ctx context.Context, repo *Repo) (string, error) {
	return g.GetBranchSHA(ctx, repo, "main")
}

// GetBranchSHA returns the current HEAD commit SHA of the named branch.
func (g *GiteaContainer) GetBranchSHA(ctx context.Context, repo *Repo, branch string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/branches/%s", g.url, AdminUsername, repo.Name, branch), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get branch info: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to get branch info: %s - %s", resp.Status, string(respBody))
	}

	var result struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Commit.ID, nil
}

// CreateBranch creates a new branch named name starting from fromSHA. Gitea's
// branches API takes a starting commit SHA via old_ref_name set to the SHA, so
// the new branch points at the requested commit.
func (g *GiteaContainer) CreateBranch(ctx context.Context, repo *Repo, name, fromSHA string) error {
	payload := map[string]interface{}{
		"new_branch_name": name,
		"old_ref_name":    fromSHA,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal create-branch payload: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/branches", g.url, AdminUsername, repo.Name)
	resp, err := doRetry(ctx, func() (*http.Request, error) {
		return g.newJSONRequest(ctx, "POST", url, body)
	})
	if err != nil {
		return fmt.Errorf("create-branch request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create-branch failed: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// DeleteBranch removes the named branch from the repository.
func (g *GiteaContainer) DeleteBranch(ctx context.Context, repo *Repo, name string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/branches/%s", g.url, AdminUsername, repo.Name, name), nil)
	if err != nil {
		return fmt.Errorf("build delete-branch request: %w", err)
	}
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete-branch request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete-branch failed: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// ListBranches returns the names of all branches in the repository.
func (g *GiteaContainer) ListBranches(ctx context.Context, repo *Repo) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/branches", g.url, AdminUsername, repo.Name), nil)
	if err != nil {
		return nil, fmt.Errorf("build list-branches request: %w", err)
	}
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list-branches request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list-branches failed: %s - %s", resp.Status, string(respBody))
	}

	var results []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode list-branches response: %w", err)
	}

	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}

	return names, nil
}

// CreateTag creates a tag pointing to the given SHA
func (g *GiteaContainer) CreateTag(ctx context.Context, repo *Repo, tag, sha string) error {
	payload := map[string]interface{}{
		"tag_name": tag,
		"target":   sha,
		"message":  "Tag " + tag,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/tags", g.url, AdminUsername, repo.Name),
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create tag: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create tag: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// GetTags returns all tags in the repository
func (g *GiteaContainer) GetTags(ctx context.Context, repo *Repo) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/tags", g.url, AdminUsername, repo.Name), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get tags: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get tags: %s - %s", resp.Status, string(respBody))
	}

	var results []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	tags := make([]string, len(results))
	for i, r := range results {
		tags[i] = r.Name
	}

	return tags, nil
}

// GetFileContent retrieves file content from the main branch of the repository.
func (g *GiteaContainer) GetFileContent(ctx context.Context, repo *Repo, filepath string) (string, error) {
	return g.GetFileContentOnBranch(ctx, repo, filepath, "main")
}

// GetFileContentOnBranch retrieves file content from the given branch.
func (g *GiteaContainer) GetFileContentOnBranch(ctx context.Context, repo *Repo, filepath, branch string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s?ref=%s", g.url, AdminUsername, repo.Name, filepath, branch), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("file not found or error: %s", resp.Status)
	}

	var result struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	// Content is base64 encoded
	if result.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(result.Content)
		if err != nil {
			return "", fmt.Errorf("failed to decode base64 content: %w", err)
		}
		return string(decoded), nil
	}

	return result.Content, nil
}

// DeleteTag deletes a tag from the repository
func (g *GiteaContainer) DeleteTag(ctx context.Context, repo *Repo, tag string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/tags/%s", g.url, AdminUsername, repo.Name, tag), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete tag: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 204 No Content or 404 Not Found are both acceptable
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete tag: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// DeleteRCTags deletes all pre-release tags matching a base version under the
// given grammar (e.g., v1.0.0-rc.* by default, or v0.2.0-beta* for a custom
// token/separator). This simulates what the GitHub manage-release action does
// after publishing, so it must honor the same tag grammar the release feature
// honors. Pass taggrammar.Default() to reproduce the historical "-rc." behavior.
func (g *GiteaContainer) DeleteRCTags(ctx context.Context, repo *Repo, baseVersion string, spec taggrammar.Spec) error {
	tags, err := g.GetTags(ctx, repo)
	if err != nil {
		return fmt.Errorf("failed to get tags: %w", err)
	}

	for _, tag := range tags {
		// Check if this is a pre-release tag for the given base version.
		// e.g., under the default grammar "v1.0.0-rc.0", "v1.0.0-rc.1"
		// match base "v1.0.0".
		if isRCTagForBase(tag, baseVersion, spec) {
			if err := g.DeleteTag(ctx, repo, tag); err != nil {
				return fmt.Errorf("failed to delete RC tag %s: %w", tag, err)
			}
		}
	}

	return nil
}

// isRCTagForBase checks if a tag is a pre-release tag for a given base version
// under spec. The candidate prefix is derived from the grammar as
// baseVersion + "-" + token + separator, so the default grammar (token "rc",
// separator ".") yields the historical "-rc." prefix while a custom grammar
// (e.g. token "beta", separator "") yields "-beta". Everything after the prefix
// must be digits, so "v1.2.3-rc.0" matches base "v1.2.3" but nested tags such as
// "v1.2.3-rc.4.hotfix.1" and unrelated tags do not.
func isRCTagForBase(tag, baseVersion string, spec taggrammar.Spec) bool {
	// Tag must start with the base version + "-" + pre-release token + separator.
	prefix := baseVersion + "-" + spec.PreReleaseToken + spec.PreReleaseSeparator
	if len(tag) <= len(prefix) {
		return false
	}
	if tag[:len(prefix)] != prefix {
		return false
	}
	// Rest must be digits
	for _, c := range tag[len(prefix):] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// GiteaRelease holds summary information about a Gitea release.
type GiteaRelease struct {
	ID           int64
	TagName      string
	Name         string
	IsDraft      bool
	IsPrerelease bool
}

// CreatePR opens a pull request from head into base with the given title and
// body, applies any labels (creating each label on the repository first if it
// does not already exist), and returns the pull request index.
func (g *GiteaContainer) CreatePR(ctx context.Context, repo *Repo, head, base, title, body string, labels []string) (int64, error) {
	payload := map[string]interface{}{
		"head":  head,
		"base":  base,
		"title": title,
		"body":  body,
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal create-pr payload: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls", g.url, AdminUsername, repo.Name)
	resp, err := doRetry(ctx, func() (*http.Request, error) {
		return g.newJSONRequest(ctx, "POST", url, reqBody)
	})
	if err != nil {
		return 0, fmt.Errorf("create-pr request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("create-pr failed: %s - %s", resp.Status, string(respBody))
	}

	var result struct {
		Number int64 `json:"number"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode create-pr response: %w", err)
	}

	if len(labels) > 0 {
		labelIDs, err := g.ensureLabels(ctx, repo, labels)
		if err != nil {
			return 0, err
		}
		if err := g.applyLabels(ctx, repo, result.Number, labelIDs); err != nil {
			return 0, err
		}
	}

	return result.Number, nil
}

// ensureLabels returns the label IDs for the given label names, creating any
// label that does not already exist on the repository.
func (g *GiteaContainer) ensureLabels(ctx context.Context, repo *Repo, names []string) ([]int64, error) {
	existing, err := g.listLabels(ctx, repo)
	if err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(names))
	for _, name := range names {
		if id, ok := existing[name]; ok {
			ids = append(ids, id)
			continue
		}
		id, err := g.createLabel(ctx, repo, name)
		if err != nil {
			return nil, err
		}
		existing[name] = id
		ids = append(ids, id)
	}

	return ids, nil
}

// listLabels returns a map of label name to label ID for the repository.
func (g *GiteaContainer) listLabels(ctx context.Context, repo *Repo) (map[string]int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/labels", g.url, AdminUsername, repo.Name), nil)
	if err != nil {
		return nil, fmt.Errorf("build list-labels request: %w", err)
	}
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list-labels request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list-labels failed: %s - %s", resp.Status, string(respBody))
	}

	var results []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode list-labels response: %w", err)
	}

	out := make(map[string]int64, len(results))
	for _, r := range results {
		out[r.Name] = r.ID
	}

	return out, nil
}

// createLabel creates a single label on the repository and returns its ID.
func (g *GiteaContainer) createLabel(ctx context.Context, repo *Repo, name string) (int64, error) {
	payload := map[string]interface{}{
		"name":  name,
		"color": "#00aabb",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal create-label payload: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/labels", g.url, AdminUsername, repo.Name)
	resp, err := doRetry(ctx, func() (*http.Request, error) {
		return g.newJSONRequest(ctx, "POST", url, body)
	})
	if err != nil {
		return 0, fmt.Errorf("create-label request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("create-label failed: %s - %s", resp.Status, string(respBody))
	}

	var result struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode create-label response: %w", err)
	}

	return result.ID, nil
}

// applyLabels attaches the given label IDs to the issue/PR with the given index.
func (g *GiteaContainer) applyLabels(ctx context.Context, repo *Repo, index int64, labelIDs []int64) error {
	payload := map[string]interface{}{
		"labels": labelIDs,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal apply-labels payload: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/issues/%d/labels", g.url, AdminUsername, repo.Name, index)
	resp, err := doRetry(ctx, func() (*http.Request, error) {
		return g.newJSONRequest(ctx, "POST", url, body)
	})
	if err != nil {
		return fmt.Errorf("apply-labels request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("apply-labels failed: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// MergePR merges the pull request with the given index using the named merge
// style ("squash", "merge", or "rebase"). Gitea computes a pull request's
// mergeability asynchronously and returns 405 "Please try again later" if the
// merge is attempted before that check completes, so this waits for the PR to
// report mergeable before issuing the merge.
func (g *GiteaContainer) MergePR(ctx context.Context, repo *Repo, index int64, style string) error {
	if err := g.waitForMergeable(ctx, repo, index, 30*time.Second); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"Do": style,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal merge-pr payload: %w", err)
	}

	// The merge POST is the call most exposed to gitea's "Please try again
	// later" throttle under load, so it is wrapped in the bounded transient
	// retry. The retry is safe: gitea returns the 405 throttle BEFORE applying
	// the merge, so a re-issue cannot double-merge.
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d/merge", g.url, AdminUsername, repo.Name, index)
	resp, err := doRetry(ctx, func() (*http.Request, error) {
		return g.newJSONRequest(ctx, "POST", url, body)
	})
	if err != nil {
		return fmt.Errorf("merge-pr request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Gitea returns 200 OK on a successful merge.
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("merge-pr failed: %s - %s", resp.Status, string(respBody))
	}

	return nil
}

// waitForMergeable polls the pull request until Gitea reports it mergeable or
// the timeout elapses.
func (g *GiteaContainer) waitForMergeable(ctx context.Context, repo *Repo, index int64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		mergeable, err := g.prMergeable(ctx, repo, index)
		if err != nil {
			return err
		}
		if mergeable {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pull request %d not mergeable after %s", index, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// prMergeable returns whether Gitea currently considers the pull request
// mergeable.
func (g *GiteaContainer) prMergeable(ctx context.Context, repo *Repo, index int64) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d", g.url, AdminUsername, repo.Name, index), nil)
	if err != nil {
		return false, fmt.Errorf("build get-pr request: %w", err)
	}
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("get-pr request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("get-pr failed: %s - %s", resp.Status, string(respBody))
	}

	var result struct {
		Mergeable bool `json:"mergeable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decode get-pr response: %w", err)
	}

	return result.Mergeable, nil
}

// ListOpenPRs returns the indices of open pull requests targeting base. When
// label is non-empty, only pull requests carrying that label are returned.
func (g *GiteaContainer) ListOpenPRs(ctx context.Context, repo *Repo, base, label string) ([]int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls?state=open&base=%s&limit=50", g.url, AdminUsername, repo.Name, base), nil)
	if err != nil {
		return nil, fmt.Errorf("build list-prs request: %w", err)
	}
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list-prs request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list-prs failed: %s - %s", resp.Status, string(respBody))
	}

	var results []struct {
		Number int64 `json:"number"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode list-prs response: %w", err)
	}

	indices := make([]int64, 0, len(results))
	for _, pr := range results {
		// Gitea filters by base server-side, but guard in case of API drift.
		if base != "" && pr.Base.Ref != base {
			continue
		}
		if label != "" {
			matched := false
			for _, l := range pr.Labels {
				if l.Name == label {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		indices = append(indices, pr.Number)
	}

	return indices, nil
}

// CreateRelease creates a release in the repository and returns its ID.
func (g *GiteaContainer) CreateRelease(ctx context.Context, repo *Repo, tagName, name, body string, isDraft, isPrerelease bool) (int64, error) {
	payload := map[string]interface{}{
		"tag_name":   tagName,
		"name":       name,
		"body":       body,
		"draft":      isDraft,
		"prerelease": isPrerelease,
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal create-release payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/releases", g.url, AdminUsername, repo.Name),
		bytes.NewReader(reqBody))
	if err != nil {
		return 0, fmt.Errorf("build create-release request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("create-release request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("create-release failed: %s - %s", resp.Status, string(respBody))
	}

	var result struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode create-release response: %w", err)
	}

	return result.ID, nil
}

// GetReleases returns summary information for every release in the repository.
func (g *GiteaContainer) GetReleases(ctx context.Context, repo *Repo) ([]GiteaRelease, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/releases", g.url, AdminUsername, repo.Name), nil)
	if err != nil {
		return nil, fmt.Errorf("build get-releases request: %w", err)
	}
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get-releases request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get-releases failed: %s - %s", resp.Status, string(respBody))
	}

	var results []struct {
		ID         int64  `json:"id"`
		TagName    string `json:"tag_name"`
		Name       string `json:"name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode get-releases response: %w", err)
	}

	releases := make([]GiteaRelease, len(results))
	for i, r := range results {
		releases[i] = GiteaRelease{
			ID:           r.ID,
			TagName:      r.TagName,
			Name:         r.Name,
			IsDraft:      r.Draft,
			IsPrerelease: r.Prerelease,
		}
	}

	return releases, nil
}

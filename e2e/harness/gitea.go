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
	if len(files) == 0 {
		return g.getHeadSHA(ctx, repo)
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
		op, sha, err := g.classifyFileOp(ctx, repo, path)
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
		"branch":  "main",
		"files":   ops,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal change-files payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/contents", g.url, AdminUsername, repo.Name),
		bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build change-files request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(AdminUsername, AdminPassword)

	resp, err := http.DefaultClient.Do(req)
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
	return g.getHeadSHA(ctx, repo)
}

// classifyFileOp determines whether a file needs a "create" or "update"
// operation, and returns the existing SHA (required for updates).
func (g *GiteaContainer) classifyFileOp(ctx context.Context, repo *Repo, path string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s?ref=main", g.url, AdminUsername, repo.Name, path), nil)
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

// getHeadSHA returns the current HEAD SHA of the main branch
func (g *GiteaContainer) getHeadSHA(ctx context.Context, repo *Repo) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/branches/main", g.url, AdminUsername, repo.Name), nil)
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

// GetFileContent retrieves file content from the repository
func (g *GiteaContainer) GetFileContent(ctx context.Context, repo *Repo, filepath string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s?ref=main", g.url, AdminUsername, repo.Name, filepath), nil)
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

// DeleteRCTags deletes all RC tags matching a base version (e.g., v1.0.0-rc.*)
// This simulates what the GitHub manage-release action does after publishing
func (g *GiteaContainer) DeleteRCTags(ctx context.Context, repo *Repo, baseVersion string) error {
	tags, err := g.GetTags(ctx, repo)
	if err != nil {
		return fmt.Errorf("failed to get tags: %w", err)
	}

	for _, tag := range tags {
		// Check if this is an RC tag for the given base version
		// e.g., "v1.0.0-rc.0", "v1.0.0-rc.1" match base "v1.0.0"
		if isRCTagForBase(tag, baseVersion) {
			if err := g.DeleteTag(ctx, repo, tag); err != nil {
				return fmt.Errorf("failed to delete RC tag %s: %w", tag, err)
			}
		}
	}

	return nil
}

// isRCTagForBase checks if a tag is an RC tag for a given base version
// e.g., "v1.0.0-rc.0" is an RC tag for "v1.0.0"
func isRCTagForBase(tag, baseVersion string) bool {
	// Tag must start with the base version + "-rc."
	prefix := baseVersion + "-rc."
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

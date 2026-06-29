package branchprotection

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

// parseManifest parses the manifest at path so a test can compare the applied
// body against Build's output for the same config.
func parseManifest(t *testing.T, path string) *config.TrunkConfig {
	t.Helper()
	cfg, err := config.ParseWithKey(path, config.DefaultManifestKey)
	require.NoError(t, err)
	return cfg
}

// TestCommand_Apply_PutsProtectionBody stands up a mock GitHub API with httptest
// and drives the REAL command with --apply. It is the faithful hermetic proof of
// the apply path: it asserts cascade hits the exact branch-protection endpoint
// with the Bearer token and sends only the .protection object (never the
// operator_todo guidance), honoring --api-url.
//
// The act+gitea e2e harness cannot host this. Gitea does not implement GitHub's
// PUT /repos/{owner}/{repo}/branches/{branch}/protection; its branch-protection
// API is a different endpoint and JSON shape (/api/v1/repos/{owner}/{repo}/
// branch_protections). The same divergence is already acknowledged in the release
// package's isGitHubHost helper. So this mock-server integration test, not an act
// scenario, is the correct hermetic coverage for the apply.
func TestCommand_Apply_PutsProtectionBody(t *testing.T) {
	manifest := writeManifest(t)

	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotAccept string
		gotBody   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		gotBody = body
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"url":"https://example/protection"}`))
	}))
	defer srv.Close()

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", manifest,
		"--apply",
		"--token", "scoped-pat",
		"--repo", "octo/repo",
		"--branch", "main",
		"--api-url", srv.URL,
	})
	require.NoError(t, cmd.Execute())

	// Correct verb and endpoint.
	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/repos/octo/repo/branches/main/protection", gotPath)

	// Scoped token is sent as a Bearer credential.
	assert.Equal(t, "Bearer scoped-pat", gotAuth)
	assert.Equal(t, "application/vnd.github+json", gotAccept)

	// The request body is exactly the Protection object, never the wrapper or the
	// operator_todo guidance. Unmarshaling into the strict Protection type and
	// re-comparing against Build proves shape and content.
	var sentProtection Protection
	require.NoError(t, json.Unmarshal(gotBody, &sentProtection))

	cfg := parseManifest(t, manifest)
	want := Build(cfg, "main").Protection
	assert.Equal(t, want, sentProtection)

	// The guidance key must not leak into the PUT body.
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(gotBody, &raw))
	assert.NotContains(t, raw, "operator_todo")
	assert.Contains(t, raw, "required_status_checks")

	// Confirmation is printed; the JSON wrapper is not dumped to stdout on apply.
	assert.Contains(t, out.String(), "applied branch protection to octo/repo/branches/main")
	assert.NotContains(t, out.String(), "operator_todo")
}

// TestCommand_Apply_SurfacesForbidden proves a 403 from an under-scoped token is
// surfaced as an error that includes the status and GitHub's own message, rather
// than being swallowed.
func TestCommand_Apply_SurfacesForbidden(t *testing.T) {
	manifest := writeManifest(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
	}))
	defer srv.Close()

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", manifest,
		"--apply",
		"--token", "under-scoped",
		"--repo", "octo/repo",
		"--api-url", srv.URL,
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
	assert.Contains(t, err.Error(), "Resource not accessible")
}

// TestCommand_Apply_RequiresToken proves the apply fails fast with a usage error
// when no token is available, before any network call.
func TestCommand_Apply_RequiresToken(t *testing.T) {
	manifest := writeManifest(t)
	t.Setenv("GITHUB_TOKEN", "")

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", manifest,
		"--apply",
		"--repo", "octo/repo",
		"--api-url", "http://127.0.0.1:0",
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--apply requires a token")
}

// TestCommand_Apply_RequiresRepo proves the apply fails fast with a usage error
// when no repository is available, before any network call.
func TestCommand_Apply_RequiresRepo(t *testing.T) {
	manifest := writeManifest(t)
	t.Setenv("GITHUB_REPOSITORY", "")

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--config", manifest,
		"--apply",
		"--token", "scoped-pat",
		"--api-url", "http://127.0.0.1:0",
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--apply requires a repository")
}

// TestCommand_NoApply_MakesNoRequest proves the default path is unchanged: with no
// --apply flag cascade emits the JSON wrapper to stdout and never calls the API,
// even when an --api-url is provided.
func TestCommand_NoApply_MakesNoRequest(t *testing.T) {
	manifest := writeManifest(t)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", manifest, "--api-url", srv.URL})
	require.NoError(t, cmd.Execute())

	assert.False(t, called, "default path must not call the API")

	var p Payload
	require.NoError(t, json.Unmarshal(out.Bytes(), &p))
	assert.ElementsMatch(t,
		[]string{generate.SetupJobName, generate.FinalizeJobName},
		p.Protection.RequiredStatusChecks.Contexts)
}

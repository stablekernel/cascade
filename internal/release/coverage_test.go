package release

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManager_DefaultBaseURL(t *testing.T) {
	t.Setenv("GITHUB_API_URL", "")
	m := NewManager("owner/repo", "tok")
	assert.Equal(t, "https://api.github.com", m.baseURL)
	assert.Equal(t, "owner/repo", m.repo)
	assert.Equal(t, "tok", m.token)
	assert.NotNil(t, m.client)
	assert.NotNil(t, m.sleepFn)
}

func TestNewManager_RespectsAPIURLEnv(t *testing.T) {
	t.Setenv("GITHUB_API_URL", "https://ghe.example.com/api/v3/")
	m := NewManager("owner/repo", "tok")
	// Trailing slash is trimmed.
	assert.Equal(t, "https://ghe.example.com/api/v3", m.baseURL)
}

func TestNewManagerWithURL_TrimsTrailingSlash(t *testing.T) {
	m := NewManagerWithURL("owner/repo", "tok", "https://api.example.com/")
	assert.Equal(t, "https://api.example.com", m.baseURL)
	assert.Equal(t, "owner/repo", m.repo)
	assert.Equal(t, "tok", m.token)
	assert.NotNil(t, m.client)
	assert.NotNil(t, m.sleepFn)
}

func TestIsGitHubHost(t *testing.T) {
	assert.True(t, isGitHubHost("https://api.github.com"))
	assert.True(t, isGitHubHost("https://ghe.github.example.com"))
	assert.False(t, isGitHubHost("http://localhost:3000"))
	assert.False(t, isGitHubHost("http://gitea.local"))
}

func TestSplitVersionPrefix(t *testing.T) {
	tests := []struct {
		tag        string
		wantPrefix string
		wantErr    bool
	}{
		{"v1.2.3", "v", false},
		{"1.0.0", "", false},
		{"rel-1.0.0", "rel-", false},
		{"release/2.0.0", "release/", false},
		{"no-core-here", "", true},
		{"v1.2", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			prefix, v, err := splitVersionPrefix(tt.tag)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, v)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPrefix, prefix)
			require.NotNil(t, v)
		})
	}
}

func TestNewRequest_InvalidMethodErrors(t *testing.T) {
	m := NewManagerWithURL("owner/repo", "tok", "https://api.github.com")
	_, err := m.newRequest("BAD METHOD", "/releases", nil)
	require.Error(t, err)
}

func TestNewRequest_SetsHeaders(t *testing.T) {
	m := NewManagerWithURL("owner/repo", "tok", "https://api.github.com")
	req, err := m.newRequest("POST", "/releases", map[string]interface{}{"a": 1})
	require.NoError(t, err)
	assert.Equal(t, "application/vnd.github+json", req.Header.Get("Accept"))
	assert.Equal(t, "Bearer tok", req.Header.Get("Authorization"))
	assert.Equal(t, "2022-11-28", req.Header.Get("X-GitHub-Api-Version"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "https://api.github.com/repos/owner/repo/releases", req.URL.String())
}

func TestListTags_ParsesRefs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/git/refs/tags")
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"ref": "refs/tags/v1.0.0"},
			{"ref": "refs/tags/v1.1.0-rc.1"},
		})
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}
	tags, err := m.listTags()
	require.NoError(t, err)
	assert.Equal(t, []string{"v1.0.0", "v1.1.0-rc.1"}, tags)
}

func TestListTags_NotFoundReturnsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}
	tags, err := m.listTags()
	require.NoError(t, err)
	assert.Empty(t, tags)
}

func TestListTags_ServerErrorReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}
	_, err := m.listTags()
	require.Error(t, err)
}

func TestDeleteGitTag_AcceptsNoContentAndNotFound(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "DELETE", r.Method)
			w.WriteHeader(status)
		}))
		m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}
		err := m.deleteGitTag("v1.0.0-rc.1")
		server.Close()
		require.NoError(t, err, "status %d should be accepted", status)
	}
}

func TestDeleteGitTag_ServerErrorReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("nope"))
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}
	err := m.deleteGitTag("v1.0.0-rc.1")
	require.Error(t, err)
}

func TestCreateGitTag_SkipsNonGitHubHost(t *testing.T) {
	m := &Manager{client: http.DefaultClient, baseURL: "http://gitea.local", token: "t", repo: "owner/repo"}
	// No server is contacted on a non-GitHub host.
	require.NoError(t, m.createGitTag("v1.0.0", "abc123"))
}

func TestCreateGitTag_AcceptsCreatedAndExists(t *testing.T) {
	// A fresh ref create (201) succeeds outright. A 422 (ref already exists) is
	// accepted only when the existing tag already points at the requested commit,
	// which createGitTag confirms with a follow-up GET; the same-sha case is the
	// genuinely idempotent one. A 422 whose existing target DIFFERS is a distinct,
	// fail-closed case covered by TestManager_CreateGitTag_ExistingTagDifferentSHA.
	for _, status := range []int{http.StatusCreated, http.StatusUnprocessableEntity} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost:
				assert.Contains(t, r.URL.Path, "/git/refs")
				w.WriteHeader(status)
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/refs/tags/"):
				// The existing tag points at the same commit the cut targets.
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ref":    r.URL.Path,
					"object": map[string]any{"sha": "abc123", "type": "commit"},
				})
			default:
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		}))
		m := &Manager{client: server.Client(), baseURL: server.URL + "/github", token: "t", repo: "owner/repo"}
		err := m.createGitTag("v1.0.0", "abc123")
		server.Close()
		require.NoError(t, err, "status %d should be accepted", status)
	}
}

func TestCreateGitTag_ServerErrorReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("bad ref"))
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL + "/github", token: "t", repo: "owner/repo"}
	err := m.createGitTag("v1.0.0", "abc123")
	require.Error(t, err)
}

func TestApiRequest_NonSuccessReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}
	_, err := m.apiRequest("POST", "/releases", map[string]interface{}{"a": 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestApiRequest_InvalidJSONReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}
	_, err := m.apiRequest("GET", "/releases/1", nil)
	require.Error(t, err)
}

func TestFindRelease_DirectTagHit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/releases/tags/v1.0.0")
		_ = json.NewEncoder(w).Encode(GitHubRelease{ID: 42, TagName: "v1.0.0"})
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}
	rel, err := m.findRelease("v1.0.0", "")
	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.Equal(t, int64(42), rel.ID)
}

func TestFindRelease_DirectErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}
	_, err := m.findRelease("v1.0.0", "")
	require.Error(t, err)
}

func TestCleanupRCTags_InvalidPublishedVersionErrors(t *testing.T) {
	m := NewManagerWithURL("owner/repo", "t", "https://api.github.com")
	err := m.cleanupRCTags("not-a-version")
	require.Error(t, err)
}

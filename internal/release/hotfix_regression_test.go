package release

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The RC-shaped cleanup logic is intentionally blind to hotfix tags: hotfix
// versions use a nested .hotfix.M segment (or a plain patch bump) that the RC
// regexes do not match. These regression tests pin that contract so a future
// regex change cannot silently start deleting hotfix tags or drafts.

func TestParseRCTag_IgnoresHotfixTags(t *testing.T) {
	hotfixTags := []string{
		"v1.4.0-rc.2.hotfix.1",
		"v1.4.0-rc.2.hotfix.2",
		"v1.3.0-rc.10.hotfix.5",
	}
	for _, tag := range hotfixTags {
		t.Run(tag, func(t *testing.T) {
			base, rc, ok := parseRCTag(tag)
			assert.False(t, ok, "hotfix tag must not parse as an RC tag")
			assert.Equal(t, "", base)
			assert.Equal(t, -1, rc)
		})
	}
}

func TestCleanupRCTags_SkipsHotfixTags(t *testing.T) {
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/git/refs/tags":
			refs := []map[string]string{
				{"ref": "refs/tags/v1.4.0-rc.0"},
				{"ref": "refs/tags/v1.4.0-rc.1"},
				{"ref": "refs/tags/v1.4.0-rc.1.hotfix.1"}, // must be preserved
				{"ref": "refs/tags/v1.4.0-rc.1.hotfix.2"}, // must be preserved
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(refs)
		case r.Method == http.MethodDelete:
			// /repos/owner/repo/git/refs/tags/<tag>
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}

	err := m.cleanupRCTags("v1.4.0")
	require.NoError(t, err)

	// Only the two plain RC tags are deleted; both hotfix tags are preserved.
	assert.Len(t, deleted, 2)
	for _, p := range deleted {
		assert.NotContains(t, p, "hotfix", "cleanupRCTags must never delete a hotfix tag")
	}
}

func TestCleanupStaleDrafts_IgnoresHotfixDrafts(t *testing.T) {
	var deletedIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/releases":
			releases := []GitHubRelease{
				{ID: 1, TagName: "v1.4.0-rc.0", Name: "v1.4.0-rc.0", Draft: true},
				{ID: 2, TagName: "v1.4.0-rc.1", Name: "v1.4.0-rc.1", Draft: true},
				// A hotfix draft for the same base must be preserved.
				{ID: 3, TagName: "v1.4.0-rc.1.hotfix.1", Name: "v1.4.0-rc.1.hotfix.1", Draft: true},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(releases)
		case r.Method == http.MethodDelete:
			deletedIDs = append(deletedIDs, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}

	// Creating rc.2 cleans up rc.0 and rc.1 drafts but must leave the hotfix draft.
	err := m.cleanupStaleDrafts("test", "v1.4.0-rc.2")
	require.NoError(t, err)

	for _, p := range deletedIDs {
		assert.NotContains(t, p, "/releases/3", "the hotfix draft (id 3) must be preserved")
	}
}

// TestCleanupStaleDrafts_HotfixCurrentTagIsNoOp confirms that when the current
// tag is itself a hotfix tag, no draft cleanup runs at all (parseRCTag returns
// not-ok for the hotfix tag, so cleanup short-circuits).
func TestCleanupStaleDrafts_HotfixCurrentTagIsNoOp(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]GitHubRelease{})
	}))
	defer server.Close()

	m := &Manager{client: server.Client(), baseURL: server.URL, token: "t", repo: "owner/repo"}

	err := m.cleanupStaleDrafts("test", "v1.4.0-rc.2.hotfix.1")
	require.NoError(t, err)
	assert.False(t, called, "a hotfix current tag must short-circuit before listing drafts")
}

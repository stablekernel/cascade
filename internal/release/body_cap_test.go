package release

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capEnforcingServer mimics the GitHub Releases API body validation: any
// create/update carrying a body longer than maxReleaseBodyChars characters is
// rejected with the same 422 the live fleet observed. Bodies within the cap are
// accepted and the payload is recorded so a test can assert on what was sent.
func capEnforcingServer(t *testing.T, got *map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{})
			return
		}

		var payload map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

		if body, ok := payload["body"].(string); ok && utf8.RuneCountInString(body) > maxReleaseBodyChars {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Release",` +
				`"code":"custom","field":"body","message":"body is too long (maximum is 125000 characters)"}]}`))
			return
		}

		*got = payload
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(GitHubRelease{
			ID:      123,
			URL:     "https://api.github.com/repos/owner/repo/releases/123",
			HTMLURL: "https://github.com/owner/repo/releases/tag/v1.0.0-rc.1",
		})
	}))
}

// oversizeChangelog builds a line-structured changelog that exceeds the
// GitHub release body cap, mirroring a full-history changelog produced when
// previous_tag is empty.
func oversizeChangelog() string {
	var sb strings.Builder
	for i := 0; sb.Len() < maxReleaseBodyChars+5000; i++ {
		fmt.Fprintf(&sb, "- feat(scope): commit number %d landing a change (abc%04d)\n", i, i)
	}
	return sb.String()
}

// TestManager_Create_OverCapChangelog_TruncatesInsteadOf422 is the regression
// for the live-fleet failure: a full-history changelog pushed the release body
// past GitHub's 125,000-character limit and manage-release died with a 422,
// which in turn stranded the state write for every downstream job.
func TestManager_Create_OverCapChangelog_TruncatesInsteadOf422(t *testing.T) {
	var sent map[string]interface{}
	server := capEnforcingServer(t, &sent)
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL + "/github",
		token:   "test-token",
		repo:    "owner/repo",
	}

	result, err := manager.Manage(Options{
		Action:      ActionCreate,
		Environment: "dev",
		SHA:         "abc123",
		Tag:         "v1.0.0-rc.1",
		Changelog:   oversizeChangelog(),
	})

	require.NoError(t, err, "an over-cap changelog must be truncated, not rejected with a 422")
	assert.Equal(t, int64(123), result.ReleaseID)

	body := sent["body"].(string)
	assert.LessOrEqual(t, utf8.RuneCountInString(body), maxReleaseBodyChars,
		"the sent body must respect GitHub's release body cap")
	assert.Contains(t, body, "## Status: Deployed to Dev",
		"truncation must preserve the leading status line")
	assert.Contains(t, body, truncationNotice, "a truncated body must say so")
	assert.Contains(t, body, "https://github.com/owner/repo/commits/v1.0.0-rc.1",
		"a truncated body must link to the full history when no previous tag is known")
}

// TestManager_Update_OverCapChangelog_TruncatesInsteadOf422 covers the update
// sink, which composes a body from the same changelog input as create.
func TestManager_Update_OverCapChangelog_TruncatesInsteadOf422(t *testing.T) {
	var sent map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID: 123, TagName: "v1.0.0-rc.1", Draft: true,
			})
			return
		}
		var payload map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		if body, ok := payload["body"].(string); ok && utf8.RuneCountInString(body) > maxReleaseBodyChars {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Validation Failed"}`))
			return
		}
		sent = payload
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(GitHubRelease{ID: 123})
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL + "/github",
		token:   "test-token",
		repo:    "owner/repo",
		sleepFn: func(_ time.Duration) {},
	}

	_, err := manager.Manage(Options{
		Action:      ActionUpdate,
		Environment: "dev",
		SHA:         "abc123",
		Tag:         "v1.0.0-rc.1",
		Changelog:   oversizeChangelog(),
	})

	require.NoError(t, err)
	assert.LessOrEqual(t, utf8.RuneCountInString(sent["body"].(string)), maxReleaseBodyChars)
}

func TestCapReleaseBody(t *testing.T) {
	t.Run("under cap is returned unchanged", func(t *testing.T) {
		body := "## Changes\n- one\n- two\n"
		assert.Equal(t, body, capReleaseBody(body, "owner/repo", "v0.9.0", "v1.0.0"))
	})

	t.Run("truncates on a whole-line boundary", func(t *testing.T) {
		got := capReleaseBody(oversizeChangelog(), "owner/repo", "", "v1.0.0")
		require.LessOrEqual(t, utf8.RuneCountInString(got), maxReleaseBodyChars)

		// Every retained changelog line must be intact, never cut mid-line.
		lines := strings.Split(strings.TrimSpace(strings.Split(got, truncationNotice)[0]), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "- feat") {
				assert.True(t, strings.HasSuffix(line, ")"),
					"changelog line must not be cut mid-line: %q", line)
			}
		}
	})

	t.Run("uses a compare link when a previous tag is known", func(t *testing.T) {
		got := capReleaseBody(oversizeChangelog(), "owner/repo", "v0.9.0", "v1.0.0")
		assert.Contains(t, got, "https://github.com/owner/repo/compare/v0.9.0...v1.0.0")
	})

	t.Run("never splits a multi-byte rune", func(t *testing.T) {
		// Emoji and CJK text are multi-byte; a byte-wise truncation would
		// produce invalid UTF-8 here.
		var sb strings.Builder
		for i := 0; utf8.RuneCountInString(sb.String()) < maxReleaseBodyChars+2000; i++ {
			fmt.Fprintf(&sb, "- 変更 %d 🚀 について\n", i)
		}
		got := capReleaseBody(sb.String(), "owner/repo", "", "v1.0.0")

		assert.True(t, utf8.ValidString(got), "truncated body must remain valid UTF-8")
		assert.LessOrEqual(t, utf8.RuneCountInString(got), maxReleaseBodyChars,
			"the cap counts characters (runes) the way GitHub does, not bytes")
	})

	t.Run("a single over-cap line still yields a valid capped body", func(t *testing.T) {
		got := capReleaseBody(strings.Repeat("x", maxReleaseBodyChars+500), "owner/repo", "", "v1.0.0")
		assert.LessOrEqual(t, utf8.RuneCountInString(got), maxReleaseBodyChars)
		assert.Contains(t, got, truncationNotice)
		assert.True(t, utf8.ValidString(got))
	})
}

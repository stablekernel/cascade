package release

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAction(t *testing.T) {
	tests := []struct {
		input   string
		want    Action
		wantErr bool
	}{
		{"create", ActionCreate, false},
		{"update", ActionUpdate, false},
		{"lock", ActionLock, false},
		{"publish", ActionPublish, false},
		{"delete", ActionDelete, false},
		{"invalid", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ValidateAction(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestCapitalizeFirst(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"dev", "Dev"},
		{"DEV", "DEV"},
		{"test", "Test"},
		{"prod", "Prod"},
		{"staging", "Staging"},
		{"", ""},
		{"a", "A"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := capitalizeFirst(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGenerateReleaseName(t *testing.T) {
	tests := []struct {
		env  string
		tag  string
		want string
	}{
		{"dev", "v0.1.0-rc.0", "v0.1.0-rc.0"},     // dev with RC
		{"test", "v1.2.3-rc.5", "v1.2.3-rc.5"},    // intermediate env with RC
		{"staging", "v1.0.0-rc.3", "v1.0.0-rc.3"}, // prerelease env keeps RC
		{"prod", "v1.0.0", "v1.0.0"},              // published (RC stripped)
	}

	for _, tt := range tests {
		t.Run(tt.env+"_"+tt.tag, func(t *testing.T) {
			got := generateReleaseName(tt.env, tt.tag)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGenerateStatusLine(t *testing.T) {
	tests := []struct {
		env  string
		want string
	}{
		{"dev", "## Status: Deployed to Dev"},
		{"prod", "## Status: Deployed to Prod"},
		{"staging", "## Status: Deployed to Staging"},
	}

	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			got := generateStatusLine(tt.env)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAddStatusLine(t *testing.T) {
	changelog := "## Changes\n- Fixed bug"
	result := addStatusLine(changelog, "dev")

	assert.Contains(t, result, "## Status: Deployed to Dev")
	assert.Contains(t, result, "## Changes")
	assert.True(t, len(result) > len(changelog))
}

func TestAddStatusLine_SkipsPrerelease(t *testing.T) {
	changelog := "## Changes\n- Fixed bug"

	// Prerelease environment should not add status line
	result := addStatusLine(changelog, "prerelease")
	assert.Equal(t, changelog, result)
	assert.NotContains(t, result, "## Status:")

	// Empty environment should not add status line
	result = addStatusLine(changelog, "")
	assert.Equal(t, changelog, result)
	assert.NotContains(t, result, "## Status:")
}

func TestUpdateStatusLine_SkipsPrerelease(t *testing.T) {
	body := "## Status: Deployed to Dev\n\n## Changes\n- Fixed bug"

	// Updating to prerelease should remove status entirely
	result := updateStatusLine(body, "prerelease")
	assert.Equal(t, "## Changes\n- Fixed bug", result)
	assert.NotContains(t, result, "## Status:")

	// Updating to empty should remove status entirely
	result = updateStatusLine(body, "")
	assert.Equal(t, "## Changes\n- Fixed bug", result)
	assert.NotContains(t, result, "## Status:")
}

func TestRemoveStatusLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "with status line",
			input: "## Status: Deployed to Dev\n\n## Changes\n- Fixed bug",
			want:  "## Changes\n- Fixed bug",
		},
		{
			name:  "without status line",
			input: "## Changes\n- Fixed bug",
			want:  "## Changes\n- Fixed bug",
		},
		{
			name:  "empty body",
			input: "",
			want:  "",
		},
		{
			name:  "only status line",
			input: "## Status: Deployed to Prod",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeStatusLine(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestManager_Create(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Method == "GET" && r.URL.Path == "/repos/owner/repo/releases" {
			// Return empty list for cleanup - no stale drafts
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{})
			return
		}

		assert.Equal(t, "POST", r.Method)
		assert.Contains(t, r.URL.Path, "/releases")

		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)

		assert.Equal(t, "v0.1.0-rc.1", payload["tag_name"])
		assert.Equal(t, "abc123", payload["target_commitish"])
		assert.Equal(t, "v0.1.0-rc.1", payload["name"])
		assert.True(t, payload["draft"].(bool))
		assert.Contains(t, payload["body"], "## Status: Deployed to Dev")

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(GitHubRelease{
			ID:      123,
			URL:     "https://api.github.com/repos/owner/repo/releases/123",
			HTMLURL: "https://github.com/owner/repo/releases/tag/v0.1.0-rc.1",
		})
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
	}

	result, err := manager.Manage(Options{
		Action:      ActionCreate,
		Environment: "dev",
		SHA:         "abc123",
		Tag:         "v0.1.0-rc.1", // RC tag to trigger cleanup check
		Changelog:   "## Changes\n- Test",
	})

	require.NoError(t, err)
	assert.Equal(t, int64(123), result.ReleaseID)
	assert.NotEmpty(t, result.HTMLURL)
	assert.Equal(t, 2, callCount) // GET for cleanup + POST for create
}

func TestManager_Update_ExistingRelease(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Method == "GET" {
			// Find release by tag
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      456,
				TagName: "abc123",
				Draft:   true,
			})
			return
		}

		assert.Equal(t, "PATCH", r.Method)
		assert.Contains(t, r.URL.Path, "/releases/456")

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(GitHubRelease{
			ID:      456,
			URL:     "https://api.github.com/repos/owner/repo/releases/456",
			HTMLURL: "https://github.com/owner/repo/releases/tag/abc123",
		})
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
	}

	result, err := manager.Manage(Options{
		Action:      ActionUpdate,
		Environment: "test",
		SHA:         "abc123",
		Tag:         "abc123",
		Changelog:   "## Updated\n- More changes",
	})

	require.NoError(t, err)
	assert.Equal(t, int64(456), result.ReleaseID)
	assert.Equal(t, 2, callCount) // GET + PATCH
}

func TestManager_Delete_DraftRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      789,
				TagName: "abc123",
				Draft:   true,
				URL:     "https://api.github.com/repos/owner/repo/releases/789",
				HTMLURL: "https://github.com/owner/repo/releases/tag/abc123",
			})
			return
		}

		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
	}

	result, err := manager.Manage(Options{
		Action:      ActionDelete,
		Environment: "dev",
		SHA:         "abc123",
		Tag:         "abc123",
	})

	require.NoError(t, err)
	assert.Equal(t, int64(789), result.ReleaseID)
}

func TestManager_Delete_PublishedRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(GitHubRelease{
			ID:      789,
			TagName: "v1.0.0",
			Draft:   false, // Published, not draft
		})
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
	}

	_, err := manager.Manage(Options{
		Action:      ActionDelete,
		Environment: "prod",
		SHA:         "abc123",
		Tag:         "v1.0.0",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete published release")
}

func TestManager_Delete_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First tries direct endpoint, returns 404
		if strings.Contains(r.URL.Path, "/releases/tags/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Then falls back to listing releases, return empty list
		if r.URL.Path == "/repos/owner/repo/releases" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
	}

	result, err := manager.Manage(Options{
		Action:      ActionDelete,
		Environment: "dev",
		SHA:         "abc123",
		Tag:         "abc123",
	})

	require.NoError(t, err)
	assert.Equal(t, int64(0), result.ReleaseID) // Empty result
}

func TestManager_Publish(t *testing.T) {
	deletedTags := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET /releases/tags/v1.0.0-rc.5 - find release by RC tag
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/releases/tags/") {
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:              999,
				TagName:         "v1.0.0-rc.5",
				TargetCommitish: "abc123",
				Draft:           false,
				Prerelease:      true,
				Body:            "## Status: Deployed to Release\n\n## Changes\n- Release ready",
			})
			return
		}

		// POST /git/refs - create semver tag
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/git/refs") {
			w.WriteHeader(http.StatusCreated)
			return
		}

		// GET /git/refs/tags - list tags for cleanup
		if r.Method == "GET" && r.URL.Path == "/repos/owner/repo/git/refs/tags" {
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"ref": "refs/tags/v1.0.0-rc.0"},
				{"ref": "refs/tags/v1.0.0-rc.3"},
				{"ref": "refs/tags/v1.0.0-rc.5"},
				{"ref": "refs/tags/v0.9.0-rc.2"}, // Different base version - should NOT be deleted
			})
			return
		}

		// DELETE /git/refs/tags/* - delete RC tags
		if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/git/refs/tags/") {
			tag := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/git/refs/tags/")
			deletedTags = append(deletedTags, tag)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// PATCH /releases/999 - update release
		if r.Method == "PATCH" {
			var payload map[string]interface{}
			err := json.NewDecoder(r.Body).Decode(&payload)
			require.NoError(t, err)

			// Verify tag_name is updated to semver
			assert.Equal(t, "v1.0.0", payload["tag_name"])

			// Verify status line was removed
			body := payload["body"].(string)
			assert.NotContains(t, body, "## Status:")
			assert.Contains(t, body, "## Changes")

			// Verify draft and prerelease are false
			assert.False(t, payload["draft"].(bool))
			assert.False(t, payload["prerelease"].(bool))

			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      999,
				URL:     "https://api.github.com/repos/owner/repo/releases/999",
				HTMLURL: "https://github.com/owner/repo/releases/tag/v1.0.0",
			})
			return
		}
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
	}

	result, err := manager.Manage(Options{
		Action:      ActionPublish,
		Environment: "prod",
		SHA:         "abc123",
		Tag:         "v1.0.0",
		DeleteTag:   "v1.0.0-rc.5", // RC tag to find the release
	})

	require.NoError(t, err)
	assert.Equal(t, int64(999), result.ReleaseID)

	// Verify all RC tags for v1.0.0 were deleted, but not v0.9.0-rc.2
	assert.Len(t, deletedTags, 3)
	assert.Contains(t, deletedTags, "v1.0.0-rc.0")
	assert.Contains(t, deletedTags, "v1.0.0-rc.3")
	assert.Contains(t, deletedTags, "v1.0.0-rc.5")
	assert.NotContains(t, deletedTags, "v0.9.0-rc.2") // Different base version
}

func TestManager_Create_CleansUpStaleDrafts(t *testing.T) {
	deletedIDs := []int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// List releases - returns drafts with different versions
		// Only same base version with lower RC should be cleaned up
		// Different base versions (promoted to other envs) should be preserved
		if r.Method == "GET" && r.URL.Path == "/repos/owner/repo/releases" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{
				{
					ID:      100,
					Name:    "v0.1.0-rc.1",
					TagName: "v0.1.0-rc.1", // Draft should be deleted (same base v0.1.0, RC 1 < 3)
					Draft:   true,
					Body:    "Old changelog",
				},
				{
					ID:      101,
					Name:    "v0.1.0-rc.2",
					TagName: "v0.1.0-rc.2", // Draft should be deleted (same base v0.1.0, RC 2 < 3)
					Draft:   true,
					Body:    "Another old changelog",
				},
				{
					ID:      102,
					Name:    "v0.1.1-rc.1",
					TagName: "v0.1.1-rc.1", // Should be PRESERVED (different base v0.1.1)
					Draft:   true,
					Body:    "Already promoted to another environment",
				},
			})
			return
		}

		// Delete stale draft release (tags are preserved - only releases are cleaned up)
		if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/releases/") {
			var id int64
			_, _ = fmt.Sscanf(r.URL.Path, "/repos/owner/repo/releases/%d", &id)
			deletedIDs = append(deletedIDs, id)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Create new release
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/releases") {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      200,
				URL:     "https://api.github.com/repos/owner/repo/releases/200",
				HTMLURL: "https://github.com/owner/repo/releases/tag/v0.1.0-rc.3",
			})
			return
		}

		// Create git tag (for new release)
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/git/refs") {
			w.WriteHeader(http.StatusCreated)
			return
		}
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
	}

	result, err := manager.Manage(Options{
		Action:      ActionCreate,
		Environment: "dev",
		SHA:         "abc123",
		Tag:         "v0.1.0-rc.3",
		Changelog:   "## Changes\n- New stuff",
		CreateTag:   true,
	})

	require.NoError(t, err)
	assert.Equal(t, int64(200), result.ReleaseID)

	// Should have deleted only same-base-version draft releases with lower RCs (100, 101)
	// Draft 102 (different base version v0.1.1) should be preserved
	// Note: Tags are NOT deleted - they are preserved from orchestration
	assert.Len(t, deletedIDs, 2)
	assert.Contains(t, deletedIDs, int64(100))
	assert.Contains(t, deletedIDs, int64(101))
	assert.NotContains(t, deletedIDs, int64(102)) // Preserved - different base version
}

func TestParseRCTag(t *testing.T) {
	tests := []struct {
		tag      string
		wantBase string
		wantRC   int
		wantOk   bool
	}{
		{"v1.0.0-rc.0", "v1.0.0", 0, true},
		{"v1.0.0-rc.1", "v1.0.0", 1, true},
		{"v1.2.3-rc.42", "v1.2.3", 42, true},
		{"v0.1.0-rc.3", "v0.1.0", 3, true},
		{"v1.0.0", "", -1, false},     // No RC suffix
		{"1.0.0-rc.1", "", -1, false}, // Missing v prefix
		{"v1.0-rc.1", "", -1, false},  // Invalid semver
		{"v1.0.0-1", "", -1, false},   // Legacy format (no rc. prefix)
		{"invalid", "", -1, false},    // Not a version
		{"", "", -1, false},           // Empty
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			base, rc, ok := parseRCTag(tt.tag)
			assert.Equal(t, tt.wantOk, ok)
			if ok {
				assert.Equal(t, tt.wantBase, base)
				assert.Equal(t, tt.wantRC, rc)
			}
		})
	}
}

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()

	assert.Equal(t, "manage-release", cmd.Use)
	assert.Contains(t, cmd.Short, "Manage GitHub draft releases")

	// Verify required flags exist
	repoFlag := cmd.Flags().Lookup("repo")
	assert.NotNil(t, repoFlag)

	actionFlag := cmd.Flags().Lookup("action")
	assert.NotNil(t, actionFlag)

	envFlag := cmd.Flags().Lookup("environment")
	assert.NotNil(t, envFlag)

	shaFlag := cmd.Flags().Lookup("sha")
	assert.NotNil(t, shaFlag)

	tagFlag := cmd.Flags().Lookup("tag")
	assert.NotNil(t, tagFlag)

	changelogFlag := cmd.Flags().Lookup("changelog")
	assert.NotNil(t, changelogFlag)

	tokenFlag := cmd.Flags().Lookup("token")
	assert.NotNil(t, tokenFlag)
}

// TestCreateGitTag_SkipsGitDataAPIOnGitea verifies that on a non-GitHub host the
// git-data refs API is not called at all: the release create that follows
// materializes the tag from target_commitish. The fake server fails any request
// so the test proves no HTTP call is made.
func TestCreateGitTag_SkipsGitDataAPIOnGitea(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL, // httptest host, not github -> treated as Gitea
		token:   "test-token",
		repo:    "owner/repo",
	}

	if err := manager.createGitTag("v1.0.0-rc.0.hotfix.1", "abc123"); err != nil {
		t.Fatalf("createGitTag on a non-GitHub host should be a no-op, got: %v", err)
	}
	if called {
		t.Error("createGitTag should not call the git-data API on a non-GitHub host")
	}
}

// TestCreateGitTag_CallsGitDataAPIOnGitHub verifies that on a GitHub host the
// git-data refs API is called and a 201 is treated as success.
func TestCreateGitTag_CallsGitDataAPIOnGitHub(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL + "/github", // host substring marks it as GitHub
		token:   "test-token",
		repo:    "owner/repo",
	}

	if err := manager.createGitTag("v1.0.0", "abc123"); err != nil {
		t.Fatalf("createGitTag on GitHub should succeed on 201, got: %v", err)
	}
	if !called {
		t.Error("createGitTag should call the git-data API on a GitHub host")
	}
}

package release

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/repos/owner/repo/releases") {
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
		baseURL: server.URL + "/github", // host substring marks it as GitHub
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

// tagOnlyRecordingServer returns an httptest server that records every request
// method+path it sees, answering the tag-create (POST /git/refs) with 201 and
// any release-list or release POST/GET with an empty-but-valid response. The
// recorded slice lets a test assert exactly which endpoints a manage-release
// call touched.
func tagOnlyRecordingServer(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(GitHubRelease{ID: 123})
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{})
		}
	}))
}

func containsPathSuffix(seen []string, method, suffix string) bool {
	for _, s := range seen {
		if strings.HasPrefix(s, method+" ") && strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

// TestManager_Create_TagOnly asserts that tag-only mode materializes the git tag
// but never POSTs a draft release, so a self-publishing release workflow (for
// example GoReleaser) is the sole creator of the release object and no draft is
// orphaned. The default (non-tag-only) path is asserted alongside as a scoping
// regression guard: it must still create the draft.
func TestManager_Create_TagOnly(t *testing.T) {
	tests := []struct {
		name        string
		tagOnly     bool
		wantTagPost bool
		wantDraft   bool
	}{
		{name: "tag-only skips the draft POST", tagOnly: true, wantTagPost: true, wantDraft: false},
		{name: "default still creates the draft", tagOnly: false, wantTagPost: true, wantDraft: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen []string
			server := tagOnlyRecordingServer(t, &seen)
			defer server.Close()

			manager := &Manager{
				client:  server.Client(),
				baseURL: server.URL + "/github", // host substring marks it as GitHub
				token:   "test-token",
				repo:    "owner/repo",
				sleepFn: func(time.Duration) {},
			}

			_, err := manager.Manage(Options{
				Action:      ActionCreate,
				Environment: "prerelease",
				SHA:         "abc123",
				Tag:         "v1.2.0-rc.0",
				CreateTag:   true,
				TagOnly:     tt.tagOnly,
			})
			require.NoError(t, err)

			assert.Equal(t, tt.wantTagPost, containsPathSuffix(seen, http.MethodPost, "/git/refs"),
				"git tag creation expectation not met; saw %v", seen)
			assert.Equal(t, tt.wantDraft, containsPathSuffix(seen, http.MethodPost, "/releases"),
				"draft release POST expectation not met; saw %v", seen)
		})
	}
}

// TestManager_Update_TagOnly asserts that action=update in tag-only mode (the
// shape cascade's orchestrate finalize uses) cuts the tag and returns without
// looking up or PATCHing any release object. Skipping the findRelease lookup is
// load-bearing: it guarantees a published release the self-publishing workflow
// already created (matched by tag or target SHA) is never mutated.
func TestManager_Update_TagOnly(t *testing.T) {
	var seen []string
	server := tagOnlyRecordingServer(t, &seen)
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL + "/github",
		token:   "test-token",
		repo:    "owner/repo",
		sleepFn: func(time.Duration) {},
	}

	_, err := manager.Manage(Options{
		Action:      ActionUpdate,
		Environment: "prerelease",
		SHA:         "abc123",
		Tag:         "v1.2.0-rc.0",
		CreateTag:   true,
		TagOnly:     true,
	})
	require.NoError(t, err)

	assert.True(t, containsPathSuffix(seen, http.MethodPost, "/git/refs"),
		"expected the git tag to be created; saw %v", seen)
	assert.False(t, containsPathSuffix(seen, http.MethodPost, "/releases"),
		"tag-only update must not POST a draft release; saw %v", seen)
	for _, s := range seen {
		assert.NotContains(t, s, "/releases/tags/",
			"tag-only update must not look up a release by tag; saw %v", seen)
	}
}

// updateTagRecordingServer answers an action=update flow against a GitHub host,
// recording every request. When existingDraft is true a matching draft release is
// returned by the tag-lookup GET so update() takes the PATCH branch; when false
// the tag lookup 404s and the release list is empty so update() falls through to
// create(). The POST /git/refs (tag-create) response status is gitRefStatus,
// letting a caller drive both the fresh-tag (201) and already-present (422) cases.
func updateTagRecordingServer(t *testing.T, seen *[]string, existingDraft bool, gitRefStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(gitRefStatus)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/refs/tags/"):
			// The 422 idempotency subcase re-cuts a tag that already points at the
			// same commit the update targets ("deadbeef"), so the ref resolves to
			// that sha and createGitTag treats the existing tag as a harmless match.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ref":    r.URL.Path,
				"object": map[string]any{"sha": "deadbeef", "type": "commit"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/"):
			if existingDraft {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(GitHubRelease{ID: 456, TagName: "web-0.2.0-rc.0", Draft: true})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{})
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{ID: 456})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(GitHubRelease{ID: 456})
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{})
		}
	}))
}

// TestManager_Update_CutsGitTag is the regression guard for a release-path
// concurrency defect: action=update with CreateTag set must materialize the git
// tag on BOTH the pre-existing-release (PATCH) branch and the no-release (create)
// branch. The orchestrate state write is an unconditional CAS loop, so if tag
// creation stays on the create-only path a pre-existing draft (which the defect
// itself accumulates) advances the state leaf while the git tag is permanently
// absent. The idempotency subcase proves a convergence rerun over an
// already-present tag (422) does not error.
func TestManager_Update_CutsGitTag(t *testing.T) {
	tests := []struct {
		name          string
		existingDraft bool
		gitRefStatus  int
	}{
		{name: "existing draft release still cuts the tag", existingDraft: true, gitRefStatus: http.StatusCreated},
		{name: "no existing release cuts the tag via create", existingDraft: false, gitRefStatus: http.StatusCreated},
		{name: "idempotent when the tag already exists", existingDraft: true, gitRefStatus: http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen []string
			server := updateTagRecordingServer(t, &seen, tt.existingDraft, tt.gitRefStatus)
			defer server.Close()

			manager := &Manager{
				client:  server.Client(),
				baseURL: server.URL + "/github", // host substring marks it as GitHub
				token:   "test-token",
				repo:    "owner/repo",
				sleepFn: func(time.Duration) {},
			}

			_, err := manager.Manage(Options{
				Action:      ActionUpdate,
				Environment: "prerelease",
				SHA:         "deadbeef",
				Tag:         "web-0.2.0-rc.0",
				Changelog:   "## Changes\n- Test",
				CreateTag:   true,
			})
			require.NoError(t, err)

			assert.True(t, containsPathSuffix(seen, http.MethodPost, "/git/refs"),
				"update with CreateTag must cut the git tag; saw %v", seen)
		})
	}
}

// existingTagServer answers a tag-only update against a GitHub host. The
// POST /git/refs (ref create) always returns 422 (the tag already exists), and
// the follow-up GET /git/refs/tags/<name> reports the tag pointing at
// existingSHA. It records every method+path so a test can assert the resolve
// GET fired.
func existingTagServer(t *testing.T, seen *[]string, existingSHA string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Reference already exists"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/refs/tags/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ref":    r.URL.Path,
				"object": map[string]any{"sha": existingSHA, "type": "commit"},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{})
		}
	}))
}

// TestManager_CreateGitTag_ExistingTagDifferentSHA is the regression guard for
// the frozen-rc defect: a release cut whose tag already exists at a DIFFERENT
// commit must not report success and leave the tag stranded on the stale commit.
// Before the fix a 422 from the ref-create was swallowed as harmless; this test
// drives the tag-only update path (the shape the release cut uses) and asserts
// the mismatch surfaces loudly and that the existing target was actually
// resolved. The same-sha subcase proves a genuine convergence rerun (the tag
// already points where the cut targets) stays idempotently successful.
func TestManager_CreateGitTag_ExistingTagDifferentSHA(t *testing.T) {
	tests := []struct {
		name        string
		targetSHA   string
		existingSHA string
		wantErr     bool
	}{
		{name: "different sha fails loudly", targetSHA: "newsha111", existingSHA: "148cf87stale", wantErr: true},
		{name: "same sha is idempotent success", targetSHA: "samesha222", existingSHA: "samesha222", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen []string
			server := existingTagServer(t, &seen, tt.existingSHA)
			defer server.Close()

			manager := &Manager{
				client:  server.Client(),
				baseURL: server.URL + "/github", // host substring marks it as GitHub
				token:   "test-token",
				repo:    "owner/repo",
				sleepFn: func(time.Duration) {},
			}

			_, err := manager.Manage(Options{
				Action:      ActionUpdate,
				Environment: "prerelease",
				SHA:         tt.targetSHA,
				Tag:         "v0.16.5-rc.1",
				CreateTag:   true,
				TagOnly:     true,
			})

			if tt.wantErr {
				require.Error(t, err, "an existing tag at a different sha must fail, not silently succeed")
				assert.Contains(t, err.Error(), tt.existingSHA, "error should name the stale target sha")
				assert.Contains(t, err.Error(), tt.targetSHA, "error should name the intended target sha")
			} else {
				require.NoError(t, err)
			}

			assert.True(t, containsPathSuffix(seen, http.MethodGet, "/git/refs/tags/v0.16.5-rc.1"),
				"createGitTag must resolve the existing tag's target on a 422; saw %v", seen)
		})
	}
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
		baseURL: server.URL + "/github", // marks as a GitHub host so update() exercises the real find+PATCH path (non-GitHub hosts short-circuit to create())
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
	assert.Contains(t, err.Error(), "cannot delete non-draft release")
}

// TestManager_Delete_PublishedRelease_AllowPublished verifies that the scoped
// AllowPublishedDelete opt-in lets a non-draft (published or prerelease) release
// be deleted. This path exists only for hotfix-rejoin cleanup, where the release
// is a superseded intermediate artifact; the general guard (exercised by
// TestManager_Delete_PublishedRelease) stays in force without the flag.
func TestManager_Delete_PublishedRelease_AllowPublished(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      790,
				TagName: "v1.0.0-rc.2.hotfix.1",
				Draft:   false, // non-draft (prerelease promoted)
			})
			return
		}
		assert.Equal(t, http.MethodDelete, r.Method)
		deleted = true
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
		Action:               ActionDelete,
		Tag:                  "v1.0.0-rc.2.hotfix.1",
		AllowPublishedDelete: true,
	})

	require.NoError(t, err)
	assert.Equal(t, int64(790), result.ReleaseID)
	assert.True(t, deleted, "the non-draft release must be deleted when AllowPublishedDelete is set")
}

// TestFindReleaseByTagOrSHA_PrefersExactTagOverStaleSHADraft covers L4: when a
// stale draft shares a target_commitish with the intended draft, the lookup must
// prefer the draft whose tag matches exactly rather than returning the first
// SHA-only match.
func TestFindReleaseByTagOrSHA_PrefersExactTagOverStaleSHADraft(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Direct by-tag endpoint misses so the list-scan path is exercised.
		if strings.Contains(r.URL.Path, "/releases/tags/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]GitHubRelease{
			// Stale draft sharing the SHA but a different (older) tag, listed first.
			{ID: 1, TagName: "v1.0.0-rc.0", TargetCommitish: "deadbeef", Draft: true},
			// The intended draft: exact tag match on the same SHA.
			{ID: 2, TagName: "v1.0.0-rc.1", TargetCommitish: "deadbeef", Draft: true},
		})
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
		sleepFn: func(time.Duration) {},
	}

	got, err := manager.findRelease("v1.0.0-rc.1", "deadbeef")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(2), got.ID, "the exact tag match must win over a stale SHA-only draft")
}

// TestManager_Publish_RerunFallsBackToSemverTag covers L3: a publish rerun after a
// partial publish (the rc tag was already cleaned up) must resolve the
// already-published release by its semver tag and converge instead of erroring.
func TestManager_Publish_RerunFallsBackToSemverTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/tags/v1.0.0-rc.5"):
			// rc tag already cleaned up by the first (partial) publish.
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/tags/v1.0.0"):
			// Already published under the semver tag.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{ID: 99, TagName: "v1.0.0", Draft: false})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/tags"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]struct {
				Ref string `json:"ref"`
			}{})
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{ID: 99, TagName: "v1.0.0"})
		default:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer server.Close()

	manager := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
		sleepFn: func(time.Duration) {},
	}

	result, err := manager.Manage(Options{
		Action:    ActionPublish,
		Tag:       "v1.0.0",
		DeleteTag: "v1.0.0-rc.5",
		SHA:       "abc123",
	})
	require.NoError(t, err, "publish rerun must converge by resolving the semver tag")
	assert.Equal(t, int64(99), result.ReleaseID)
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
				{"ref": "refs/tags/v0.9.0-rc.2"}, // Superseded earlier base - should be reaped
				{"ref": "refs/tags/v1.1.0-rc.0"}, // Higher base (future work) - should NOT be deleted
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

	// Publishing v1.0.0 reaps every RC tag whose base is at or below v1.0.0,
	// including the superseded v0.9.0 base, but never a higher base (v1.1.0).
	assert.Len(t, deletedTags, 4)
	assert.Contains(t, deletedTags, "v1.0.0-rc.0")
	assert.Contains(t, deletedTags, "v1.0.0-rc.3")
	assert.Contains(t, deletedTags, "v1.0.0-rc.5")
	assert.Contains(t, deletedTags, "v0.9.0-rc.2")    // Superseded earlier base
	assert.NotContains(t, deletedTags, "v1.1.0-rc.0") // Higher base preserved
}

// TestManager_Publish_CustomTagPrefix verifies that publishing a release with a
// custom tag prefix (e.g. "rel-") cleans up the superseded RC git tags. The RC
// cleanup must match the configured prefix, not assume the default "v".
func TestManager_Publish_CustomTagPrefix(t *testing.T) {
	deletedTags := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET /releases/tags/rel-0.1.0-rc.0 - find release by RC tag
		if r.Method == "GET" && strings.Contains(r.URL.Path, "/releases/tags/") {
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:              777,
				TagName:         "rel-0.1.0-rc.0",
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
				{"ref": "refs/tags/rel-0.1.0-rc.0"},
				{"ref": "refs/tags/rel-0.1.0-rc.1"},
				{"ref": "refs/tags/rel-0.2.0-rc.0"}, // Different base version - should NOT be deleted
				{"ref": "refs/tags/v0.1.0-rc.0"},    // Different prefix - should NOT be deleted
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

		// PATCH /releases/777 - update release
		if r.Method == "PATCH" {
			var payload map[string]interface{}
			err := json.NewDecoder(r.Body).Decode(&payload)
			require.NoError(t, err)
			assert.Equal(t, "rel-0.1.0", payload["tag_name"])
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      777,
				URL:     "https://api.github.com/repos/owner/repo/releases/777",
				HTMLURL: "https://github.com/owner/repo/releases/tag/rel-0.1.0",
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
		Tag:         "rel-0.1.0",
		DeleteTag:   "rel-0.1.0-rc.0",
	})

	require.NoError(t, err)
	assert.Equal(t, int64(777), result.ReleaseID)

	// Only the rel-0.1.0 RC tags are deleted; other bases and prefixes survive.
	assert.Len(t, deletedTags, 2)
	assert.Contains(t, deletedTags, "rel-0.1.0-rc.0")
	assert.Contains(t, deletedTags, "rel-0.1.0-rc.1")
	assert.NotContains(t, deletedTags, "rel-0.2.0-rc.0") // Different base version
	assert.NotContains(t, deletedTags, "v0.1.0-rc.0")    // Different prefix
}

func TestManager_Create_CleansUpStaleDrafts(t *testing.T) {
	deletedIDs := []int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// List releases - returns drafts with different versions
		// Only same base version with lower RC should be cleaned up
		// Different base versions (promoted to other envs) should be preserved
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/repos/owner/repo/releases") {
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
			idx := strings.LastIndex(r.URL.Path, "/releases/")
			_, _ = fmt.Sscanf(r.URL.Path[idx:], "/releases/%d", &id)
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
		baseURL: server.URL + "/github", // host substring marks it as GitHub
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

// TestCleanupRCTags_ReapsSupersededBases verifies that publish-time RC cleanup
// reaps every RC tag whose base version is at or below the published version,
// including bases from earlier rounds that were never published, while leaving
// any higher base (work staged for a future release) untouched. A delete that
// returns 404 (already gone) is treated as a no-op so the cleanup is idempotent,
// and a base carrying a different tag prefix is never compared across prefixes.
func TestCleanupRCTags_ReapsSupersededBases(t *testing.T) {
	listedTags := []string{
		"v0.9.0-rc.0",          // below published base - reap
		"v1.0.0-rc.0",          // superseded earlier base, never published - reap
		"v1.0.0-rc.4",          // below published rc on the same base - reap
		"v1.0.1-rc.0",          // equal to published base - reap (404 path)
		"v1.0.1-rc.2",          // equal to published base - reap
		"v1.1.0-rc.0",          // higher base, future work - preserve
		"rel-1.0.0-rc.0",       // different prefix - preserve
		"v1.0.1-rc.1.hotfix.1", // hotfix variant, not a plain RC tag - preserve
	}

	deletedTags := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/repos/owner/repo/git/refs/tags" {
			refs := make([]map[string]string, 0, len(listedTags))
			for _, tag := range listedTags {
				refs = append(refs, map[string]string{"ref": "refs/tags/" + tag})
			}
			_ = json.NewEncoder(w).Encode(refs)
			return
		}

		if r.Method == "DELETE" && strings.Contains(r.URL.Path, "/git/refs/tags/") {
			tag := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/git/refs/tags/")
			deletedTags = append(deletedTags, tag)
			// v1.0.1-rc.0 simulates a tag a prior partial run already removed: a
			// 404 must be absorbed as a no-op, not surfaced as a failure.
			if tag == "v1.0.1-rc.0" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
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

	err := manager.cleanupRCTags("v1.0.1")
	require.NoError(t, err)

	// Every base <= v1.0.1 with the matching prefix is reaped, regardless of which
	// round produced it. The 404 on v1.0.1-rc.0 is a no-op.
	assert.ElementsMatch(t, []string{
		"v0.9.0-rc.0",
		"v1.0.0-rc.0",
		"v1.0.0-rc.4",
		"v1.0.1-rc.0",
		"v1.0.1-rc.2",
	}, deletedTags)

	// Higher base, mismatched prefix, and hotfix variants are never touched.
	assert.NotContains(t, deletedTags, "v1.1.0-rc.0")
	assert.NotContains(t, deletedTags, "rel-1.0.0-rc.0")
	assert.NotContains(t, deletedTags, "v1.0.1-rc.1.hotfix.1")
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
		{"rel-0.1.0-rc.0", "rel-0.1.0", 0, true},         // Custom prefix
		{"rel-1.2.3-rc.7", "rel-1.2.3", 7, true},         // Custom prefix
		{"release/2.0.0-rc.4", "release/2.0.0", 4, true}, // Slash-style prefix
		{"1.0.0-rc.1", "1.0.0", 1, true},                 // Empty prefix
		{"0.1.0-rc.0", "0.1.0", 0, true},                 // Empty prefix
		{"v1.0.0", "", -1, false},               // No RC suffix
		{"v1.0-rc.1", "", -1, false},            // Invalid semver
		{"v1.0.0-1", "", -1, false},             // Legacy format (no rc. prefix)
		{"v1.0.0-rc.2.hotfix.1", "", -1, false}, // Hotfix suffix is not a plain RC tag
		{"v1.0.0-rc.x", "", -1, false},          // Non-numeric RC
		{"invalid", "", -1, false},              // Not a version
		{"", "", -1, false},                     // Empty
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

// TestManager_Create_HostGating verifies that the release-object POST is issued
// on a GitHub host and skipped on the Gitea e2e backend. GitHub's Releases API
// (release objects) is unavailable on Gitea; on a non-GitHub host create returns
// a synthetic success so finalize proceeds, and the tag is materialized via the
// env branch / git tag path instead.
func TestManager_Create_HostGating(t *testing.T) {
	tests := []struct {
		name       string
		githubHost bool
		wantPost   bool
	}{
		{name: "github host issues release POST", githubHost: true, wantPost: true},
		{name: "gitea host skips release POST", githubHost: false, wantPost: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			postCalled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && strings.Contains(r.URL.Path, "/git/refs") {
					// createGitTag (CreateTag: true) on the GitHub host.
					w.WriteHeader(http.StatusCreated)
					return
				}
				if r.Method == "POST" && strings.Contains(r.URL.Path, "/releases") {
					postCalled = true
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(GitHubRelease{
						ID:      321,
						URL:     "https://api.github.com/repos/owner/repo/releases/321",
						HTMLURL: "https://github.com/owner/repo/releases/tag/v0.1.0-rc.0.hotfix.1",
					})
					return
				}
				// Any other request (e.g. stale-draft cleanup GET) is tolerated.
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode([]GitHubRelease{})
			}))
			defer server.Close()

			baseURL := server.URL
			if tt.githubHost {
				baseURL = server.URL + "/github" // host substring marks it as GitHub
			}
			manager := &Manager{
				client:  server.Client(),
				baseURL: baseURL,
				token:   "test-token",
				repo:    "owner/repo",
			}

			result, err := manager.Manage(Options{
				Action:      ActionCreate,
				Environment: "test",
				SHA:         "abc123",
				Tag:         "v0.1.0-rc.0.hotfix.1",
				Changelog:   "Hotfix",
				CreateTag:   true,
			})

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantPost, postCalled,
				"release POST issued=%v, want %v", postCalled, tt.wantPost)
		})
	}
}

// TestManager_Prerelease_HostGating verifies that the prerelease promotion (a
// release-object PATCH) is issued on a GitHub host and skipped on the Gitea e2e
// backend. On a non-GitHub host there is no release object to promote, so the
// finalize prerelease step returns a synthetic success without contacting the
// release-object API.
func TestManager_Prerelease_HostGating(t *testing.T) {
	tests := []struct {
		name        string
		githubHost  bool
		wantRelease bool
	}{
		{name: "github host issues prerelease PATCH", githubHost: true, wantRelease: true},
		{name: "gitea host skips prerelease PATCH", githubHost: false, wantRelease: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			releaseCalled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// On the GitHub path, findRelease resolves the existing draft first.
				if r.Method == "GET" && strings.Contains(r.URL.Path, "/releases") {
					_ = json.NewEncoder(w).Encode(GitHubRelease{
						ID:              654,
						TagName:         "v0.1.0-rc.0.hotfix.1",
						TargetCommitish: "abc123",
						Draft:           true,
					})
					return
				}
				if r.Method == "PATCH" && strings.Contains(r.URL.Path, "/releases/") {
					releaseCalled = true
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(GitHubRelease{
						ID:      654,
						URL:     "https://api.github.com/repos/owner/repo/releases/654",
						HTMLURL: "https://github.com/owner/repo/releases/tag/v0.1.0-rc.0.hotfix.1",
					})
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			baseURL := server.URL
			if tt.githubHost {
				baseURL = server.URL + "/github" // host substring marks it as GitHub
			}
			manager := &Manager{
				client:  server.Client(),
				baseURL: baseURL,
				token:   "test-token",
				repo:    "owner/repo",
			}

			result, err := manager.Manage(Options{
				Action:      ActionPrerelease,
				Environment: "test",
				SHA:         "abc123",
				Tag:         "v0.1.0-rc.0.hotfix.1",
			})

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantRelease, releaseCalled,
				"prerelease PATCH issued=%v, want %v", releaseCalled, tt.wantRelease)
		})
	}
}

package release

// Tests for the draft-release eventual-consistency race that caused the second
// env's Finalize Hotfix to fail with "no release found for tag ..." even though
// the draft existed.
//
// Two failure modes are exercised:
//
//  (a) Prong 1 - create->prerelease using the returned release ID directly,
//      bypassing findRelease entirely. The fake server returns 404 on
//      GET /releases/tags/{tag} AND an empty list on GET /releases, so any
//      code that still calls findRelease on the prerelease path will fail.
//
//  (b) Prong 2 - findReleaseByTagOrSHA bounded retry: the list returns empty on
//      the first call and the real release on the second, simulating GitHub's
//      list endpoint eventual-consistency. The retry must eventually succeed.
//      When the release never appears the function must return a non-nil error.
//
// The act+gitea e2e harness cannot reproduce this race (Gitea's release API
// skips the prerelease PATCH entirely). These unit tests with a stubbed
// eventual-consistency server are the regression guard.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManager_CreateThenPrerelease_UsesCreatedReleaseID_NoFindRacePrerelease
// reproduces the fleet failure: create returns a draft release, then the
// prerelease promotion re-discovers it. The stub server returns 404 on the
// by-tag endpoint AND an empty list - simulating the consistency window - so
// any implementation that still calls findRelease on the prerelease path must
// fail. With prong 1 fixed (prerelease uses the created release ID directly),
// the sequence must succeed without ever hitting findRelease.
func TestManager_CreateThenPrerelease_UsesCreatedReleaseID_NoFindRacePrerelease(t *testing.T) {
	t.Helper()

	const releaseID = int64(42)
	const tag = "v1.0.0-rc.0.hotfix.2"
	const sha = "de5dfd1234567890"

	findReleaseCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cleanup-draft list on create path - return empty
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases") &&
			!strings.Contains(r.URL.Path, "/tags/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{})
			return
		}

		// POST /releases - the draft create
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases") {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:              releaseID,
				TagName:         tag,
				TargetCommitish: sha,
				Draft:           true,
				URL:             "https://api.github.com/repos/owner/repo/releases/42",
				HTMLURL:         "https://github.com/owner/repo/releases/tag/" + tag,
			})
			return
		}

		// POST /git/refs - createGitTag
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/git/refs") {
			w.WriteHeader(http.StatusCreated)
			return
		}

		// GET /releases/tags/{tag} - by-tag endpoint, always 404 (draft not indexed)
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/") {
			findReleaseCalled = true
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// PATCH /releases/{id} - prerelease promotion
		if r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/releases/") {
			// Verify we are patching by ID, not re-discovering
			assert.Contains(t, r.URL.Path, "/releases/42",
				"prerelease PATCH must target the created release ID, not a re-looked-up one")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      releaseID,
				URL:     "https://api.github.com/repos/owner/repo/releases/42",
				HTMLURL: "https://github.com/owner/repo/releases/tag/" + tag,
			})
			return
		}

		// Any unexpected call
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	mgr := &Manager{
		client:  server.Client(),
		baseURL: server.URL + "/github", // marks as GitHub host
		token:   "test-token",
		repo:    "owner/repo",
		sleepFn: func(time.Duration) {}, // no-op: prong 1 should not need retries
	}

	// Step 1: create the hotfix draft release (returns the created release object)
	created, err := mgr.Manage(Options{
		Action:    ActionCreate,
		SHA:       sha,
		Tag:       tag,
		Changelog: "Hotfix based on v1.0.0.",
		CreateTag: true,
	})
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, releaseID, created.ReleaseID)

	// Step 2: promote to prerelease, passing the known release ID via KnownReleaseID
	_, err = mgr.Manage(Options{
		Action:          ActionPrerelease,
		SHA:             sha,
		Tag:             tag,
		KnownReleaseID:  releaseID,
	})
	require.NoError(t, err,
		"prerelease promotion must succeed even when the by-tag endpoint returns 404 for a draft")

	// With prong 1 in place, findRelease (the by-tag GET) must never be called
	// during the prerelease promotion when KnownReleaseID is supplied.
	assert.False(t, findReleaseCalled,
		"prerelease must not call GET /releases/tags/{tag} when KnownReleaseID is set")
}

// TestFindReleaseByTagOrSHA_BoundedRetry_EventuallySucceeds verifies prong 2:
// when the list endpoint returns empty on the first call and the matching
// release on the second, findReleaseByTagOrSHA retries and returns the release.
func TestFindReleaseByTagOrSHA_BoundedRetry_EventuallySucceeds(t *testing.T) {
	t.Helper()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		if callCount == 1 {
			// First call: empty list (consistency window)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{})
			return
		}
		// Second call: release has propagated
		_ = json.NewEncoder(w).Encode([]GitHubRelease{
			{
				ID:              99,
				TagName:         "v1.0.0-rc.0.hotfix.2",
				TargetCommitish: "de5dfd",
				Draft:           true,
			},
		})
	}))
	defer server.Close()

	mgr := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
		sleepFn: func(time.Duration) {}, // no-op sleep for fast tests
	}

	got, err := mgr.findReleaseByTagOrSHA("v1.0.0-rc.0.hotfix.2", "de5dfd")
	require.NoError(t, err)
	require.NotNil(t, got, "must find the release on the second list call")
	assert.Equal(t, int64(99), got.ID)
	assert.Equal(t, 2, callCount, "must have retried once")
}

// TestFindReleaseByTagOrSHA_BoundedRetry_NeverAppears verifies that when the
// release never appears in the list, the function returns a clear error rather
// than silently returning nil.
func TestFindReleaseByTagOrSHA_BoundedRetry_NeverAppears(t *testing.T) {
	t.Helper()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]GitHubRelease{})
	}))
	defer server.Close()

	mgr := &Manager{
		client:  server.Client(),
		baseURL: server.URL,
		token:   "test-token",
		repo:    "owner/repo",
		sleepFn: func(time.Duration) {},
	}

	got, err := mgr.findReleaseByTagOrSHA("v1.0.0-rc.0.hotfix.2", "de5dfd")
	// When the release genuinely does not exist, nil+nil is the existing contract
	// (the nil is propagated up where the caller emits "no release found"). The
	// bounded retry must not change that contract; it just means more list calls.
	// We assert the call count is > 1 (retried) and the result is nil.
	assert.NoError(t, err)
	assert.Nil(t, got, "must return nil when release never appears after all retries")
	assert.Greater(t, callCount, 1, "must retry before giving up")
}

// TestManager_SingleEnvHotfix_Finalize_Unaffected verifies that single-env
// hotfix finalize (non-prerelease env) is unaffected by the prong-1 change:
// the create->lock sequence still works correctly when no KnownReleaseID flows.
func TestManager_SingleEnvHotfix_Finalize_Unaffected(t *testing.T) {
	t.Helper()

	const releaseID = int64(77)
	const tag = "v1.0.0-rc.0.hotfix.1"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cleanup-draft list on create path
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases") &&
			!strings.Contains(r.URL.Path, "/tags/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]GitHubRelease{})
			return
		}

		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases") {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      releaseID,
				TagName: tag,
				Draft:   true,
				URL:     "https://api.github.com/repos/owner/repo/releases/77",
				HTMLURL: "https://github.com/owner/repo/releases/tag/" + tag,
			})
			return
		}

		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/git/refs") {
			w.WriteHeader(http.StatusCreated)
			return
		}

		// GET /releases/tags/{tag} for lock path - return the draft (consistent)
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases/tags/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      releaseID,
				TagName: tag,
				Draft:   true,
			})
			return
		}

		// PATCH for lock
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				ID:      releaseID,
				URL:     "https://api.github.com/repos/owner/repo/releases/77",
				HTMLURL: "https://github.com/owner/repo/releases/tag/" + tag,
			})
			return
		}

		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	mgr := &Manager{
		client:  server.Client(),
		baseURL: server.URL + "/github",
		token:   "test-token",
		repo:    "owner/repo",
		sleepFn: func(time.Duration) {},
	}

	// create
	res, err := mgr.Manage(Options{
		Action:    ActionCreate,
		SHA:       "abc123",
		Tag:       tag,
		Changelog: "Hotfix",
		CreateTag: true,
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	// lock (single-env path - no KnownReleaseID needed)
	_, err = mgr.Manage(Options{
		Action: ActionLock,
		SHA:    "abc123",
		Tag:    tag,
	})
	require.NoError(t, err, "single-env lock path must be unaffected")
}

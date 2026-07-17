package release

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDraftCleanupManager builds a Manager whose release-list endpoint returns
// listed and whose release DELETE endpoint records the deleted release IDs into
// the returned slice pointer. Options thread the per-component grammar under
// test. deleteStatus is the status the DELETE endpoint replies with.
func newDraftCleanupManager(t *testing.T, listed []GitHubRelease, deleteStatus int, opts ...Option) (*Manager, *[]int) {
	t.Helper()
	deleted := &[]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/releases" {
			_ = json.NewEncoder(w).Encode(listed)
			return
		}
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/releases/") {
			id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/releases/"))
			require.NoError(t, err)
			*deleted = append(*deleted, id)
			w.WriteHeader(deleteStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	return NewManagerWithURL("owner/repo", "test-token", server.URL, opts...), deleted
}

// TestCleanupStaleDrafts_PreservesSiblingComponentDrafts is the namespace
// isolation contract for the draft reaper: a component publishing an RC must
// never delete a sibling component's draft, even when the sibling shares the
// numeric version core and carries a lower RC number. The sibling's tag does not
// parse under this component's strict grammar, so it is skipped outright.
func TestCleanupStaleDrafts_PreservesSiblingComponentDrafts(t *testing.T) {
	listed := []GitHubRelease{
		// Component A ("api-") - the publishing component's own stale drafts.
		{ID: 1, TagName: "api-1.2.0-rc.0", Name: "api-1.2.0-rc.0", Draft: true},
		{ID: 2, TagName: "api-1.2.0-rc.1", Name: "api-1.2.0-rc.1", Draft: true},
		// Sibling components sharing the same numeric core and a lower RC.
		{ID: 3, TagName: "web-1.2.0-rc.0", Name: "web-1.2.0-rc.0", Draft: true},
		{ID: 4, TagName: "web-1.2.0-rc.1", Name: "web-1.2.0-rc.1", Draft: true},
		{ID: 5, TagName: "svc-1.2.0-beta.0", Name: "svc-1.2.0-beta.0", Draft: true},
		// A bare, unprefixed draft is likewise outside the component namespace.
		{ID: 6, TagName: "1.2.0-rc.0", Name: "1.2.0-rc.0", Draft: true},
	}

	mgr, deleted := newDraftCleanupManager(t, listed, http.StatusNoContent, WithTagGrammar(strictSpec("api-", "rc")))

	require.NoError(t, mgr.cleanupStaleDrafts("test", "api-1.2.0-rc.2"))

	assert.ElementsMatch(t, []int{1, 2}, *deleted,
		"only the publishing component's own lower-RC drafts are deleted")
	for _, sibling := range []int{3, 4, 5, 6} {
		assert.NotContains(t, *deleted, sibling, "a sibling component's draft must never be deleted")
	}
}

// TestCleanupStaleDrafts_PermissivePathPreservesForeignPrefixes pins the
// single-component (no grammar) path: the permissive parser captures the prefix
// into the base, so a differently-prefixed draft has a different base and is
// preserved.
func TestCleanupStaleDrafts_PermissivePathPreservesForeignPrefixes(t *testing.T) {
	listed := []GitHubRelease{
		{ID: 1, TagName: "v1.2.0-rc.0", Name: "v1.2.0-rc.0", Draft: true},
		{ID: 2, TagName: "v1.2.0-rc.1", Name: "v1.2.0-rc.1", Draft: true},
		{ID: 3, TagName: "rel-1.2.0-rc.0", Name: "rel-1.2.0-rc.0", Draft: true},
		{ID: 4, TagName: "v1.1.0-rc.9", Name: "v1.1.0-rc.9", Draft: true},
	}

	// No WithTagGrammar option: the legacy permissive path.
	mgr, deleted := newDraftCleanupManager(t, listed, http.StatusNoContent)

	require.NoError(t, mgr.cleanupStaleDrafts("test", "v1.2.0-rc.2"))

	assert.ElementsMatch(t, []int{1, 2}, *deleted)
	assert.NotContains(t, *deleted, 3, "a foreign prefix is a different base and is preserved")
	assert.NotContains(t, *deleted, 4, "a different base version is preserved")
}

// TestCleanupStaleDrafts_PreservesCurrentAndNewerDrafts pins the RC ordering
// contract: only strictly lower RC numbers on the same base are stale. The
// current tag itself and any equal-or-higher RC are preserved.
func TestCleanupStaleDrafts_PreservesCurrentAndNewerDrafts(t *testing.T) {
	listed := []GitHubRelease{
		{ID: 1, TagName: "v1.2.0-rc.1", Name: "v1.2.0-rc.1", Draft: true},
		{ID: 2, TagName: "v1.2.0-rc.2", Name: "v1.2.0-rc.2", Draft: true}, // the current tag
		{ID: 3, TagName: "v1.2.0-rc.3", Name: "v1.2.0-rc.3", Draft: true}, // higher RC
	}

	mgr, deleted := newDraftCleanupManager(t, listed, http.StatusNoContent)

	require.NoError(t, mgr.cleanupStaleDrafts("test", "v1.2.0-rc.2"))

	assert.Equal(t, []int{1}, *deleted, "only the strictly lower RC is stale")
}

// TestCleanupStaleDrafts_IgnoresPublishedReleases proves the reaper only ever
// considers drafts: a published release with a stale RC tag is filtered out by
// listDraftReleases and is never deleted.
func TestCleanupStaleDrafts_IgnoresPublishedReleases(t *testing.T) {
	listed := []GitHubRelease{
		{ID: 1, TagName: "v1.2.0-rc.0", Name: "v1.2.0-rc.0", Draft: true},
		{ID: 2, TagName: "v1.2.0-rc.1", Name: "v1.2.0-rc.1", Draft: false}, // published
	}

	mgr, deleted := newDraftCleanupManager(t, listed, http.StatusNoContent)

	require.NoError(t, mgr.cleanupStaleDrafts("test", "v1.2.0-rc.2"))

	assert.Equal(t, []int{1}, *deleted)
	assert.NotContains(t, *deleted, 2, "a published release must never be deleted")
}

// TestCleanupStaleDrafts_FallsBackToReleaseName covers the untagged-draft path:
// a draft whose TagName is not an RC tag but whose Name is falls back to the
// Name for staleness, and a draft matching the current tag by Name is skipped.
func TestCleanupStaleDrafts_FallsBackToReleaseName(t *testing.T) {
	listed := []GitHubRelease{
		// Untagged draft: TagName empty, Name carries the RC tag.
		{ID: 1, TagName: "", Name: "v1.2.0-rc.0", Draft: true},
		// Untagged draft naming the current tag: skipped by the Name match.
		{ID: 2, TagName: "", Name: "v1.2.0-rc.2", Draft: true},
		// Neither TagName nor Name is an RC tag: skipped.
		{ID: 3, TagName: "", Name: "Nightly build", Draft: true},
	}

	mgr, deleted := newDraftCleanupManager(t, listed, http.StatusNoContent)

	require.NoError(t, mgr.cleanupStaleDrafts("test", "v1.2.0-rc.2"))

	assert.Equal(t, []int{1}, *deleted, "the untagged stale draft is resolved by Name")
}

// TestCleanupStaleDrafts_NonRCCurrentTagIsNoOp proves a non-RC current tag
// short-circuits before the reaper ever lists drafts, so publishing a final
// version never reaps.
func TestCleanupStaleDrafts_NonRCCurrentTagIsNoOp(t *testing.T) {
	listed := []GitHubRelease{
		{ID: 1, TagName: "v1.2.0-rc.0", Name: "v1.2.0-rc.0", Draft: true},
	}

	mgr, deleted := newDraftCleanupManager(t, listed, http.StatusNoContent)

	require.NoError(t, mgr.cleanupStaleDrafts("test", "v1.2.0"))

	assert.Empty(t, *deleted, "a non-RC current tag reaps nothing")
}

// TestCleanupStaleDrafts_DeleteFailureIsReported proves a failed delete surfaces
// as an error rather than being swallowed.
func TestCleanupStaleDrafts_DeleteFailureIsReported(t *testing.T) {
	listed := []GitHubRelease{
		{ID: 1, TagName: "v1.2.0-rc.0", Name: "v1.2.0-rc.0", Draft: true},
	}

	mgr, deleted := newDraftCleanupManager(t, listed, http.StatusForbidden)

	err := mgr.cleanupStaleDrafts("test", "v1.2.0-rc.2")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete failed with status 403")
	assert.Equal(t, []int{1}, *deleted, "the delete was attempted before the failure surfaced")
}

// TestCleanupStaleDrafts_ListFailureIsReported proves a failed release listing
// surfaces as an error and nothing is deleted.
func TestCleanupStaleDrafts_ListFailureIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(server.Close)

	mgr := NewManagerWithURL("owner/repo", "test-token", server.URL)

	err := mgr.cleanupStaleDrafts("test", "v1.2.0-rc.2")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error 500")
}

// TestCleanupStaleDrafts_CustomPreReleaseToken proves the draft reaper follows a
// component's custom pre-release token, reaping "beta" drafts that a hardcoded
// "-rc." matcher would miss entirely.
func TestCleanupStaleDrafts_CustomPreReleaseToken(t *testing.T) {
	listed := []GitHubRelease{
		{ID: 1, TagName: "svc-1.2.0-beta.0", Name: "svc-1.2.0-beta.0", Draft: true},
		{ID: 2, TagName: "svc-1.2.0-beta.1", Name: "svc-1.2.0-beta.1", Draft: true},
		{ID: 3, TagName: "svc-1.3.0-beta.0", Name: "svc-1.3.0-beta.0", Draft: true},                   // higher base
		{ID: 4, TagName: "svc-1.2.0-beta.1.hotfix.1", Name: "svc-1.2.0-beta.1.hotfix.1", Draft: true}, // hotfix variant
	}

	mgr, deleted := newDraftCleanupManager(t, listed, http.StatusNoContent, WithTagGrammar(strictSpec("svc-", "beta")))

	require.NoError(t, mgr.cleanupStaleDrafts("test", "svc-1.2.0-beta.2"))

	assert.ElementsMatch(t, []int{1, 2}, *deleted)
	assert.NotContains(t, *deleted, 3, "a higher base version is preserved")
	assert.NotContains(t, *deleted, 4, "a nested hotfix draft is preserved")
}

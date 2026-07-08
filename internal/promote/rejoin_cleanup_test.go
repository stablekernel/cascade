package promote

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/hotfix"
	"github.com/stablekernel/cascade/internal/release"
	"github.com/stretchr/testify/require"
)

// newStubbedCleaner builds a gitReleaseCleaner whose release operations target the
// given httptest server and whose git tag list/delete are replaced by the supplied
// stubs, so the cleanup's release-then-tag ordering can be exercised without a real
// remote.
func newStubbedCleaner(serverURL string, tags []string, deleteTag func(remote, name string) error) *gitReleaseCleaner {
	mgr := release.NewManagerWithURL("owner/repo", "token", serverURL)
	return &gitReleaseCleaner{
		remote:     "origin",
		releaseMgr: mgr,
		listTags:   func() ([]string, error) { return tags, nil },
		deleteTag:  deleteTag,
	}
}

// TestCleanHotfixReleases_PublishedRelease_Completes covers F1: a hotfix release
// that was promoted to a prerelease (non-draft) must still be deletable by the
// rejoin cleanup, which the general delete guard would otherwise reject.
func TestCleanHotfixReleases_PublishedRelease_Completes(t *testing.T) {
	const tag = "v1.4.0-rc.2.hotfix.1"
	releaseDeleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/tags/"+tag):
			w.WriteHeader(http.StatusOK)
			// Non-draft: the prerelease promotion patched draft:false.
			_ = json.NewEncoder(w).Encode(release.GitHubRelease{ID: 42, TagName: tag, Draft: false, Prerelease: true})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/releases/"):
			releaseDeleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer server.Close()

	tagDeleted := false
	cleaner := newStubbedCleaner(server.URL, []string{tag}, func(remote, name string) error {
		tagDeleted = true
		return nil
	})

	err := cleaner.CleanHotfixReleases(CleanReleasesRequest{Environment: "test", BaseVersion: "v1.4.0-rc.2"})
	require.NoError(t, err, "a non-draft hotfix release must be deletable by rejoin cleanup")
	require.True(t, releaseDeleted, "the non-draft hotfix release object must be deleted")
	require.True(t, tagDeleted, "the hotfix tag must be deleted after a successful release delete")
}

// TestCleanHotfixReleases_ReleaseDeleteError_KeepsTag covers F2: when the release
// delete fails, the matching tag must NOT be deleted, so a rerun can still resolve
// the release object by tag rather than orphaning a tagless release.
func TestCleanHotfixReleases_ReleaseDeleteError_KeepsTag(t *testing.T) {
	const tag = "v1.4.0-rc.2.hotfix.1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/tags/"+tag):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(release.GitHubRelease{ID: 42, TagName: tag, Draft: true})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/releases/"):
			// Transient server error on the release delete.
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer server.Close()

	tagDeleted := false
	cleaner := newStubbedCleaner(server.URL, []string{tag}, func(remote, name string) error {
		tagDeleted = true
		return nil
	})

	err := cleaner.CleanHotfixReleases(CleanReleasesRequest{Environment: "test", BaseVersion: "v1.4.0-rc.2"})
	require.Error(t, err, "a failed release delete must surface as an error")
	require.False(t, tagDeleted, "the tag must be preserved when the release delete fails, so a rerun can resolve it")
}

// TestCleanHotfixReleases_AlreadyDeleted_Idempotent covers the rerun path: when the
// release object is already gone (404) the cleanup deletes the tag and succeeds, so
// a second run after a partial failure converges.
func TestCleanHotfixReleases_AlreadyDeleted_Idempotent(t *testing.T) {
	const tag = "v1.4.0-rc.2.hotfix.1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/tags/"+tag):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]release.GitHubRelease{})
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer server.Close()

	tagDeleted := false
	cleaner := newStubbedCleaner(server.URL, []string{tag}, func(remote, name string) error {
		tagDeleted = true
		return nil
	})

	err := cleaner.CleanHotfixReleases(CleanReleasesRequest{Environment: "test", BaseVersion: "v1.4.0-rc.2"})
	require.NoError(t, err, "an already-deleted release must not error on rerun")
	require.True(t, tagDeleted, "the orphaned tag must still be cleaned up on rerun")
}

// failingTagCleaner records that DeleteEnvBranch was called and lets the test fail
// the hotfix-release cleanup of a specific env, to exercise the half-completed
// multi-env rejoin rerun (L1).
type failingTagCleaner struct {
	deletedBranches []string
	cleaned         []string
	failOnEnv       string
}

func (c *failingTagCleaner) DeleteEnvBranch(component, env string) error {
	c.deletedBranches = append(c.deletedBranches, hotfix.EnvBranchName(component, env))
	return nil
}

func (c *failingTagCleaner) CleanHotfixReleases(req CleanReleasesRequest) error {
	if req.Environment == c.failOnEnv {
		return fmt.Errorf("transient cleanup failure for %s", req.Environment)
	}
	c.cleaned = append(c.cleaned, req.Environment)
	return nil
}

// TestRunLifecycleCleanup_BestEffort_DoesNotStrandOtherEnvs covers L1: when one
// env's hotfix-release cleanup fails during a multi-env rejoin, the finalize must
// not abort. The remaining envs must still be cleaned, and Run must succeed so the
// already-persisted state is not wedged behind a disposable-object cleanup failure.
// Cleanup of disposable objects is best-effort; only state writes hard-fail.
func TestRunLifecycleCleanup_BestEffort_DoesNotStrandOtherEnvs(t *testing.T) {
	cleaner := &failingTagCleaner{failOnEnv: "test"}
	f := &Finalizer{
		cleaner: cleaner,
		pendingRejoins: []rejoinEvent{
			{env: "test", baseVersion: "v1.4.0-rc.2.hotfix.1"},
			{env: "uat", baseVersion: "v1.3.0-rc.5.hotfix.1"},
		},
	}

	err := f.runLifecycleCleanup()
	require.NoError(t, err, "a cleanup failure for one env must not abort finalize")

	require.Contains(t, cleaner.deletedBranches, "env/test")
	require.Contains(t, cleaner.deletedBranches, "env/uat", "the second env's branch must still be cleaned after the first env's cleanup error")
	require.Contains(t, cleaner.cleaned, "uat", "the second env's hotfix releases must still be cleaned")
}

package release

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPagedReleaseServer stands up a stub that paginates the release list the way
// api.github.com actually does: it honors per_page, slices the corpus by the
// page query parameter, and advertises the next page with a real
// `Link: <...>; rel="next"` header, omitting that header on the final page.
//
// Every pre-existing release stub encodes the whole corpus into a single
// response, which is precisely why unpaginated list calls read as correct under
// test. A stub that cannot paginate cannot catch a pagination bug.
func newPagedReleaseServer(t *testing.T, corpus []GitHubRelease, deleted *[]int) (*httptest.Server, *int32) {
	t.Helper()
	var pageRequests int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/releases" {
			atomic.AddInt32(&pageRequests, 1)

			// GitHub's default page size is 30 when per_page is absent. A caller
			// that omits per_page therefore sees at most 30 items.
			perPage := 30
			if v := r.URL.Query().Get("per_page"); v != "" {
				parsed, err := strconv.Atoi(v)
				require.NoError(t, err)
				perPage = parsed
			}
			page := 1
			if v := r.URL.Query().Get("page"); v != "" {
				parsed, err := strconv.Atoi(v)
				require.NoError(t, err)
				page = parsed
			}

			start := (page - 1) * perPage
			if start > len(corpus) {
				start = len(corpus)
			}
			end := start + perPage
			if end > len(corpus) {
				end = len(corpus)
			}

			// Advertise rel="next" only while a further page exists, exactly as
			// the real API does.
			if end < len(corpus) {
				next := fmt.Sprintf("%s/repos/owner/repo/releases?per_page=%d&page=%d",
					strings.TrimSuffix("http://"+r.Host, "/"), perPage, page+1)
				w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next", <%s>; rel="last"`, next, next))
			}
			_ = json.NewEncoder(w).Encode(corpus[start:end])
			return
		}
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/releases/") {
			id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/releases/"))
			require.NoError(t, err)
			*deleted = append(*deleted, id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	return server, &pageRequests
}

// buildReleaseCorpus returns filler releases followed by the stale drafts under
// test, mirroring the real ordering: /releases is newest-first, so a draft that
// has been superseded for a while sinks past the first page on any repository
// with an ordinary release history.
func buildReleaseCorpus(fillerCount int, stale ...GitHubRelease) []GitHubRelease {
	corpus := make([]GitHubRelease, 0, fillerCount+len(stale))
	for i := 0; i < fillerCount; i++ {
		corpus = append(corpus, GitHubRelease{
			ID:      int64(1000 + i),
			TagName: fmt.Sprintf("v9.%d.0", i),
			Name:    fmt.Sprintf("v9.%d.0", i),
			Draft:   false,
		})
	}
	corpus = append(corpus, stale...)
	return corpus
}

// TestListDraftReleases_FollowsLinkPagination pins the contract that the release
// lister walks every page. The corpus is 250 releases, which spans nine pages at
// GitHub's 30-item default and three at the maximum per_page=100, so no single
// request can cover it regardless of the page size the lister picks.
func TestListDraftReleases_FollowsLinkPagination(t *testing.T) {
	stale := []GitHubRelease{
		{ID: 1, TagName: "v1.2.0-rc.0", Name: "v1.2.0-rc.0", Draft: true},
		{ID: 2, TagName: "v1.2.0-rc.1", Name: "v1.2.0-rc.1", Draft: true},
	}
	corpus := buildReleaseCorpus(248, stale...)
	require.Len(t, corpus, 250)
	require.Greater(t, len(corpus), listPageSize,
		"corpus must exceed the maximum page size or the walk is untested")

	deleted := &[]int{}
	server, pageRequests := newPagedReleaseServer(t, corpus, deleted)
	mgr := NewManagerWithURL("owner/repo", "test-token", server.URL)

	drafts, err := mgr.listDraftReleases()
	require.NoError(t, err)

	// The drafts live at the tail of the corpus, past the first page under any
	// page size the lister could choose.
	assert.Len(t, drafts, 2, "lister must see drafts beyond the first page")
	assert.Greater(t, int(atomic.LoadInt32(pageRequests)), 1,
		"lister must issue more than one request to span a multi-page corpus")
}

// TestCleanupStaleDrafts_ReapsDraftsBeyondFirstPage is the consumer-level proof.
// The reaper is a no-op exactly where it is needed: on a repository with a long
// release history, the superseded drafts it exists to delete have sunk past the
// first page.
func TestCleanupStaleDrafts_ReapsDraftsBeyondFirstPage(t *testing.T) {
	stale := []GitHubRelease{
		{ID: 1, TagName: "v1.2.0-rc.0", Name: "v1.2.0-rc.0", Draft: true},
		{ID: 2, TagName: "v1.2.0-rc.1", Name: "v1.2.0-rc.1", Draft: true},
	}
	corpus := buildReleaseCorpus(248, stale...)

	deleted := &[]int{}
	server, _ := newPagedReleaseServer(t, corpus, deleted)
	mgr := NewManagerWithURL("owner/repo", "test-token", server.URL)

	require.NoError(t, mgr.cleanupStaleDrafts("test", "v1.2.0-rc.2"))

	assert.ElementsMatch(t, []int{1, 2}, *deleted,
		"stale drafts past the first page must still be reaped")
}

// TestListDraftReleases_FailsLoudlyMidPagination asserts that an error on a
// later page surfaces rather than returning the pages gathered so far. Silent
// truncation is the very defect this change removes; a partial list handed to
// the reaper would preserve drafts it was asked to delete while reporting
// success.
func TestListDraftReleases_FailsLoudlyMidPagination(t *testing.T) {
	var pageRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&pageRequests, 1)
		if n == 1 {
			next := fmt.Sprintf("http://%s/repos/owner/repo/releases?per_page=100&page=2", r.Host)
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next))
			_ = json.NewEncoder(w).Encode([]GitHubRelease{
				{ID: 1, TagName: "v1.2.0-rc.0", Name: "v1.2.0-rc.0", Draft: true},
			})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	t.Cleanup(server.Close)

	mgr := NewManagerWithURL("owner/repo", "test-token", server.URL)

	drafts, err := mgr.listDraftReleases()
	require.Error(t, err, "a mid-pagination failure must not be reported as success")
	assert.Nil(t, drafts, "a failed listing must not return a truncated page set")
}

// TestListDraftReleases_BoundsPathologicalLinkChain guards against a server (or
// proxy) whose rel="next" never terminates. Without a bound the lister would
// spin forever holding the release path hostage; the walk stops and reports the
// anomaly instead of hanging.
func TestListDraftReleases_BoundsPathologicalLinkChain(t *testing.T) {
	var pageRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pageRequests, 1)
		// Always advertise a next page: a self-perpetuating Link chain.
		next := fmt.Sprintf("http://%s/repos/owner/repo/releases?per_page=100&page=99", r.Host)
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next))
		_ = json.NewEncoder(w).Encode([]GitHubRelease{
			{ID: 1, TagName: "v1.2.0-rc.0", Name: "v1.2.0-rc.0", Draft: true},
		})
	}))
	t.Cleanup(server.Close)

	mgr := NewManagerWithURL("owner/repo", "test-token", server.URL)

	_, err := mgr.listDraftReleases()
	require.Error(t, err, "an unterminated Link chain must be reported, not followed forever")
	assert.LessOrEqual(t, int(atomic.LoadInt32(&pageRequests)), maxListPages+1,
		"the walk must stop at the page bound")
}

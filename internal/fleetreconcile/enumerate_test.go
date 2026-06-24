package fleetreconcile

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeStore is an in-memory run store that mimics `gh run list`: it holds runs
// (each with a databaseId and createdAt) and serves pages newest-first,
// restricted to runs whose createdAt is <= the inclusive upper bound the
// enumerator asks for. It records every bound it was queried with so a test can
// prove the enumerator advanced (and did not stall re-fetching one page).
type fakeStore struct {
	runs     []Run
	pageSize int
	calls    []string
	fetches  int
}

func newFakeStore(pageSize int, runs []Run) *fakeStore {
	cp := make([]Run, len(runs))
	copy(cp, runs)
	// Newest-first by createdAt, then by id, so equal timestamps have a stable
	// order the way the real API returns them.
	sort.SliceStable(cp, func(i, j int) bool {
		if cp[i].CreatedAt != cp[j].CreatedAt {
			return cp[i].CreatedAt > cp[j].CreatedAt
		}
		return cp[i].DatabaseID > cp[j].DatabaseID
	})
	return &fakeStore{runs: cp, pageSize: pageSize}
}

// fetch serves one newest-first page strictly older than the given cursor:
// runs whose (createdAt, databaseId) sorts strictly before the cursor. A zero
// cursor means no upper bound (newest runs). This mirrors a real fetcher that
// requests `gh run list --created ">=windowStart"` and slices by a descending
// (createdAt, id) cursor, which advances even through a tied-timestamp cluster.
func (s *fakeStore) fetch(cursor PageCursor) ([]Run, error) {
	s.fetches++
	s.calls = append(s.calls, cursor.CreatedAt)
	var page []Run
	for _, r := range s.runs {
		if !cursor.IsZero() && !olderThan(r, cursor) {
			continue
		}
		page = append(page, r)
		if len(page) == s.pageSize {
			break
		}
	}
	return page, nil
}

// olderThan reports whether run r sorts strictly after the cursor in
// newest-first order: an earlier createdAt, or the same createdAt with a
// smaller id. It is the test mirror of the enumerator's own ordering.
func olderThan(r Run, c PageCursor) bool {
	if r.CreatedAt != c.CreatedAt {
		return r.CreatedAt < c.CreatedAt
	}
	return r.DatabaseID < c.DatabaseID
}

func ids(runs []Run) []int64 {
	out := make([]int64, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.DatabaseID)
	}
	return out
}

// TestEnumerateRuns_BoundaryClusterFullyCaptured proves the strict-backward
// enumerator captures a boundary-timestamp CLUSTER larger than one page: many
// runs share the exact createdAt at the page boundary. The old `>=` page loop
// stalled here (since never advanced past the shared timestamp); the new
// enumerator must page through the whole cluster and return every run.
func TestEnumerateRuns_BoundaryClusterFullyCaptured(t *testing.T) {
	t.Parallel()

	// pageSize 3, but five runs share the boundary timestamp T2, which is the
	// oldest timestamp on the first full page. A timestamp-only advance with an
	// inclusive bound would keep re-serving the same cluster; the enumerator
	// must use the id cursor within a tied-timestamp cluster to make progress.
	const (
		windowStart = "2026-06-23T10:00:00Z"
		t1          = "2026-06-23T12:00:00Z"
		t2          = "2026-06-23T11:00:00Z" // the boundary cluster timestamp
	)
	runs := []Run{
		completedAt(1, "Orchestrate", "success", "main", t1),
		completedAt(2, "Orchestrate", "success", "main", t1),
		completedAt(10, "Orchestrate", "failure", "main", t2),
		completedAt(11, "Orchestrate", "failure", "main", t2),
		completedAt(12, "Orchestrate", "failure", "main", t2),
		completedAt(13, "Orchestrate", "failure", "main", t2),
		completedAt(14, "Orchestrate", "failure", "main", t2),
	}
	store := newFakeStore(3, runs)

	got, err := EnumerateRuns(windowStart, 3, 50, store.fetch)
	require.NoError(t, err)

	want := []int64{1, 2, 10, 11, 12, 13, 14}
	gotIDs := ids(got)
	sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
	require.Equal(t, want, gotIDs, "boundary cluster must be fully captured, not truncated")
}

// TestEnumerateRuns_CapOverflowFailsClosed proves that when the safety page cap
// is reached while the last page was still FULL (more runs may remain), the
// enumerator returns an ERROR instead of a truncated set. Never reconcile a
// window we did not fully enumerate.
func TestEnumerateRuns_CapOverflowFailsClosed(t *testing.T) {
	t.Parallel()

	// Far more runs than maxPages*pageSize can reach, all strictly inside the
	// window and each with a distinct descending timestamp so paging always
	// advances but never finishes within the cap.
	const windowStart = "2026-06-23T00:00:00Z"
	var runs []Run
	for i := 0; i < 100; i++ {
		// createdAt descends as i grows; all are > windowStart.
		ts := descendingTS(i)
		runs = append(runs, completedAt(int64(1000+i), "Orchestrate", "failure", "main", ts))
	}
	store := newFakeStore(5, runs)

	_, err := EnumerateRuns(windowStart, 5, 3, store.fetch) // cap of 3 pages = 15 runs max, window has 100
	require.Error(t, err, "reaching the page cap on a full page must fail closed")
	require.Contains(t, err.Error(), "truncat")
}

// TestEnumerateRuns_DedupesAcrossPages proves a run that appears on two adjacent
// pages (the inclusive boundary re-serves it) is counted once.
func TestEnumerateRuns_DedupesAcrossPages(t *testing.T) {
	t.Parallel()

	const (
		windowStart = "2026-06-23T00:00:00Z"
		t1          = "2026-06-23T12:00:00Z"
		t2          = "2026-06-23T11:00:00Z"
		t3          = "2026-06-23T10:00:00Z"
	)
	runs := []Run{
		completedAt(1, "Orchestrate", "success", "main", t1),
		completedAt(2, "Orchestrate", "success", "main", t2),
		completedAt(3, "Orchestrate", "failure", "main", t3),
	}
	store := newFakeStore(2, runs) // pageSize 2 forces a re-fetch with overlap

	got, err := EnumerateRuns(windowStart, 2, 50, store.fetch)
	require.NoError(t, err)

	gotIDs := ids(got)
	sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
	require.Equal(t, []int64{1, 2, 3}, gotIDs, "each run counted exactly once")
}

// stalledStore models a source that cannot page past a tied-timestamp cluster
// larger than one page: it always returns the newest pageSize runs at the
// boundary timestamp, ignoring the cursor's id half (as `gh run list --created`
// would when a single timestamp has more runs than --limit). The enumerator
// must fail closed rather than stop early and miss the older cluster runs.
type stalledStore struct {
	page []Run
}

func (s *stalledStore) fetch(_ PageCursor) ([]Run, error) { return s.page, nil }

// TestEnumerateRuns_UnpageableClusterFailsClosed proves that when a full page
// contributes no new runs (an oversized same-timestamp cluster the source
// cannot page through), the enumerator fails closed instead of silently
// returning a partial window.
func TestEnumerateRuns_UnpageableClusterFailsClosed(t *testing.T) {
	t.Parallel()

	const t1 = "2026-06-23T11:00:00Z"
	page := []Run{
		completedAt(3, "Orchestrate", "failure", "main", t1),
		completedAt(2, "Orchestrate", "failure", "main", t1),
	}
	store := &stalledStore{page: page} // pageSize 2, always returns the same 2

	_, err := EnumerateRuns("2026-06-23T00:00:00Z", 2, 50, store.fetch)
	require.Error(t, err, "an unpageable same-timestamp cluster must fail closed")
	require.Contains(t, err.Error(), "stall")
}

// TestEnumerateRuns_StopsAtWindowStart proves the enumerator stops once it has
// reached runs older than the window: out-of-window runs are excluded and the
// walk does not page forever.
func TestEnumerateRuns_StopsAtWindowStart(t *testing.T) {
	t.Parallel()

	const (
		windowStart = "2026-06-23T11:00:00Z"
		inWindow    = "2026-06-23T12:00:00Z"
		preWindow   = "2026-06-23T09:00:00Z"
	)
	runs := []Run{
		completedAt(1, "Orchestrate", "success", "main", inWindow),
		completedAt(2, "Orchestrate", "failure", "main", preWindow), // before window-start
	}
	store := newFakeStore(5, runs)

	got, err := EnumerateRuns(windowStart, 5, 50, store.fetch)
	require.NoError(t, err)
	require.Equal(t, []int64{1}, ids(got), "runs older than window-start are excluded")
}

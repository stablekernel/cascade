package fleetreconcile

import "fmt"

// PageCursor is a strict-backward paging position over runs ordered newest
// first by (CreatedAt, DatabaseID). A page fetched at a cursor returns only
// runs that sort strictly OLDER than the cursor: an earlier CreatedAt, or the
// same CreatedAt with a smaller DatabaseID. Using the (timestamp, id) pair as
// the cursor - rather than the timestamp alone - is what lets the walk advance
// through a cluster of runs that share one CreatedAt: a shared-timestamp
// boundary can never stall the loop, because the id half of the cursor still
// strictly decreases.
type PageCursor struct {
	CreatedAt string
	// DatabaseID is the run id at the cursor. Together with CreatedAt it forms
	// the strict lower bound: the next page is everything ordered after this
	// run in newest-first order.
	DatabaseID int64
}

// IsZero reports whether the cursor is the initial "no upper bound" position,
// i.e. the first page (newest runs).
func (c PageCursor) IsZero() bool { return c.CreatedAt == "" && c.DatabaseID == 0 }

// PageFetcher fetches one newest-first page of runs strictly older than the
// cursor. A zero cursor (PageCursor{}) means "the newest page, no upper bound".
// It returns at most a page-sized slice; a returned page shorter than the page
// size signals the source is exhausted. The real fetcher (in the cmd binary)
// shells out to `gh run list --created ">=<window-start>" --json ...` and
// slices the result by the cursor; tests inject a synthetic fetcher.
type PageFetcher func(cursor PageCursor) ([]Run, error)

// EnumerateRuns walks every run created at or after windowStart, strictly
// backward in (CreatedAt, DatabaseID) order, deduping by run id, and returns
// the full set. It is the fail-CLOSED replacement for the old inline 20-page
// `break` loop: it never returns a truncated window.
//
// windowStart is an ISO-8601 timestamp; runs with CreatedAt < windowStart are
// outside the scenario window and are excluded. pageSize is the fetcher's page
// limit (used to tell a final short page from a full one). maxPages caps the
// walk so a misbehaving source cannot loop forever.
//
// Fail-closed guarantee: if maxPages is reached while the last fetched page was
// still FULL (so more in-window runs may remain unenumerated), EnumerateRuns
// returns an error rather than a partial set. A truncated window could drop an
// unaccounted failing run and silently pass the gate, so truncation is always
// an error, never a quiet partial result.
func EnumerateRuns(windowStart string, pageSize, maxPages int, fetch PageFetcher) ([]Run, error) {
	if pageSize <= 0 {
		return nil, fmt.Errorf("pageSize must be positive, got %d", pageSize)
	}
	if maxPages <= 0 {
		return nil, fmt.Errorf("maxPages must be positive, got %d", maxPages)
	}

	seen := make(map[int64]struct{})
	var out []Run
	cursor := PageCursor{} // start at the newest page

	for page := 0; page < maxPages; page++ {
		runs, err := fetch(cursor)
		if err != nil {
			return nil, fmt.Errorf("fetching run page %d: %w", page+1, err)
		}

		reachedOlder := false // saw a run older than the window: walk is complete
		added := 0            // new in-window runs this page contributed
		var oldest *Run       // oldest (last, newest-first) run on this page, for the next cursor
		for i := range runs {
			r := runs[i]
			oldest = &runs[i]
			if r.CreatedAt < windowStart {
				// Older than the window: everything from here back is out of
				// scope, and the source is newest-first, so we are done.
				reachedOlder = true
				continue
			}
			if _, dup := seen[r.DatabaseID]; dup {
				continue
			}
			seen[r.DatabaseID] = struct{}{}
			out = append(out, r)
			added++
		}

		// The window is fully enumerated once we have either seen a run older
		// than window-start (the source is newest-first, so nothing older
		// remains in window) or received a short page (the source is exhausted).
		if reachedOlder || len(runs) < pageSize {
			return out, nil
		}

		// A full page that contributed no new in-window runs means the source
		// cannot advance past this position - a tied-timestamp cluster larger
		// than one page that the cursor's id half could not page through (the
		// source returned only runs we have already consumed). We cannot prove
		// the window is fully enumerated, so fail closed rather than stop early.
		if added == 0 {
			return nil, fmt.Errorf(
				"run enumeration stalled: a full page yielded no new runs, so a "+
					"same-timestamp run cluster exceeds the %d-run page size and cannot "+
					"be fully paged (refusing to reconcile a partial window since %s)",
				pageSize, windowStart)
		}

		// The page was full and entirely within the window: more runs may
		// remain. Advance the cursor strictly past the oldest run on this page.
		// Because the cursor carries the run id, a boundary cluster sharing one
		// CreatedAt cannot stall the walk - the id strictly decreases.
		if oldest == nil { // defensive: a full page is never empty
			return out, nil
		}
		cursor = PageCursor{CreatedAt: oldest.CreatedAt, DatabaseID: oldest.DatabaseID}
	}

	// Reached the page cap with the last page still full: the window may be
	// truncated. Fail closed - never reconcile a window we did not fully
	// enumerate.
	return nil, fmt.Errorf(
		"run enumeration truncated: hit the %d-page safety cap with a full final page; "+
			"more runs may remain in the window since %s (refusing to reconcile a partial window)",
		maxPages, windowStart)
}

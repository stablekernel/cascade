package main

import (
	"testing"

	"github.com/stablekernel/cascade/internal/fleetreconcile"
)

// TestSlicePage_FullServerPageShortSliceFailsClosed proves the gh fetcher
// fails closed when the server page hit --limit but the cursor slice came
// back short. In that state gh may have truncated older in-window runs
// beyond the limit (a same-second run cluster at least page-sized), so a
// short page would read to EnumerateRuns as "source exhausted" and silently
// truncate the window.
func TestSlicePage_FullServerPageShortSliceFailsClosed(t *testing.T) {
	const ts = "2026-06-23T00:00:05Z"
	cursor := fleetreconcile.PageCursor{CreatedAt: ts, DatabaseID: 5}
	// Server returned a FULL page (len == pageSize) of runs that all sit at
	// or newer than the cursor position: every one is already-consumed
	// boundary data, so the local slice is empty.
	all := []fleetreconcile.Run{
		{DatabaseID: 7, CreatedAt: ts},
		{DatabaseID: 6, CreatedAt: ts},
		{DatabaseID: 5, CreatedAt: ts},
	}

	_, err := slicePage(all, cursor, 3)
	if err == nil {
		t.Fatal("expected fail-closed error: full server page sliced short means gh --limit may have truncated older in-window runs")
	}
}

// TestSlicePage_ShortServerPageIsExhaustion proves a genuinely short server
// response (gh returned fewer runs than --limit) still pages through: the
// source really is exhausted, and the short slice is the correct signal.
func TestSlicePage_ShortServerPageIsExhaustion(t *testing.T) {
	const ts = "2026-06-23T00:00:05Z"
	cursor := fleetreconcile.PageCursor{CreatedAt: ts, DatabaseID: 5}
	all := []fleetreconcile.Run{
		{DatabaseID: 5, CreatedAt: ts},                     // consumed boundary run
		{DatabaseID: 4, CreatedAt: "2026-06-23T00:00:01Z"}, // strictly older: kept
	}

	page, err := slicePage(all, cursor, 3)
	if err != nil {
		t.Fatalf("short server page must not fail closed: %v", err)
	}
	if len(page) != 1 || page[0].DatabaseID != 4 {
		t.Fatalf("expected the single strictly-older run, got %+v", page)
	}
}

// TestSlicePage_FullSliceAdvances proves a full server page whose slice is
// also full passes through untouched (the enumerator will keep paging).
func TestSlicePage_FullSliceAdvances(t *testing.T) {
	cursor := fleetreconcile.PageCursor{CreatedAt: "2026-06-23T00:00:05Z", DatabaseID: 5}
	all := []fleetreconcile.Run{
		{DatabaseID: 4, CreatedAt: "2026-06-23T00:00:04Z"},
		{DatabaseID: 3, CreatedAt: "2026-06-23T00:00:03Z"},
	}

	page, err := slicePage(all, cursor, 2)
	if err != nil {
		t.Fatalf("full slice of a full page must not error: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("expected full page of 2, got %d", len(page))
	}
}

// TestSlicePage_SortsNewestFirst proves the slice is well defined even when
// gh returns runs out of (createdAt, id) order.
func TestSlicePage_SortsNewestFirst(t *testing.T) {
	all := []fleetreconcile.Run{
		{DatabaseID: 1, CreatedAt: "2026-06-23T00:00:01Z"},
		{DatabaseID: 3, CreatedAt: "2026-06-23T00:00:03Z"},
		{DatabaseID: 2, CreatedAt: "2026-06-23T00:00:03Z"},
	}

	page, err := slicePage(all, fleetreconcile.PageCursor{}, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []int64{3, 2, 1}
	for i, id := range want {
		if page[i].DatabaseID != id {
			t.Fatalf("position %d: want id %d, got %d (page %+v)", i, id, page[i].DatabaseID, page)
		}
	}
}

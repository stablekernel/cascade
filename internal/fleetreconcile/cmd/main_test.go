package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

// writeFile writes content to a file under t.TempDir() and returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// runsJSON is a two-run window: an unregistered success (benign) and a
// completed failure whose accounting depends on the ledger under test.
const runsJSON = `[
  {"databaseId": 11, "workflowName": "e2e", "event": "push", "conclusion": "success", "status": "completed", "headBranch": "main", "createdAt": "2026-07-15T01:00:00Z"},
  {"databaseId": 12, "workflowName": "e2e", "event": "push", "conclusion": "failure", "status": "completed", "headBranch": "main", "createdAt": "2026-07-15T01:01:00Z"}
]`

func TestRun_FlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "both runs and window-start",
			args:    []string{"--runs", "runs.json", "--window-start", "2026-07-15T00:00:00Z"},
			wantErr: "exactly one of --runs or --window-start is required",
		},
		{
			name:    "neither runs nor window-start",
			args:    nil,
			wantErr: "exactly one of --runs or --window-start is required",
		},
		{
			name:    "unknown flag",
			args:    []string{"--bogus"},
			wantErr: "flag provided but not defined: -bogus",
		},
		{
			name:    "window-start without repo",
			args:    []string{"--window-start", "2026-07-15T00:00:00Z"},
			wantErr: "--repo is required with --window-start",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			code, err := run(tt.args, &out)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if out.Len() != 0 {
				t.Errorf("report written on a validation error: %q", out.String())
			}
		})
	}
}

func TestRun_InputErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	malformedRuns := writeFile(t, "runs.json", "not json")
	goodRuns := writeFile(t, "runs.json", runsJSON)
	badLedger := writeFile(t, "ledger.jsonl",
		"{\"run_id\": 12, \"expected\": \"failure\", \"reason\": \"gated\"}\n\nnot json\n")

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing runs file",
			args:    []string{"--runs", missing},
			wantErr: "loading runs:",
		},
		{
			name:    "malformed runs JSON",
			args:    []string{"--runs", malformedRuns},
			wantErr: "parsing run-list JSON",
		},
		{
			name:    "malformed ledger line reports its line number",
			args:    []string{"--runs", goodRuns, "--ledger", badLedger},
			wantErr: "reading ledger: ledger line 3:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			code, err := run(tt.args, &out)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestRun_GatePass(t *testing.T) {
	runs := writeFile(t, "runs.json", runsJSON)
	ledger := writeFile(t, "ledger.jsonl",
		"{\"run_id\": 12, \"expected\": \"failure\", \"reason\": \"negative scenario\"}\n")

	var out bytes.Buffer
	code, err := run([]string{"--runs", runs, "--ledger", ledger}, &out)
	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	report := out.String()
	if !strings.Contains(report, "RESULT: PASS") {
		t.Errorf("report missing PASS result:\n%s", report)
	}
	if !strings.Contains(report, "FAILING - coverage gaps (0):") {
		t.Errorf("report should list zero coverage gaps:\n%s", report)
	}
}

func TestRun_GateFail(t *testing.T) {
	// Run 12 is a completed failure and nothing registers it: the exact
	// fire-and-forget gap the gate exists to catch.
	runs := writeFile(t, "runs.json", runsJSON)

	var out bytes.Buffer
	code, err := run([]string{"--runs", runs}, &out)
	if err != nil {
		t.Fatalf("run() error = %v, want nil (gate failure is an exit code, not an error)", err)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	report := out.String()
	if !strings.Contains(report, "RESULT: FAIL") {
		t.Errorf("report missing FAIL result:\n%s", report)
	}
	if !strings.Contains(report, "run 12") {
		t.Errorf("report should name the unaccounted run 12:\n%s", report)
	}
}

func TestRun_EmptyAndMissingLedger(t *testing.T) {
	// A success-only window passes with no ledger at all: an omitted --ledger
	// flag and a --ledger path that does not exist both mean "no registered
	// runs".
	successOnly := writeFile(t, "runs.json",
		`[{"databaseId": 11, "workflowName": "e2e", "event": "push", "conclusion": "success", "status": "completed", "headBranch": "main", "createdAt": "2026-07-15T01:00:00Z"}]`)
	missingLedger := filepath.Join(t.TempDir(), "no-ledger.jsonl")

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "omitted ledger flag", args: []string{"--runs", successOnly}},
		{name: "missing ledger file", args: []string{"--runs", successOnly, "--ledger", missingLedger}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			code, err := run(tt.args, &out)
			if err != nil {
				t.Fatalf("run() error = %v, want nil", err)
			}
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if !strings.Contains(out.String(), "RESULT: PASS") {
				t.Errorf("report missing PASS result:\n%s", out.String())
			}
		})
	}
}

func TestParseLedger(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantEntries int
		wantErr     string
	}{
		{
			name:        "valid entries with blank lines",
			input:       "{\"run_id\": 1, \"expected\": \"success\", \"reason\": \"a\"}\n\n{\"run_id\": 2, \"expected\": \"failure\", \"reason\": \"b\"}\n",
			wantEntries: 2,
		},
		{
			name:  "empty input",
			input: "",
		},
		{
			name:  "whitespace-only input",
			input: "\n   \n\t\n",
		},
		{
			name:    "malformed line numbered physically",
			input:   "{\"run_id\": 1, \"expected\": \"success\", \"reason\": \"a\"}\n\n{broken\n",
			wantErr: "ledger line 3:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := parseLedger(strings.NewReader(tt.input))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLedger() error = %v", err)
			}
			if len(entries) != tt.wantEntries {
				t.Fatalf("got %d entries, want %d", len(entries), tt.wantEntries)
			}
		})
	}

	t.Run("fields decode", func(t *testing.T) {
		entries, err := parseLedger(strings.NewReader(
			"{\"run_id\": 42, \"expected\": \"failure\", \"reason\": \"hotfix negative\"}\n"))
		if err != nil {
			t.Fatalf("parseLedger() error = %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(entries))
		}
		e := entries[0]
		if e.RunID != 42 || e.Expected != "failure" || e.Reason != "hotfix negative" {
			t.Errorf("entry = %+v, want RunID=42 Expected=failure Reason=%q", e, "hotfix negative")
		}
	})
}

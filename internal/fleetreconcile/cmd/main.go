// Command fleet-reconcile is the runnable wrapper around the fleetreconcile
// core. The fleet-reconcile reusable workflow invokes it with `go run` after a
// scenario suite finishes: it reads the run ledger (JSONL, one LedgerEntry per
// line) and the `gh run list` JSON the workflow captured for the scenario
// window, classifies every run, prints the report, and exits non-zero if any
// run is unaccounted for.
//
// This is fleet maintainer tooling, not part of cascade's generated output.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/stablekernel/cascade/internal/fleetreconcile"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "fleet-reconcile: %v\n", err)
		os.Exit(2) // 2 = tool error (bad input); 1 = gate failed; 0 = gate passed
	}
}

func run(args []string, out *os.File) error {
	fs := flag.NewFlagSet("fleet-reconcile", flag.ContinueOnError)
	ledgerPath := fs.String("ledger", "", "path to the run-ledger JSONL file (empty = no registered runs)")
	runsPath := fs.String("runs", "", "path to a pre-fetched gh-run-list JSON array (mutually exclusive with --window-start)")
	windowStart := fs.String("window-start", "", "ISO-8601 scenario window-start; enumerate runs via gh from here (mutually exclusive with --runs)")
	repo := fs.String("repo", "", "owner/name repo to enumerate (required with --window-start)")
	pageSize := fs.Int("page-size", 200, "gh run list page size for enumeration")
	maxPages := fs.Int("max-pages", 50, "safety cap on enumeration pages; reaching it on a full page fails closed")
	selfRunID := fs.Int64("self-run-id", 0, "this reconcile run's own id, excluded from reconciliation")
	allow := fs.String("allow-workflows", "", "comma-separated workflow names to reconcile; empty = all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*runsPath == "") == (*windowStart == "") {
		return fmt.Errorf("exactly one of --runs or --window-start is required")
	}

	runs, err := loadRuns(*runsPath, *windowStart, *repo, *pageSize, *maxPages)
	if err != nil {
		return fmt.Errorf("loading runs: %w", err)
	}
	ledger, err := readLedger(*ledgerPath)
	if err != nil {
		return fmt.Errorf("reading ledger: %w", err)
	}

	opts := fleetreconcile.Options{SelfRunID: *selfRunID}
	if names := splitNonEmpty(*allow); len(names) > 0 {
		opts.AllowWorkflows = make(map[string]struct{}, len(names))
		for _, n := range names {
			opts.AllowWorkflows[n] = struct{}{}
		}
	}

	rep := fleetreconcile.Reconcile(runs, ledger, opts)
	if _, err := fmt.Fprint(out, fleetreconcile.FormatReport(rep)); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}
	if !rep.Passed() {
		os.Exit(1)
	}
	return nil
}

// loadRuns returns the runs to reconcile: either a pre-fetched JSON array
// (runsPath, used by tests and callers that captured the list themselves) or,
// when windowStart is set, the full set enumerated from gh by paging strictly
// backward. Paging in Go (behind the unit-tested EnumerateRuns) means the gate
// fails closed on a truncated window instead of silently dropping runs.
func loadRuns(runsPath, windowStart, repo string, pageSize, maxPages int) ([]fleetreconcile.Run, error) {
	if runsPath != "" {
		return readRuns(runsPath)
	}
	if repo == "" {
		return nil, fmt.Errorf("--repo is required with --window-start")
	}
	return fleetreconcile.EnumerateRuns(windowStart, pageSize, maxPages, ghFetcher(repo, windowStart, pageSize))
}

func readRuns(path string) ([]fleetreconcile.Run, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var runs []fleetreconcile.Run
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, fmt.Errorf("parsing run-list JSON: %w", err)
	}
	return runs, nil
}

// ghFetcher returns a PageFetcher backed by `gh run list`. gh filters
// server-side to the scenario window (--created ">=windowStart") and returns
// runs newest-first; the fetcher slices off everything at or newer than the
// cursor so each call yields a strictly-older page. Slicing locally (rather
// than relying on a server-side run-id bound gh does not offer) keeps the
// strict-backward cursor - including its id half - working through a
// tied-timestamp boundary cluster.
func ghFetcher(repo, windowStart string, pageSize int) fleetreconcile.PageFetcher {
	return func(cursor fleetreconcile.PageCursor) ([]fleetreconcile.Run, error) {
		// Narrow the server-side date range to the window's older end at the
		// cursor's timestamp so each gh call returns a genuinely smaller slice
		// and the --limit page boundary advances. The range is inclusive on
		// both ends; the cursor's id half (applied below) drops the runs at the
		// boundary timestamp we have already consumed, so a tied-timestamp
		// boundary cluster still pages through cleanly.
		created := ">=" + windowStart
		if !cursor.IsZero() {
			created = windowStart + ".." + cursor.CreatedAt
		}
		// #nosec G204 - repo, windowStart, and the cursor timestamp come from
		// the trusted reusable workflow inputs and gh itself, not arbitrary
		// user data; every gh arg is a fixed flag.
		cmd := exec.Command("gh", "run", "list",
			"--repo", repo,
			"--created", created,
			"--limit", fmt.Sprintf("%d", pageSize),
			"--json", "databaseId,workflowName,event,conclusion,status,headBranch,createdAt")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("gh run list: %w", err)
		}
		var all []fleetreconcile.Run
		if err := json.Unmarshal(out, &all); err != nil {
			return nil, fmt.Errorf("parsing gh run list JSON: %w", err)
		}
		// Newest-first by (createdAt, id) so the cursor slice is well defined.
		sort.SliceStable(all, func(i, j int) bool {
			if all[i].CreatedAt != all[j].CreatedAt {
				return all[i].CreatedAt > all[j].CreatedAt
			}
			return all[i].DatabaseID > all[j].DatabaseID
		})
		page := make([]fleetreconcile.Run, 0, pageSize)
		for _, r := range all {
			if !cursor.IsZero() && !runOlderThanCursor(r, cursor) {
				continue
			}
			page = append(page, r)
			if len(page) == pageSize {
				break
			}
		}
		return page, nil
	}
}

// runOlderThanCursor reports whether r sorts strictly older than the cursor in
// newest-first (createdAt, id) order. It mirrors the enumerator's ordering so
// the gh fetcher pages exactly as EnumerateRuns expects.
func runOlderThanCursor(r fleetreconcile.Run, c fleetreconcile.PageCursor) bool {
	if r.CreatedAt != c.CreatedAt {
		return r.CreatedAt < c.CreatedAt
	}
	return r.DatabaseID < c.DatabaseID
}

// readLedger parses a JSONL ledger: one JSON LedgerEntry per non-blank line.
// A missing or empty path yields no entries (a suite that gated nothing).
func readLedger(path string) ([]fleetreconcile.LedgerEntry, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var entries []fleetreconcile.LedgerEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var e fleetreconcile.LedgerEntry
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", line, err)
		}
		entries = append(entries, e)
	}
	return entries, sc.Err()
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

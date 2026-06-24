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
	runsPath := fs.String("runs", "", "path to the gh-run-list JSON array for the scenario window (required)")
	selfRunID := fs.Int64("self-run-id", 0, "this reconcile run's own id, excluded from reconciliation")
	allow := fs.String("allow-workflows", "", "comma-separated workflow names to reconcile; empty = all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runsPath == "" {
		return fmt.Errorf("--runs is required")
	}

	runs, err := readRuns(*runsPath)
	if err != nil {
		return fmt.Errorf("reading runs: %w", err)
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

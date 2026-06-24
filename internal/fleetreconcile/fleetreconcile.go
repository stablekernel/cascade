// Package fleetreconcile holds the run-ledger reconcile core that backs the
// fleet-reconcile reusable workflow. It is fleet maintainer tooling, not part
// of cascade's generated output: a scenario suite registers every run it gates
// (its id and the conclusion it expects) into a ledger, and after the suite
// finishes this core enumerates every run the repo produced in the scenario
// window and fails if any run is unaccounted for.
//
// Keeping the decision logic here (rather than inline in YAML) makes the gate
// unit-testable against synthetic run lists with no live GitHub, so the core
// guarantee - an unregistered failing run reds the gate - is proven by a table
// test rather than asserted by hand.
package fleetreconcile

import (
	"fmt"
	"sort"
	"strings"
)

// LedgerEntry is one line a suite appended via the register-run action: the
// run it gated, the conclusion it expects that run to reach, and a short tag
// describing which scenario step registered it.
type LedgerEntry struct {
	RunID    int64  `json:"run_id"`
	Expected string `json:"expected"` // "success" or "failure"
	Reason   string `json:"reason"`
}

// Run is the subset of `gh run list --json ...` fields the reconcile needs.
// Field names mirror the gh JSON keys so the workflow can pass the raw list
// straight through.
type Run struct {
	DatabaseID   int64  `json:"databaseId"`
	WorkflowName string `json:"workflowName"`
	Event        string `json:"event"`
	Conclusion   string `json:"conclusion"` // success|failure|cancelled|skipped|"" (in-flight)
	Status       string `json:"status"`     // completed|in_progress|queued
	HeadBranch   string `json:"headBranch"`
}

// Verdict classifies one run against the ledger. Outcome buckets are mutually
// exclusive so the report can never double-count a run.
type Verdict struct {
	Run     Run
	Bucket  Bucket
	Detail  string // human-readable why, for the report
	Expects string // for accounted runs, the registered expectation
}

// Bucket is the reconcile classification of a single run.
type Bucket int

const (
	// Accounted: the run is in the ledger and its actual conclusion matched
	// the registered expectation (success-matched or failure-matched).
	Accounted Bucket = iota
	// BenignUnregistered: not in the ledger but allowed - a success or a
	// skipped run needs no explicit registration.
	BenignUnregistered
	// SupersededCancelled: a cancelled/skipped run provably superseded by a
	// later run of the same workflow + concurrency lane. Never a failure.
	SupersededCancelled
	// InFlight: the run has not concluded yet (e.g. the suite's own run, or a
	// late sibling). Not counted as a gap; reported for transparency.
	InFlight
	// FailGap: an unregistered non-success (the fire-and-forget the gate
	// exists to catch), or a registered run whose conclusion did not match
	// its expectation. These fail the gate.
	FailGap
)

// Report is the full reconcile outcome over a window of runs.
type Report struct {
	Verdicts []Verdict
	// Failing is the subset of Verdicts in the FailGap bucket, in input order.
	Failing []Verdict
}

// Passed reports whether the gate should exit zero (no failing runs).
func (r Report) Passed() bool { return len(r.Failing) == 0 }

// Options tune the classification without weakening it.
type Options struct {
	// SelfRunID is this reconcile run's own id; it is always excluded so the
	// gate never reconciles against itself. Zero means "no self to exclude".
	SelfRunID int64
	// AllowWorkflows, when non-empty, scopes reconcile to runs whose
	// WorkflowName is in the set. A run of any other workflow is ignored
	// (an unrelated sibling workflow's runs are out of scope). Empty means
	// "reconcile every workflow's runs in the window".
	AllowWorkflows map[string]struct{}
}

// Reconcile classifies every run against the ledger and returns the report.
// It never lets a `failure` conclusion be skipped as benign or superseded:
// only `cancelled`/`skipped` runs are eligible for the superseded path, and
// only when a strictly-later registered (accounted) run shares the same
// (workflow, concurrency lane) so the cancellation is provably a supersede,
// not a masked failure.
func Reconcile(runs []Run, ledger []LedgerEntry, opts Options) Report {
	byID := make(map[int64]LedgerEntry, len(ledger))
	for _, e := range ledger {
		byID[e.RunID] = e
	}

	rep := Report{}
	for _, r := range runs {
		if opts.SelfRunID != 0 && r.DatabaseID == opts.SelfRunID {
			continue
		}
		if len(opts.AllowWorkflows) > 0 {
			if _, ok := opts.AllowWorkflows[r.WorkflowName]; !ok {
				continue
			}
		}

		v := classify(r, byID, runs)
		rep.Verdicts = append(rep.Verdicts, v)
		if v.Bucket == FailGap {
			rep.Failing = append(rep.Failing, v)
		}
	}
	return rep
}

func classify(r Run, ledger map[int64]LedgerEntry, all []Run) Verdict {
	// Registered runs: the conclusion MUST match the expectation. A
	// registered expected:failure that actually failed is accounted; a
	// registered expected:success that failed is a hard gap.
	if e, ok := ledger[r.DatabaseID]; ok {
		if r.Status != "completed" || r.Conclusion == "" {
			return Verdict{Run: r, Bucket: InFlight, Expects: e.Expected,
				Detail: fmt.Sprintf("registered (%s) but still %s", e.Reason, statusLabel(r))}
		}
		if r.Conclusion == e.Expected {
			return Verdict{Run: r, Bucket: Accounted, Expects: e.Expected,
				Detail: fmt.Sprintf("registered %s, got %s (%s)", e.Expected, r.Conclusion, e.Reason)}
		}
		return Verdict{Run: r, Bucket: FailGap, Expects: e.Expected,
			Detail: fmt.Sprintf("registered run expected %s but concluded %s (%s)", e.Expected, r.Conclusion, e.Reason)}
	}

	// Unregistered runs.
	if r.Status != "completed" || r.Conclusion == "" {
		return Verdict{Run: r, Bucket: InFlight,
			Detail: fmt.Sprintf("unregistered run still %s", statusLabel(r))}
	}

	switch r.Conclusion {
	case "success", "skipped":
		return Verdict{Run: r, Bucket: BenignUnregistered,
			Detail: fmt.Sprintf("unregistered %s (benign)", r.Conclusion)}
	case "cancelled":
		if supersededBy := findSupersedingRun(r, ledger, all); supersededBy != 0 {
			return Verdict{Run: r, Bucket: SupersededCancelled,
				Detail: fmt.Sprintf("cancelled, superseded by registered run %d (same %s lane)", supersededBy, r.WorkflowName)}
		}
		return Verdict{Run: r, Bucket: FailGap,
			Detail: "cancelled but no superseding registered run in the same lane (could mask a real failure)"}
	default: // failure, timed_out, action_required, neutral, startup_failure, ...
		return Verdict{Run: r, Bucket: FailGap,
			Detail: fmt.Sprintf("unregistered non-success run (%s) created in the scenario window but never registered", r.Conclusion)}
	}
}

// findSupersedingRun returns the id of a registered (in-ledger) run that
// provably supersedes the cancelled run r: same workflow and the same
// concurrency lane (head branch), with a strictly greater run id (run ids are
// monotonic, so a larger id is a later run). It only consults registered runs
// so a cancelled run cannot be excused by another stray cancelled run - the
// superseder must itself be an asserted run. Returns 0 when none qualifies.
func findSupersedingRun(r Run, ledger map[int64]LedgerEntry, all []Run) int64 {
	byID := make(map[int64]Run, len(all))
	for _, x := range all {
		byID[x.DatabaseID] = x
	}
	var best int64
	for id := range ledger {
		later, ok := byID[id]
		if !ok {
			continue
		}
		if later.WorkflowName != r.WorkflowName {
			continue
		}
		if !sameLane(later, r) {
			continue
		}
		if later.DatabaseID > r.DatabaseID && later.DatabaseID > best {
			best = later.DatabaseID
		}
	}
	return best
}

// sameLane reports whether two runs share a concurrency lane. GitHub keys a
// generated workflow's concurrency on the head ref (branch), so a later run on
// the same branch+workflow is the run that cancelled the earlier queued one.
func sameLane(a, b Run) bool { return a.HeadBranch == b.HeadBranch }

func statusLabel(r Run) string {
	if r.Status != "" {
		return r.Status
	}
	return "in-flight"
}

// FormatReport renders a deterministic, human-readable reconcile report. The
// failing set is printed last and most loudly so a red gate is unambiguous.
func FormatReport(rep Report) string {
	var b strings.Builder

	groups := map[Bucket][]Verdict{}
	for _, v := range rep.Verdicts {
		groups[v.Bucket] = append(groups[v.Bucket], v)
	}

	section := func(title string, bucket Bucket) {
		vs := groups[bucket]
		fmt.Fprintf(&b, "%s (%d):\n", title, len(vs))
		sort.Slice(vs, func(i, j int) bool { return vs[i].Run.DatabaseID < vs[j].Run.DatabaseID })
		for _, v := range vs {
			fmt.Fprintf(&b, "  - run %d [%s] %s\n", v.Run.DatabaseID, v.Run.WorkflowName, v.Detail)
		}
	}

	section("Accounted (registered + matched)", Accounted)
	section("Benign unregistered (success/skipped)", BenignUnregistered)
	section("Superseded-cancelled (concurrency)", SupersededCancelled)
	section("In-flight (not yet concluded)", InFlight)

	fmt.Fprintf(&b, "FAILING - coverage gaps (%d):\n", len(rep.Failing))
	for _, v := range rep.Failing {
		fmt.Fprintf(&b, "  - run %d [%s] %s\n", v.Run.DatabaseID, v.Run.WorkflowName, v.Detail)
	}

	if rep.Passed() {
		b.WriteString("RESULT: PASS - every run in the scenario window is accounted for.\n")
	} else {
		fmt.Fprintf(&b, "RESULT: FAIL - %d run(s) unaccounted for. See FAILING above.\n", len(rep.Failing))
	}
	return b.String()
}

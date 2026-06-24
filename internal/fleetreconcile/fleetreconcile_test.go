package fleetreconcile

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// completed is a helper for a finished run.
func completed(id int64, wf, conclusion, branch string) Run {
	return Run{DatabaseID: id, WorkflowName: wf, Event: "push", Status: "completed", Conclusion: conclusion, HeadBranch: branch}
}

// completedAt is completed with an explicit createdAt for enumerator tests.
func completedAt(id int64, wf, conclusion, branch, createdAt string) Run {
	r := completed(id, wf, conclusion, branch)
	r.CreatedAt = createdAt
	return r
}

// descendingTS yields strictly descending ISO-8601 timestamps as i grows, so a
// set of runs built from increasing i has distinct, always-advancing createdAt
// values inside a single day.
func descendingTS(i int) string {
	h := 23 - (i / 60)
	m := 59 - (i % 60)
	return fmt.Sprintf("2026-06-23T%02d:%02d:00Z", h, m)
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		runs        []Run
		ledger      []LedgerEntry
		opts        Options
		wantPass    bool
		wantFailIDs []int64
	}{
		{
			// (a) every run registered and matching -> PASS.
			name: "all registered and matched passes",
			runs: []Run{
				completed(1, "Orchestrate", "success", "main"),
				completed(2, "Promote", "success", "main"),
			},
			ledger: []LedgerEntry{
				{RunID: 1, Expected: "success", Reason: "push-orchestrate"},
				{RunID: 2, Expected: "success", Reason: "promote-hop"},
			},
			wantPass: true,
		},
		{
			// (b) THE CORE GUARANTEE: an unregistered failure in the window
			// fails the gate. This is the fire-and-forget the gate exists to
			// catch (e.g. the 3env hotfix Finalize run).
			name: "unregistered failure fails the gate",
			runs: []Run{
				completed(1, "Orchestrate", "success", "main"),
				completed(99, "Cascade Hotfix", "failure", "main"), // never registered
			},
			ledger: []LedgerEntry{
				{RunID: 1, Expected: "success", Reason: "push-orchestrate"},
			},
			wantPass:    false,
			wantFailIDs: []int64{99},
		},
		{
			// (c1) a registered expected:failure that actually failed -> PASS
			// (negative guard, e.g. divergence-promote guard).
			name: "registered expected-failure that failed passes",
			runs: []Run{
				completed(7, "Promote", "failure", "main"),
			},
			ledger: []LedgerEntry{
				{RunID: 7, Expected: "failure", Reason: "divergence-guard"},
			},
			wantPass: true,
		},
		{
			// (c2) a registered expected:success that actually failed -> FAIL.
			name: "registered expected-success that failed fails",
			runs: []Run{
				completed(8, "Release", "failure", "main"),
			},
			ledger: []LedgerEntry{
				{RunID: 8, Expected: "success", Reason: "release-publish"},
			},
			wantPass:    false,
			wantFailIDs: []int64{8},
		},
		{
			// A registered expected:failure that unexpectedly SUCCEEDED is also
			// a gap: the negative guard did not fire (never let a guard pass by
			// going green when it should have refused).
			name: "registered expected-failure that succeeded fails",
			runs: []Run{
				completed(9, "Promote", "success", "main"),
			},
			ledger: []LedgerEntry{
				{RunID: 9, Expected: "failure", Reason: "downgrade-guard"},
			},
			wantPass:    false,
			wantFailIDs: []int64{9},
		},
		{
			// Unregistered success and skipped runs are benign.
			name: "unregistered success and skipped are benign",
			runs: []Run{
				completed(10, "Orchestrate", "success", "main"),
				completed(11, "Cascade PR Preview", "skipped", "feature"),
			},
			wantPass: true,
		},
		{
			// A cancelled run provably superseded by a later REGISTERED run of
			// the same workflow + lane is skipped (GHA concurrency cancels the
			// older queued run). Never a false positive.
			name: "cancelled superseded by later registered run is benign",
			runs: []Run{
				completed(20, "Orchestrate", "cancelled", "main"),
				completed(21, "Orchestrate", "success", "main"),
			},
			ledger: []LedgerEntry{
				{RunID: 21, Expected: "success", Reason: "push-orchestrate-latest"},
			},
			wantPass: true,
		},
		{
			// A cancelled run with NO superseding registered run in its lane is
			// a gap: it could be masking a real cancellation we never asserted.
			name: "cancelled with no superseder fails",
			runs: []Run{
				completed(30, "Orchestrate", "cancelled", "main"),
			},
			wantPass:    false,
			wantFailIDs: []int64{30},
		},
		{
			// A failure is NEVER excused as superseded, even if a later
			// registered run shares the lane. Failures always red the gate
			// unless explicitly registered as expected:failure.
			name: "failure is never treated as superseded",
			runs: []Run{
				completed(40, "Orchestrate", "failure", "main"),
				completed(41, "Orchestrate", "success", "main"),
			},
			ledger: []LedgerEntry{
				{RunID: 41, Expected: "success", Reason: "push-orchestrate-latest"},
			},
			wantPass:    false,
			wantFailIDs: []int64{40},
		},
		{
			// The reconcile run excludes itself and ignores out-of-scope
			// workflows (an unrelated sibling's runs do not red the gate).
			name: "self-run excluded and out-of-scope workflow ignored",
			runs: []Run{
				completed(50, "Fleet Reconcile", "failure", "main"),     // self (in-flight in reality; failure here proves exclusion)
				completed(51, "Some Unrelated CI", "failure", "main"),   // not in allowlist
				completed(52, "Orchestrate", "success", "main"),
			},
			ledger: []LedgerEntry{{RunID: 52, Expected: "success", Reason: "orchestrate"}},
			opts: Options{
				SelfRunID:      50,
				AllowWorkflows: map[string]struct{}{"Orchestrate": {}},
			},
			wantPass: true,
		},
		{
			// An unregistered run still in-flight is not a gap (reported only).
			name: "in-flight unregistered run is not a gap",
			runs: []Run{
				{DatabaseID: 60, WorkflowName: "Orchestrate", Status: "in_progress", Conclusion: "", HeadBranch: "main"},
			},
			wantPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rep := Reconcile(tt.runs, tt.ledger, tt.opts)
			require.Equal(t, tt.wantPass, rep.Passed(), "pass/fail mismatch\n%s", FormatReport(rep))

			var gotFail []int64
			for _, v := range rep.Failing {
				gotFail = append(gotFail, v.Run.DatabaseID)
			}
			require.ElementsMatch(t, tt.wantFailIDs, gotFail, "failing set mismatch\n%s", FormatReport(rep))
		})
	}
}

// TestFormatReport_FailIsLoud proves the rendered report names the failing run
// and states a FAIL result so a red gate is never ambiguous in the log.
func TestFormatReport_FailIsLoud(t *testing.T) {
	t.Parallel()
	rep := Reconcile(
		[]Run{completed(99, "Cascade Hotfix", "failure", "main")},
		nil,
		Options{},
	)
	out := FormatReport(rep)
	require.False(t, rep.Passed())
	require.Contains(t, out, "run 99")
	require.Contains(t, out, "RESULT: FAIL")
	require.True(t, strings.Contains(out, "never registered"))
}

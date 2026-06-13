package harness

import (
	"strings"
	"testing"
)

// goroutineDump is a representative Go runtime panic + goroutine dump as it
// appears interleaved in act's stdout/stderr when the cascade CLI crashes
// mid-run. The dump corrupts act's --json stream, so no "Job failed" event is
// parseable. It carries a real cascade stack frame
// (internal/orchestrate.(*Planner).Plan), so it is a definitive cascade crash
// and must not be retried away as a transient flake.
const goroutineDump = `time="2026-06-13T00:00:00Z" level=info msg="⭐ Run Main cascade orchestrate"
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x10a3f20]

goroutine 1 [running]:
github.com/stablekernel/cascade/internal/orchestrate.(*Planner).Plan(0x0)
	/cascade/internal/orchestrate/planner.go:142 +0x1a0
main.main()
	/cascade/main.go:33 +0x9c
`

// actOnlyPanicDump is a panic whose goroutine dump contains ONLY nektos/act and
// stdlib frames, with no cascade frame. act can itself panic on an
// infrastructure path (a docker transport hiccup, a teardown race); such a
// crash is not a cascade defect and a retry should absorb it, so it must NOT be
// classified as a (non-transient) cascade crash.
const actOnlyPanicDump = `time="2026-06-13T00:00:00Z" level=info msg="⭐ Run Set up job"
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x9f1d40]

goroutine 84 [running]:
github.com/nektos/act/pkg/runner.(*runnerImpl).NewPlanExecutor.func1(0x0)
	/go/pkg/mod/github.com/nektos/act/pkg/runner/runner.go:201 +0x120
net/http.(*conn).serve(0xc0001a4000, {0x1, 0x2})
	/usr/local/go/src/net/http/server.go:2092 +0x4f4
internal/poll.(*FD).Read(0xc0000b8000, {0xc0001, 0x10, 0x10})
	/usr/local/go/src/internal/poll/fd_unix.go:164 +0x29a
`

// bareActGoroutineDump is the actual shape act prints on a benign teardown:
// monitorJobCancellation under cancel-in-progress dumps every goroutine with a
// "goroutine N [wselect]:" header and NO panic / fatal error / signal trigger.
// On CI run 27480827218 this flaked Hotfix_Refusal_Guards step 2 even though the
// scenario passes 4/4 locally with zero retries. It is pure act-infra noise and
// must stay transient.
const bareActGoroutineDump = `time="2026-06-13T00:00:00Z" level=info msg="⭐ Run Set up job"
goroutine 470 [wselect]:
github.com/nektos/act/pkg/runner.(*runnerImpl).monitorJobCancellation.func1()
	/go/pkg/mod/github.com/nektos/act/pkg/runner/runner.go:312 +0x88
created by github.com/nektos/act/pkg/runner.(*runnerImpl).monitorJobCancellation
	/go/pkg/mod/github.com/nektos/act/pkg/runner/runner.go:305 +0x14c

goroutine 12 [chan receive]:
github.com/nektos/act/pkg/container.(*containerReference).Exec.func2()
	/go/pkg/mod/github.com/nektos/act/pkg/container/docker_run.go:401 +0x40
`

// signalWithCascadeFrame is a fatal signal whose dump carries a cascade frame
// but no leading "panic:" line. The signal report is itself a crash trigger, so
// paired with a cascade frame it is a definitive cascade crash.
const signalWithCascadeFrame = `time="2026-06-13T00:00:00Z" level=info msg="⭐ Run Main cascade promote"
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x10b4c10]

goroutine 1 [running]:
github.com/stablekernel/cascade/cmd/cascade.run({0x0, 0x0})
	/cascade/cmd/cascade/main.go:88 +0x210
`

// TestDetectCrashSignature verifies a crash is classified (non-transient) ONLY
// when a real runtime trigger (panic/fatal error/signal) is paired with a real
// cascade stack frame. An act/docker/stdlib-only dump, or a bare goroutine dump
// with no trigger, stays uncaught here so the scenario runner treats it as a
// transient flake and retries it.
func TestDetectCrashSignature(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		logs        string
		wantCrash   bool
		wantReasony string // substring expected in the reason when wantCrash
	}{
		{
			name:        "panic plus goroutine dump with cascade frame is a crash",
			logs:        goroutineDump,
			wantCrash:   true,
			wantReasony: "panic",
		},
		{
			name:        "fatal error at line start with cascade frame is a crash",
			logs:        "some log\nfatal error: concurrent map writes\n\ngoroutine 7 [running]:\ngithub.com/stablekernel/cascade/internal/state.(*Store).Write(0x0)\n\t/cascade/internal/state/store.go:51 +0x40\n",
			wantCrash:   true,
			wantReasony: "fatal error",
		},
		{
			name:        "SIGSEGV signal line with cascade frame is a crash",
			logs:        signalWithCascadeFrame,
			wantCrash:   true,
			wantReasony: "SIGSEGV",
		},
		{
			name:      "panic with only act and stdlib frames is not a cascade crash",
			logs:      actOnlyPanicDump,
			wantCrash: false,
		},
		{
			name:      "bare act goroutine dump with no trigger is not a crash",
			logs:      bareActGoroutineDump,
			wantCrash: false,
		},
		{
			name:      "fatal error with no cascade frame is not a cascade crash",
			logs:      "fatal error: concurrent map writes\n\ngoroutine 7 [running]:\nruntime.gopanic(0x1, 0x2)\n\t/usr/local/go/src/runtime/panic.go:884\n",
			wantCrash: false,
		},
		{
			name:      "cascade frame present but no trigger is not a crash",
			logs:      "goroutine 9 [running]:\ngithub.com/stablekernel/cascade/internal/orchestrate.(*Planner).Plan(0x0)\n\t/cascade/internal/orchestrate/planner.go:142\n",
			wantCrash: false,
		},
		{
			name:      "git-url prose mentioning the cascade module path is not a crash",
			logs:      `{"jobID":"deploy","msg":"cloning github.com/stablekernel/cascade/internal at v1.2.3","level":"info"}`,
			wantCrash: false,
		},
		{
			name:      "benign log mentioning the word panic in prose is not a crash",
			logs:      `{"jobID":"deploy","msg":"Do not panic: rollback is automatic","level":"info"}`,
			wantCrash: false,
		},
		{
			name:      "benign log mentioning goroutine count in prose is not a crash",
			logs:      `{"jobID":"deploy","msg":"started 8 goroutines for the worker pool","level":"info"}`,
			wantCrash: false,
		},
		{
			name:      "ordinary successful output has no crash signature",
			logs:      `{"jobID":"build","jobResult":"success","msg":"🏁 Job succeeded","level":"info"}`,
			wantCrash: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			crashed, reason := detectCrashSignature(tt.logs)
			if crashed != tt.wantCrash {
				t.Fatalf("detectCrashSignature crashed = %v, want %v (reason=%q)", crashed, tt.wantCrash, reason)
			}
			if tt.wantCrash {
				if reason == "" {
					t.Fatalf("expected a non-empty crash reason when crashed")
				}
				if tt.wantReasony != "" && !strings.Contains(strings.ToLower(reason), strings.ToLower(tt.wantReasony)) {
					t.Fatalf("crash reason %q does not mention %q", reason, tt.wantReasony)
				}
			}
		})
	}
}

// TestParseActOutput_SetsCrashFields verifies the parser surfaces a runtime
// crash on the result so the classifier downstream can act on it.
func TestParseActOutput_SetsCrashFields(t *testing.T) {
	t.Parallel()

	result, err := ParseActOutput(goroutineDump)
	if err != nil {
		t.Fatalf("ParseActOutput returned error: %v", err)
	}
	if !result.Crashed {
		t.Fatalf("expected Crashed=true for a panic+goroutine dump with a cascade frame")
	}
	if result.CrashReason == "" {
		t.Fatalf("expected a non-empty CrashReason")
	}
}

// TestParseActOutput_ActOnlyDumpIsNotCrash verifies an act/docker-only panic or
// a bare act goroutine dump does NOT set the crash fields, so the run falls
// through to the transient path and is retried as an infrastructure flake.
func TestParseActOutput_ActOnlyDumpIsNotCrash(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		logs string
	}{
		{name: "act-only panic", logs: actOnlyPanicDump},
		{name: "bare act goroutine dump", logs: bareActGoroutineDump},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseActOutput(tt.logs)
			if err != nil {
				t.Fatalf("ParseActOutput returned error: %v", err)
			}
			if result.Crashed {
				t.Fatalf("expected Crashed=false for an act-only dump (reason=%q)", result.CrashReason)
			}
		})
	}
}

// TestNormalizeWorkflowResult_CrashIsNotTransient is the core safety guarantee:
// a non-zero act exit carrying a cascade crash signature must be classified as a
// REAL (non-transient) failure so the scenario runner does not retry it as a
// flake and the stack trace surfaces.
func TestNormalizeWorkflowResult_CrashIsNotTransient(t *testing.T) {
	t.Parallel()

	result := &ExtendedWorkflowResult{
		Conclusion: "success",
		Jobs:       map[string]*JobResultExtended{},
		Logs:       goroutineDump,
		Crashed:    true,
		CrashReason: "panic: runtime error: invalid memory address or nil pointer " +
			"dereference",
	}
	normalizeWorkflowResult(result, ".github/workflows/orchestrate.yaml", 2)

	if result.Conclusion != "failure" {
		t.Fatalf("Conclusion = %q, want failure", result.Conclusion)
	}
	if result.ExecError {
		t.Fatalf("a crash must NOT be tagged as a transient ExecError")
	}
	if !strings.Contains(strings.ToLower(result.Error), "crash") {
		t.Fatalf("error %q should describe the crash", result.Error)
	}

	// And it must not be retried by the classifier.
	failErr := workflowFailureError("orchestrate", result)
	if IsTransientWorkflowError(failErr) {
		t.Fatalf("a crash failure must not be classified transient: %v", failErr)
	}
}

// TestNormalizeWorkflowResult_ActOnlyDumpIsTransient is the sibling guarantee:
// a non-zero act exit whose logs carry only an act/docker dump (no cascade
// frame) is NOT flagged Crashed by the parser, so it falls through to the
// transient ExecError path and the scenario runner retries it as an infra
// flake.
func TestNormalizeWorkflowResult_ActOnlyDumpIsTransient(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		logs string
	}{
		{name: "act-only panic", logs: actOnlyPanicDump},
		{name: "bare act goroutine dump", logs: bareActGoroutineDump},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Mirror the production path: the parser decides Crashed, then
			// normalizeWorkflowResult classifies a non-zero exit.
			result, err := ParseActOutput(tt.logs)
			if err != nil {
				t.Fatalf("ParseActOutput returned error: %v", err)
			}
			normalizeWorkflowResult(result, ".github/workflows/orchestrate.yaml", 2)

			if result.Conclusion != "failure" {
				t.Fatalf("Conclusion = %q, want failure", result.Conclusion)
			}
			if !result.ExecError {
				t.Fatalf("an act-only dump must be tagged transient (ExecError) so it is retried")
			}
			failErr := workflowFailureError("orchestrate", result)
			if !IsTransientWorkflowError(failErr) {
				t.Fatalf("an act-only dump failure must be classified transient: %v", failErr)
			}
		})
	}
}

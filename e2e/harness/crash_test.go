package harness

import (
	"strings"
	"testing"
)

// goroutineDump is a representative Go runtime panic + goroutine dump as it
// appears interleaved in act's stdout/stderr when the cascade CLI (or act
// itself) crashes mid-run. The dump corrupts act's --json stream, so no
// "Job failed" event is parseable and the run would otherwise be misclassified
// as a transient flake and retried away.
const goroutineDump = `time="2026-06-13T00:00:00Z" level=info msg="⭐ Run Main cascade orchestrate"
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x10a3f20]

goroutine 1 [running]:
github.com/stablekernel/cascade/internal/orchestrate.(*Planner).Plan(0x0)
	/cascade/internal/orchestrate/planner.go:142 +0x1a0
main.main()
	/cascade/main.go:33 +0x9c
`

// TestDetectCrashSignature_GoPanicAndGoroutineDump verifies a real Go-runtime
// crash signature in raw act output is detected so it can be treated as a
// definitive failure rather than a transient flake.
func TestDetectCrashSignature_GoPanicAndGoroutineDump(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		logs        string
		wantCrash   bool
		wantReasony string // substring expected in the reason when wantCrash
	}{
		{
			name:        "panic plus goroutine dump",
			logs:        goroutineDump,
			wantCrash:   true,
			wantReasony: "panic",
		},
		{
			name:        "fatal error at line start",
			logs:        "some log\nfatal error: concurrent map writes\n\ngoroutine 7 [running]:\n",
			wantCrash:   true,
			wantReasony: "fatal error",
		},
		{
			name:        "goroutine dump with runtime frame",
			logs:        "goroutine 42 [running]:\nruntime.gopanic(0x1, 0x2)\n\t/usr/local/go/src/runtime/panic.go:884\n",
			wantCrash:   true,
			wantReasony: "goroutine",
		},
		{
			name:        "SIGSEGV signal line",
			logs:        "unexpected\n[signal SIGSEGV: segmentation violation code=0x1 addr=0x0]\n",
			wantCrash:   true,
			wantReasony: "SIGSEGV",
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
		t.Fatalf("expected Crashed=true for a panic+goroutine dump")
	}
	if result.CrashReason == "" {
		t.Fatalf("expected a non-empty CrashReason")
	}
}

// TestNormalizeWorkflowResult_CrashIsNotTransient is the core safety guarantee:
// a non-zero act exit carrying a Go-runtime crash signature must be classified
// as a REAL (non-transient) failure so the scenario runner does not retry it as
// a flake and the stack trace surfaces.
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

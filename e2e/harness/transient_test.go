package harness

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestIsTransientWorkflowError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error is not transient",
			err:  nil,
			want: false,
		},
		{
			name: "plain error is not transient",
			err:  errors.New("expected promote to fail but it succeeded"),
			want: false,
		},
		{
			name: "sentinel directly is transient",
			err:  errTransientWorkflow,
			want: true,
		},
		{
			name: "wrapped sentinel is transient",
			err:  fmt.Errorf("promote workflow failed: workflow execution failed: %w", errTransientWorkflow),
			want: true,
		},
		{
			name: "doubly wrapped sentinel is transient",
			err: fmt.Errorf("step 3 (Promote) failed: %w",
				fmt.Errorf("promote workflow failed: %w", errTransientWorkflow)),
			want: true,
		},
		{
			name: "real job failure wrapping a non-sentinel is not transient",
			err:  fmt.Errorf("promote workflow failed: %s", "build job concluded failure"),
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsTransientWorkflowError(tt.err); got != tt.want {
				t.Fatalf("IsTransientWorkflowError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestWorkflowFailureError_TransientClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		result        *ExtendedWorkflowResult
		wantTransient bool
	}{
		{
			name:          "exec error result is transient",
			result:        &ExtendedWorkflowResult{Conclusion: "failure", Error: "workflow execution failed", ExecError: true},
			wantTransient: true,
		},
		{
			name:          "real job-level failure is not transient",
			result:        &ExtendedWorkflowResult{Conclusion: "failure", Error: "build job failed"},
			wantTransient: false,
		},
		{
			name:          "missing-workflow failure is not transient",
			result:        &ExtendedWorkflowResult{Conclusion: "failure", Error: "act produced no jobs for workflow \"promote.yaml\""},
			wantTransient: false,
		},
		{
			name:          "nil result is not transient",
			result:        nil,
			wantTransient: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := workflowFailureError("promote", tt.result)
			if got := IsTransientWorkflowError(err); got != tt.wantTransient {
				t.Fatalf("workflowFailureError transient = %v, want %v (err=%v)", got, tt.wantTransient, err)
			}
			if !strings.Contains(err.Error(), "promote workflow failed") {
				t.Fatalf("error message missing action prefix: %q", err.Error())
			}
		})
	}
}

// TestNormalizeWorkflowResult_ExecErrorTagging verifies the NARROWED transient
// classifier: a non-zero act exit is tagged transient (ExecError true,
// retryable) ONLY when the raw logs carry a named infra-saturation signature.
// Every other job-less, crash-free non-zero exit is now a DETERMINISTIC failure
// (ExecError false, not retried) - this is the load-bearing proof that the
// catch-all masking bucket is gone. Real job failures, crashes, missing
// workflows, and successful runs keep their prior classification.
func TestNormalizeWorkflowResult_ExecErrorTagging(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		jobs           map[string]*JobResultExtended
		logs           string
		crashed        bool
		crashReason    string
		workflowPath   string
		exitCode       int
		wantConclusion string
		wantExecError  bool
	}{
		{
			// LOAD-BEARING: a non-zero exit with no parsed jobs, no crash
			// signature, and NO infra signature is no longer swallowed as a
			// transient flake. It is now a deterministic real failure that fails
			// on attempt 1 and surfaces a bug instead of burning the retry budget.
			name:           "non-infra non-zero exit is NOT transient",
			jobs:           map[string]*JobResultExtended{},
			logs:           "act exited 1: some unexpected step error with no named infra signature",
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantExecError:  false,
		},
		{
			// New: a non-zero exit carrying a named docker address-pool
			// exhaustion signature (#125) is a genuine, retryable infra transient.
			name:           "address-pool exhaustion is transient",
			jobs:           map[string]*JobResultExtended{},
			logs:           "Error response from daemon: all predefined address pools have been fully subnetted",
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantExecError:  true,
		},
		{
			name:           "no space left on device is transient",
			jobs:           map[string]*JobResultExtended{},
			logs:           "write /var/lib/docker/tmp/x: no space left on device",
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantExecError:  true,
		},
		{
			name:           "cannot allocate memory is transient",
			jobs:           map[string]*JobResultExtended{},
			logs:           "fork/exec: cannot allocate memory",
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantExecError:  true,
		},
		{
			name:           "gitea connection reset is transient",
			jobs:           map[string]*JobResultExtended{},
			logs:           "fatal: unable to access gitea: Recv failure: Connection reset by peer",
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantExecError:  true,
		},
		{
			name:           "docker daemon unreachable is transient",
			jobs:           map[string]*JobResultExtended{},
			logs:           "Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantExecError:  true,
		},
		{
			// An infra signature must NOT override a real, parsed job failure:
			// the job-failure branch is checked first and stays deterministic.
			name: "real job failure beats an infra signature in logs",
			jobs: map[string]*JobResultExtended{
				"build": {Name: "build", Conclusion: "failure"},
			},
			logs:           "connection refused (noise) but a job really failed",
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantExecError:  false,
		},
		{
			// A non-zero exit with no parsed jobs but carrying a Go-runtime crash
			// signature is a REAL crash, not a flake. ExecError must stay false so
			// the scenario runner does not retry it away - even if the logs also
			// contain an infra phrase, the crash branch wins.
			name:           "non-zero exit with crash signature is not transient",
			jobs:           map[string]*JobResultExtended{},
			crashed:        true,
			crashReason:    "panic: runtime error: nil pointer dereference",
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       2,
			wantConclusion: "failure",
			wantExecError:  false,
		},
		{
			// Regression: act exits non-zero when a job genuinely concludes
			// "failure". That is a real, deterministic defect, not an act/docker
			// transport hiccup, so ExecError must stay false and the scenario
			// runner must NOT retry it.
			name: "non-zero exit with a failed job is a real failure not transient",
			jobs: map[string]*JobResultExtended{
				"build": {Name: "build", Conclusion: "failure"},
			},
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantExecError:  false,
		},
		{
			name:           "zero exit with no jobs is a real failure not transient",
			jobs:           map[string]*JobResultExtended{},
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       0,
			wantConclusion: "failure",
			wantExecError:  false,
		},
		{
			name:           "zero exit with jobs stays successful and not transient",
			jobs:           map[string]*JobResultExtended{"build": {Name: "build", Conclusion: "success"}},
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       0,
			wantConclusion: "success",
			wantExecError:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := &ExtendedWorkflowResult{
				Conclusion:  "success",
				Jobs:        tt.jobs,
				Logs:        tt.logs,
				Crashed:     tt.crashed,
				CrashReason: tt.crashReason,
			}
			normalizeWorkflowResult(result, tt.workflowPath, tt.exitCode)
			if result.Conclusion != tt.wantConclusion {
				t.Fatalf("Conclusion = %q, want %q", result.Conclusion, tt.wantConclusion)
			}
			if result.ExecError != tt.wantExecError {
				t.Fatalf("ExecError = %v, want %v", result.ExecError, tt.wantExecError)
			}
		})
	}
}

// TestDetectInfraSaturation verifies that each named host-saturation signature
// is matched (case-insensitively) and that unrelated output is not, since this
// detector is the sole gate that makes a job-less, crash-free non-zero act exit
// retryable.
func TestDetectInfraSaturation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		logs      string
		wantMatch bool
	}{
		{name: "empty never matches", logs: "", wantMatch: false},
		{name: "ordinary failure does not match", logs: "step 'build' failed with exit code 1", wantMatch: false},
		{
			name:      "address pool exhaustion matches",
			logs:      "all predefined address pools have been fully subnetted",
			wantMatch: true,
		},
		{
			name:      "case-insensitive address pool matches",
			logs:      "Error: All Predefined Address Pools Have Been Fully Subnetted",
			wantMatch: true,
		},
		{name: "overlapping pool matches", logs: "could not find an available, non-overlapping IPv4 address pool", wantMatch: true},
		{name: "no space matches", logs: "no space left on device", wantMatch: true},
		{name: "cannot allocate memory matches", logs: "cannot allocate memory", wantMatch: true},
		{name: "oomkilled matches", logs: "container state: OOMKilled", wantMatch: true},
		{name: "connection reset matches", logs: "Connection reset by peer", wantMatch: true},
		{name: "connection refused matches", logs: "dial tcp: connection refused", wantMatch: true},
		{name: "docker daemon unreachable matches", logs: "Cannot connect to the Docker daemon", wantMatch: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, reason := detectInfraSaturation(tt.logs)
			if got != tt.wantMatch {
				t.Fatalf("detectInfraSaturation(%q) = %v (reason %q), want %v", tt.logs, got, reason, tt.wantMatch)
			}
			if got && reason == "" {
				t.Fatalf("detectInfraSaturation matched but returned empty reason for %q", tt.logs)
			}
		})
	}
}

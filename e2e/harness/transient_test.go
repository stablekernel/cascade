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

// TestNormalizeWorkflowResult_ExecErrorTagging verifies that only an act/docker
// exec hiccup (non-zero exit) is tagged transient, while a real "no jobs
// parsed" failure (a missing or unloadable workflow) and a successful run are
// not.
func TestNormalizeWorkflowResult_ExecErrorTagging(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		jobs           map[string]*JobResultExtended
		workflowPath   string
		exitCode       int
		wantConclusion string
		wantExecError  bool
	}{
		{
			name:           "non-zero exit with no jobs tags transient exec error",
			jobs:           map[string]*JobResultExtended{},
			workflowPath:   ".github/workflows/promote.yaml",
			exitCode:       1,
			wantConclusion: "failure",
			wantExecError:  true,
		},
		{
			// Regression: act exits non-zero when a job genuinely concludes
			// "failure". That is a real, deterministic defect, not an
			// act/docker transport hiccup, so ExecError must stay false and the
			// scenario runner must NOT retry it.
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
			result := &ExtendedWorkflowResult{Conclusion: "success", Jobs: tt.jobs}
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

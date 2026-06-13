package harness

import (
	"errors"
	"fmt"
)

// errTransientWorkflow is the sentinel that marks a workflow failure as a
// transient act/docker execution hiccup rather than a real outcome. The
// scenario runner retries ONLY on errors that wrap this sentinel; every other
// failure (a real job-level "failure" conclusion, an expect_failure mismatch, a
// state/branch/tag assertion mismatch) is deterministic and must fail
// immediately.
var errTransientWorkflow = errors.New("transient workflow execution failure")

// IsTransientWorkflowError reports whether err is (or wraps) a transient
// act/docker execution failure that is safe to retry from a clean slate.
func IsTransientWorkflowError(err error) bool {
	return errors.Is(err, errTransientWorkflow)
}

// workflowError is the error a failing workflow step returns. Beyond the
// message it carries the failing run's raw act stdout/stderr so the scenario
// retry layer can persist it as evidence when the retry budget is exhausted -
// preserving the stack-trace origin of a crash that the CI log would otherwise
// expire. When transient is true it wraps errTransientWorkflow so the scenario
// runner may retry; otherwise it is a deterministic failure that fails on the
// first attempt.
type workflowError struct {
	msg       string
	logs      string
	transient bool
}

// Error returns the human-readable failure message.
func (e *workflowError) Error() string { return e.msg }

// Unwrap exposes errTransientWorkflow for transient failures so
// IsTransientWorkflowError (and errors.Is) classify them as retryable, and nil
// otherwise so a deterministic failure never matches the transient sentinel.
func (e *workflowError) Unwrap() error {
	if e.transient {
		return errTransientWorkflow
	}
	return nil
}

// actLogs returns the raw act stdout/stderr captured on the failing run, used by
// the retry layer to persist crash evidence. It is empty when no logs were
// available.
func (e *workflowError) actLogs() string { return e.logs }

// workflowFailureError builds the error returned when a workflow run concluded
// in failure on a non-expect_failure step. When the failure was an act/docker
// execution hiccup (result.ExecError), the error is classified transient so the
// scenario runner may retry it from a fresh repo and fresh containers. A real
// job-level failure or a runtime crash (ExecError false) yields a deterministic
// error that is never retried. The returned error always carries the run's raw
// act logs so the retry layer can persist evidence on exhaustion.
//
// This must only be called on a genuine failure path. An expect_failure step
// that legitimately concluded "failure" is the expected outcome and returns nil
// before reaching here, so a transient classification can never mask it.
func workflowFailureError(action string, result *ExtendedWorkflowResult) error {
	if result == nil {
		return &workflowError{msg: fmt.Sprintf("%s workflow failed", action)}
	}
	if result.ExecError {
		return &workflowError{
			msg:       fmt.Sprintf("%s workflow failed: %s: %s", action, result.Error, errTransientWorkflow),
			logs:      result.Logs,
			transient: true,
		}
	}
	return &workflowError{
		msg:  fmt.Sprintf("%s workflow failed: %s", action, result.Error),
		logs: result.Logs,
	}
}

// workflowExecError is an alias for workflowFailureError, named for the call
// sites that surface an act execution failure carrying raw logs. It exists so
// the intent (capture-and-classify an act run failure) reads clearly at the call
// site and in tests.
func workflowExecError(action string, result *ExtendedWorkflowResult) error {
	return workflowFailureError(action, result)
}

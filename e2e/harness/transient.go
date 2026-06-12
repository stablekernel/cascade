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

// workflowFailureError builds the error returned when a workflow run concluded
// in failure on a non-expect_failure step. When the failure was an act/docker
// execution hiccup (result.ExecError), the error wraps errTransientWorkflow so
// the scenario runner may retry it from a fresh repo and fresh containers. A
// real job-level failure conclusion (ExecError false) yields a plain error that
// is never retried.
//
// This must only be called on a genuine failure path. An expect_failure step
// that legitimately concluded "failure" is the expected outcome and returns nil
// before reaching here, so a transient classification can never mask it.
func workflowFailureError(action string, result *ExtendedWorkflowResult) error {
	if result == nil {
		return fmt.Errorf("%s workflow failed", action)
	}
	if result.ExecError {
		return fmt.Errorf("%s workflow failed: %s: %w", action, result.Error, errTransientWorkflow)
	}
	return fmt.Errorf("%s workflow failed: %s", action, result.Error)
}

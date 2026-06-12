package harness

import (
	"context"
	"fmt"
	"testing"
)

// scenarioRetryAttempts bounds how many times a single multi-step scenario is
// run end to end. Each attempt runs against a fresh gitea repo and fresh act
// containers, so a retry starts from a clean slate with no partial mutation
// carried over. Only transient act/docker execution failures consume an
// attempt; real assertion or job-level failures fail on the first attempt.
const scenarioRetryAttempts = 3

// logger is the minimal logging surface a scenario attempt needs. *testing.T
// satisfies it, and unit tests can supply a fake to assert on retry logging.
type logger interface {
	Logf(format string, args ...any)
}

// runScenarioWithRetry runs attempt up to scenarioRetryAttempts times, retrying
// ONLY when the attempt returns a transient act/docker execution failure
// (errors that wrap errTransientWorkflow). Any non-transient error - a real
// job-level failure, an expect_failure mismatch, or a state/branch/tag
// assertion mismatch - fails immediately without a retry. attempt must perform
// a full clean-slate run (fresh repo + fresh containers) on every call so a
// retry never inherits partial state from a prior attempt.
//
// It returns nil on the first successful attempt, or the last error after the
// attempt budget is exhausted.
func runScenarioWithRetry(ctx context.Context, log logger, name string, attempt func(ctx context.Context) error) error {
	var lastErr error
	for n := 1; n <= scenarioRetryAttempts; n++ {
		err := attempt(ctx)
		if err == nil {
			if n > 1 {
				log.Logf("scenario %q: passed on attempt %d/%d after transient retry", name, n, scenarioRetryAttempts)
			}
			return nil
		}
		// Real assertion failures and genuine job-level failures are
		// deterministic; surface them immediately so they fail the run.
		if !IsTransientWorkflowError(err) {
			return err
		}
		lastErr = err
		if n < scenarioRetryAttempts {
			log.Logf("scenario %q: transient act/docker execution failure on attempt %d/%d, retrying from a clean slate: %v",
				name, n, scenarioRetryAttempts, err)
			continue
		}
		log.Logf("scenario %q: exhausted %d attempts; last failure was transient: %v",
			name, scenarioRetryAttempts, err)
	}
	return fmt.Errorf("scenario %q failed after %d attempts: %w", name, scenarioRetryAttempts, lastErr)
}

// RunMultiStepScenario runs a whole multi-step scenario with a bounded
// scenario-level retry on transient act/docker execution failures. Each attempt
// builds a fresh harness (new docker network, gitea container + repo, act
// container), stages the repo from the scenario config, runs every step, and
// tears the harness down - so a retry is a clean slate with no carried-over
// mutation. This is the safe layer at which to retry: re-running a partial,
// state-mutating act run in place is not safe, but re-running an entire scenario
// from scratch is.
//
// It retries ONLY transient failures (act/docker could not execute the workflow
// to a real conclusion). A real job-level failure, an expect_failure mismatch,
// or any state/branch/tag assertion mismatch fails deterministically on the
// first attempt.
func RunMultiStepScenario(ctx context.Context, t *testing.T, scenario *MultiStepScenario) error {
	t.Helper()
	return runScenarioWithRetry(ctx, t, scenario.Name, func(ctx context.Context) error {
		h := New(t)
		defer h.Cleanup()

		if err := h.SetupInfra(ctx); err != nil {
			return fmt.Errorf("failed to setup infrastructure: %w", err)
		}
		if err := h.StageRepoFromConfig(ctx, scenario.Config); err != nil {
			return fmt.Errorf("failed to stage repo: %w", err)
		}

		runner := NewRunner(t, h)
		return runner.Run(ctx, scenario)
	})
}

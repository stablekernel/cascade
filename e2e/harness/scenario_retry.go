package harness

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

// scenarioRetryAttempts bounds how many times a single multi-step scenario is
// run end to end. Each attempt runs against a fresh gitea repo and fresh act
// containers, so a retry starts from a clean slate with no partial mutation
// carried over. Only transient act/docker execution failures consume an
// attempt; real assertion or job-level failures fail on the first attempt.
//
// Five attempts gives contention-driven transients a couple more chances under
// heavy CI load: the recovery logs show several scenarios passing on attempt 2
// or 3, so the mechanism works and the extra headroom covers the slowest tail.
const scenarioRetryAttempts = 5

// scenarioRetryBackoff is the pause between scenario attempts. It lets a burst
// of container/docker contention subside before the next clean-slate attempt
// rather than retrying instantly into the same pressure. It is a var, not a
// const, so unit tests can zero it to avoid real sleeps.
var scenarioRetryBackoff = 5 * time.Second

// pruneBetweenAttempts reclaims orphaned docker networks between attempts. It
// is a var so unit tests can replace it with a no-op (and so the production
// path stays a single, testable seam over the docker CLI).
var pruneBetweenAttempts = pruneDockerNetworks

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
			// Reclaim any docker networks orphaned by the failed attempt and
			// pause so contention can subside before the next clean-slate
			// attempt. Both are best effort: the attempt's own Cleanup already
			// removes its network, and a prune failure must not mask the
			// scenario result.
			pruneBetweenAttempts(ctx)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(scenarioRetryBackoff):
			}
			continue
		}
		log.Logf("scenario %q: exhausted %d attempts; last failure was transient: %v",
			name, scenarioRetryAttempts, err)
	}
	return fmt.Errorf("scenario %q failed after %d attempts: %w", name, scenarioRetryAttempts, lastErr)
}

// pruneDockerNetworks best-effort reclaims unused docker networks between
// scenario attempts so a network orphaned by a failed attempt cannot
// accumulate and exhaust the daemon's address pool. Any error is intentionally
// ignored: this is a defensive reclaim, not a correctness requirement.
func pruneDockerNetworks(ctx context.Context) {
	_ = exec.CommandContext(ctx, "docker", "network", "prune", "-f").Run()
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

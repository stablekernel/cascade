package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// scenarioRetryAttempts bounds how many times a single multi-step scenario is
// run end to end. Each attempt runs against a fresh gitea repo and fresh act
// containers, so a retry starts from a clean slate with no partial mutation
// carried over. Only NAMED infra-saturation transients (see
// detectInfraSaturation) consume an attempt; real assertion, crash, or
// job-level failures fail deterministically on the first attempt.
//
// Two attempts (one retry) is the safety net now that the harness concurrency
// cap (see scenarioConcurrency) removes the self-inflicted contention the old
// five-attempt budget existed to absorb. With oversubscription gone, a genuine
// named transient is a rare one-off that a single clean-slate retry clears; a
// deterministic failure must not be retried at all, so it fails on attempt 1.
const scenarioRetryAttempts = 2

// scenarioRetryBackoff is the pause between scenario attempts. It lets a burst
// of container/docker contention subside before the next clean-slate attempt
// rather than retrying instantly into the same pressure. It is a var, not a
// const, so unit tests can zero it to avoid real sleeps.
var scenarioRetryBackoff = 5 * time.Second

// pruneBetweenAttempts reclaims orphaned docker networks between attempts. It
// is a var so unit tests can replace it with a no-op (and so the production
// path stays a single, testable seam over the docker CLI).
var pruneBetweenAttempts = pruneDockerNetworks

// artifactDir is where the raw act stdout/stderr of an exhausted scenario is
// persisted so a future CI failure yields the stack-trace origin instead of an
// expired log. It is a var so unit tests can redirect it to a temp dir. The
// default lives under the e2e module so captured logs are easy to find and are
// covered by e2e/.gitignore (which ignores *.log).
var artifactDir = "_artifacts"

// artifactNameUnsafe matches any run of characters that are not safe in a
// cross-platform filename. The scenario name (which can contain spaces and
// slashes) is sanitized through it so each artifact path is a single, safe
// filename.
var artifactNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// persistAttemptEvidence writes the raw act logs carried by err to a per
// scenario+attempt artifact file and returns its path. It is best effort: if no
// logs are available or the write fails, it returns "" and the caller proceeds
// without an artifact reference (the scenario result must never be masked by an
// artifact-write failure). The filename is unique per scenario+attempt so
// parallel scenarios never collide.
func persistAttemptEvidence(scenario string, attempt int, err error) string {
	var we *workflowError
	if !errors.As(err, &we) {
		return ""
	}
	logs := we.actLogs()
	if logs == "" {
		return ""
	}
	if mkErr := os.MkdirAll(artifactDir, 0o755); mkErr != nil {
		return ""
	}
	safe := artifactNameUnsafe.ReplaceAllString(scenario, "_")
	path := filepath.Join(artifactDir, fmt.Sprintf("%s-attempt%d.log", safe, attempt))
	if wrErr := os.WriteFile(path, []byte(logs), 0o644); wrErr != nil {
		return ""
	}
	return path
}

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

	// The budget is exhausted. Persist the last attempt's raw act stdout/stderr
	// so the stack-trace origin survives the CI log's retention window, and
	// reference the artifact path in the returned error.
	if path := persistAttemptEvidence(name, scenarioRetryAttempts, lastErr); path != "" {
		log.Logf("scenario %q: wrote last-attempt act log to %s", name, path)
		return fmt.Errorf("scenario %q failed after %d attempts (act log: %s): %w",
			name, scenarioRetryAttempts, path, lastErr)
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
//
// Each attempt acquires a scenarioConcurrency slot BEFORE standing up its
// docker/gitea/act stack and releases it AFTER teardown, so no more than the
// host-derived cap of heavyweight stacks exist at once regardless of
// `go test -parallel`. This is what prevents the self-inflicted oversubscription
// (docker address pool / RAM / gitea) that the retry budget used to mask.
func RunMultiStepScenario(ctx context.Context, t *testing.T, scenario *MultiStepScenario) error {
	t.Helper()
	return runScenarioWithRetry(ctx, t, scenario.Name, func(ctx context.Context) error {
		// Bound concurrent stack standup independent of -parallel. Acquire before
		// SetupInfra so the docker network + gitea + act containers (and act's
		// nested job containers) only come up once a slot is free; release after
		// Cleanup so the slot covers the stack's entire lifetime.
		release, err := acquireScenarioSlot(ctx)
		if err != nil {
			return err
		}
		defer release()

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

package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeLogger captures formatted log lines for assertions.
type fakeLogger struct {
	mu    sync.Mutex
	lines []string
}

func (f *fakeLogger) Logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = append(f.lines, fmt.Sprintf(format, args...))
}

func (f *fakeLogger) contains(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func transientErr() error {
	return fmt.Errorf("promote workflow failed: workflow execution failed: %w", errTransientWorkflow)
}

// fastRetries removes the inter-attempt pause and stubs out the docker network
// prune for the duration of a test so the retry loop neither sleeps nor shells
// out to docker in unit tests, and restores both on cleanup. It records how
// many times the prune seam fired so callers can assert prune-between-attempts
// behaviour. These globals are shared, so a test using this must not run in
// parallel.
func fastRetries(t *testing.T) *int {
	t.Helper()
	prevBackoff := scenarioRetryBackoff
	prevPrune := pruneBetweenAttempts
	prunes := 0
	scenarioRetryBackoff = 0
	pruneBetweenAttempts = func(context.Context) { prunes++ }
	t.Cleanup(func() {
		scenarioRetryBackoff = prevBackoff
		pruneBetweenAttempts = prevPrune
	})
	return &prunes
}

func TestRunScenarioWithRetry_PassesFirstAttempt(t *testing.T) {
	t.Parallel()

	log := &fakeLogger{}
	calls := 0
	err := runScenarioWithRetry(context.Background(), log, "Scenario", func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1", calls)
	}
	if log.contains("retry") {
		t.Fatalf("did not expect a retry log on first-attempt pass: %v", log.lines)
	}
}

func TestRunScenarioWithRetry_RecoversAfterTransient(t *testing.T) {
	prunes := fastRetries(t)

	log := &fakeLogger{}
	calls := 0
	err := runScenarioWithRetry(context.Background(), log, "Hotfix_Stacked", func(context.Context) error {
		calls++
		if calls < 2 {
			return transientErr()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected recovery, got error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("attempts = %d, want 2", calls)
	}
	// One failed attempt preceded the recovery, so the prune-between-attempts
	// seam must have fired exactly once.
	if *prunes != 1 {
		t.Fatalf("prune-between-attempts fired %d times, want 1", *prunes)
	}
	if !log.contains("transient act/docker execution failure on attempt 1") {
		t.Fatalf("expected a transient-retry log line, got: %v", log.lines)
	}
	if !log.contains("passed on attempt 2") {
		t.Fatalf("expected a recovery log line, got: %v", log.lines)
	}
}

// TestRunScenarioWithRetry_AssertionFailsImmediately is the critical safety
// guarantee: a real assertion failure (here, an expect_failure mismatch) must
// NOT be retried and must surface on the first attempt.
func TestRunScenarioWithRetry_AssertionFailsImmediately(t *testing.T) {
	t.Parallel()

	assertionErr := errors.New("expected promote to fail but it succeeded")
	log := &fakeLogger{}
	calls := 0
	err := runScenarioWithRetry(context.Background(), log, "Promote_rolls_back", func(context.Context) error {
		calls++
		return assertionErr
	})
	if !errors.Is(err, assertionErr) {
		t.Fatalf("expected the assertion error to surface, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("assertion failure must not retry: attempts = %d, want 1", calls)
	}
	if log.contains("retry") {
		t.Fatalf("must not log a retry for an assertion failure: %v", log.lines)
	}
}

// TestRunScenarioWithRetry_RealJobFailureNotRetried guards that a genuine
// job-level workflow failure (ExecError false) is treated as deterministic.
func TestRunScenarioWithRetry_RealJobFailureNotRetried(t *testing.T) {
	t.Parallel()

	realFailure := workflowFailureError("orchestrate", &ExtendedWorkflowResult{
		Conclusion: "failure",
		Error:      "build job concluded failure",
	})
	log := &fakeLogger{}
	calls := 0
	err := runScenarioWithRetry(context.Background(), log, "Four_Environment_Cascade_Promotion", func(context.Context) error {
		calls++
		return realFailure
	})
	if err == nil {
		t.Fatal("expected the real failure to surface")
	}
	if calls != 1 {
		t.Fatalf("real job failure must not retry: attempts = %d, want 1", calls)
	}
}

func TestRunScenarioWithRetry_ExhaustsAttemptsOnPersistentTransient(t *testing.T) {
	prunes := fastRetries(t)

	log := &fakeLogger{}
	calls := 0
	err := runScenarioWithRetry(context.Background(), log, "Flaky", func(context.Context) error {
		calls++
		return transientErr()
	})
	if err == nil {
		t.Fatal("expected an error after exhausting attempts")
	}
	if calls != scenarioRetryAttempts {
		t.Fatalf("attempts = %d, want %d", calls, scenarioRetryAttempts)
	}
	// A prune runs between attempts but not after the final one.
	if *prunes != scenarioRetryAttempts-1 {
		t.Fatalf("prune-between-attempts fired %d times, want %d", *prunes, scenarioRetryAttempts-1)
	}
	if !IsTransientWorkflowError(err) {
		t.Fatalf("final error should still wrap the transient sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("after %d attempts", scenarioRetryAttempts)) {
		t.Fatalf("final error should report the attempt count: %v", err)
	}
}

// TestRunScenarioWithRetry_PersistsEvidenceOnExhaustion verifies that when a
// scenario exhausts its retry budget, the last attempt's full act stdout/stderr
// is written to an artifact file and the path is referenced in the returned
// error, so a future CI failure yields the stack-trace origin instead of an
// expired log.
func TestRunScenarioWithRetry_PersistsEvidenceOnExhaustion(t *testing.T) {
	fastRetries(t)

	dir := t.TempDir()
	prev := artifactDir
	artifactDir = dir
	t.Cleanup(func() { artifactDir = prev })

	const dump = "panic: boom\n\ngoroutine 1 [running]:\nruntime.gopanic(...)\n"
	log := &fakeLogger{}
	err := runScenarioWithRetry(context.Background(), log, "Hotfix Promote Guards", func(context.Context) error {
		// A transient-classified failure that nonetheless carries the raw act
		// logs (the masking case the harness must preserve evidence for).
		return workflowExecError("orchestrate", &ExtendedWorkflowResult{
			Conclusion: "failure",
			Error:      "workflow execution failed",
			Logs:       dump,
			ExecError:  true,
		})
	})
	if err == nil {
		t.Fatal("expected an error after exhausting attempts")
		return
	}

	// The error must reference a written artifact path.
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("error should reference the artifact dir %q: %v", dir, err)
	}

	// Exactly one artifact file for this scenario+last-attempt must exist and
	// contain the raw dump.
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read artifact dir: %v", readErr)
	}
	if len(entries) == 0 {
		t.Fatalf("expected an artifact log to be written, dir empty")
	}
	var found bool
	for _, e := range entries {
		b, rdErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rdErr != nil {
			t.Fatalf("read artifact %q: %v", e.Name(), rdErr)
		}
		if strings.Contains(string(b), "panic: boom") {
			found = true
			// The filename must be unique per scenario+attempt and filesystem
			// safe (no spaces or slashes from the scenario name).
			if strings.ContainsAny(e.Name(), " /") {
				t.Fatalf("artifact name %q is not filesystem safe", e.Name())
			}
		}
	}
	if !found {
		t.Fatalf("no artifact contained the raw act dump; entries=%v", entries)
	}
}

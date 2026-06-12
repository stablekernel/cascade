package harness

import (
	"context"
	"errors"
	"fmt"
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
	t.Parallel()

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
	t.Parallel()

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
	if !IsTransientWorkflowError(err) {
		t.Fatalf("final error should still wrap the transient sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("after %d attempts", scenarioRetryAttempts)) {
		t.Fatalf("final error should report the attempt count: %v", err)
	}
}

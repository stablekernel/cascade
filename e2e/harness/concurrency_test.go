package harness

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParsePositiveInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		wantN  int
		wantOK bool
	}{
		{name: "empty falls back", in: "", wantN: 0, wantOK: false},
		{name: "zero falls back", in: "0", wantN: 0, wantOK: false},
		{name: "negative falls back", in: "-3", wantN: 0, wantOK: false},
		{name: "non-numeric falls back", in: "abc", wantN: 0, wantOK: false},
		{name: "one is honored", in: "1", wantN: 1, wantOK: true},
		{name: "larger value is honored", in: "6", wantN: 6, wantOK: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n, ok := parsePositiveInt(tt.in)
			if n != tt.wantN || ok != tt.wantOK {
				t.Fatalf("parsePositiveInt(%q) = (%d,%v), want (%d,%v)", tt.in, n, ok, tt.wantN, tt.wantOK)
			}
		})
	}
}

// TestComputeScenarioConcurrency_EnvOverride pins the cap via E2E_MAX_CONCURRENT
// and confirms a positive override wins over the host-derived default while an
// invalid one falls back to a sane (>=1) host value.
func TestComputeScenarioConcurrency_EnvOverride(t *testing.T) {
	t.Setenv(maxConcurrentEnv, "3")
	if got := computeScenarioConcurrency(); got != 3 {
		t.Fatalf("computeScenarioConcurrency() with override=3 = %d, want 3", got)
	}

	t.Setenv(maxConcurrentEnv, "bogus")
	if got := computeScenarioConcurrency(); got < 1 {
		t.Fatalf("computeScenarioConcurrency() with invalid override = %d, want >= 1", got)
	}
}

// TestComputeScenarioConcurrency_HostDefault confirms the default is always at
// least 1 (the suite must make progress on any host) when no override is set.
func TestComputeScenarioConcurrency_HostDefault(t *testing.T) {
	t.Setenv(maxConcurrentEnv, "")
	if got := computeScenarioConcurrency(); got < 1 {
		t.Fatalf("computeScenarioConcurrency() host default = %d, want >= 1", got)
	}
}

// TestAcquireScenarioSlot_BoundsConcurrency proves the semaphore never admits
// more than its capacity at once: with a cap of 2 and many contending
// goroutines, the observed in-flight count never exceeds 2.
func TestAcquireScenarioSlot_BoundsConcurrency(t *testing.T) {
	const capLimit = 2
	// Swap the package semaphore for a capacity-controlled one for this test,
	// then restore it so other tests see the real cap.
	orig := scenarioConcurrency
	scenarioConcurrency = make(chan struct{}, capLimit)
	t.Cleanup(func() { scenarioConcurrency = orig })

	var (
		inFlight int32
		maxSeen  int32
		wg       sync.WaitGroup
	)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := acquireScenarioSlot(context.Background())
			if err != nil {
				t.Errorf("acquireScenarioSlot returned error: %v", err)
				return
			}
			defer release()

			cur := atomic.AddInt32(&inFlight, 1)
			for {
				prev := atomic.LoadInt32(&maxSeen)
				if cur <= prev || atomic.CompareAndSwapInt32(&maxSeen, prev, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
		}()
	}
	wg.Wait()

	if maxSeen > capLimit {
		t.Fatalf("max concurrent slots = %d, want <= %d", maxSeen, capLimit)
	}
	// All slots must have been returned: a fresh acquire must not block.
	release, err := acquireScenarioSlot(context.Background())
	if err != nil {
		t.Fatalf("post-run acquire errored: %v", err)
	}
	release()
}

// TestAcquireScenarioSlot_ContextCancelled confirms a caller cancelled while the
// semaphore is full gets a non-nil error and a no-op release (never blocks,
// never frees a slot it does not hold).
func TestAcquireScenarioSlot_ContextCancelled(t *testing.T) {
	orig := scenarioConcurrency
	scenarioConcurrency = make(chan struct{}, 1)
	t.Cleanup(func() { scenarioConcurrency = orig })

	// Fill the only slot.
	hold, err := acquireScenarioSlot(context.Background())
	if err != nil {
		t.Fatalf("initial acquire errored: %v", err)
	}
	defer hold()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, err := acquireScenarioSlot(ctx)
	if err == nil {
		t.Fatalf("acquireScenarioSlot with cancelled ctx returned nil error")
	}
	// The returned release must be a safe no-op (must not free the held slot).
	release()
	select {
	case scenarioConcurrency <- struct{}{}:
		t.Fatalf("no-op release freed a slot it did not own (semaphore under-counted)")
	default:
	}
}

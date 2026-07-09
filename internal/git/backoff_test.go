package git

import (
	"testing"
	"time"
)

// TestBackoffForAttempt_GrowthCapAndJitter drives the unexported backoffForAttempt
// directly. The push-retry tests inject a no-op sleep that counts calls but
// discards the duration, so only the retry ceiling is asserted; the timing curve
// itself is otherwise unverified. This pins the three documented properties of the
// pure function, with no real sleeping: the deterministic floor doubles per
// attempt, is capped at maxPushBackoff, and the jitter added on top stays within
// [0, base].
func TestBackoffForAttempt_GrowthCapAndJitter(t *testing.T) {
	const base = defaultPushBackoff

	// floorForAttempt is the deterministic (pre-jitter) component: base doubled per
	// attempt, capped at maxPushBackoff. It mirrors the growth loop in
	// backoffForAttempt so the jitter can be isolated as got-floor.
	floorForAttempt := func(attempt int) time.Duration {
		d := base
		for i := 0; i < attempt && d < maxPushBackoff; i++ {
			d *= 2
		}
		if d > maxPushBackoff {
			d = maxPushBackoff
		}
		return d
	}

	sawJitter := false
	for attempt := 0; attempt <= 8; attempt++ {
		floor := floorForAttempt(attempt)

		// Doubling: each floor is twice the previous until the cap clamps it.
		if attempt > 0 {
			want := floorForAttempt(attempt-1) * 2
			if want > maxPushBackoff {
				want = maxPushBackoff
			}
			if floor != want {
				t.Fatalf("attempt %d floor = %v, want %v (doubling/cap)", attempt, floor, want)
			}
		}

		// Every jittered sample must land in [floor, floor+base]; at least one
		// sample across the sweep must exceed the floor, proving jitter is applied.
		for i := 0; i < 2000; i++ {
			got := backoffForAttempt(base, attempt)
			if got < floor || got > floor+base {
				t.Fatalf("attempt %d backoff = %v, want within [%v, %v]", attempt, got, floor, floor+base)
			}
			if got > floor {
				sawJitter = true
			}
		}
	}

	// The cap holds: a very high attempt never grows the floor past maxPushBackoff.
	if got := floorForAttempt(64); got != maxPushBackoff {
		t.Fatalf("floor at attempt 64 = %v, want cap %v", got, maxPushBackoff)
	}
	// The live function honours the cap too: at a high attempt the result stays in
	// [maxPushBackoff, maxPushBackoff+base].
	for i := 0; i < 2000; i++ {
		got := backoffForAttempt(base, 64)
		if got < maxPushBackoff || got > maxPushBackoff+base {
			t.Fatalf("capped backoff = %v, want within [%v, %v]", got, maxPushBackoff, maxPushBackoff+base)
		}
	}
	if !sawJitter {
		t.Fatal("jitter never observed across 8 attempts; de-sync jitter is not applied")
	}

	// A zero/negative base falls back to defaultPushBackoff rather than collapsing
	// to a fixed no-backoff.
	if got := backoffForAttempt(0, 0); got < defaultPushBackoff || got > 2*defaultPushBackoff {
		t.Fatalf("zero-base backoff = %v, want within [%v, %v]", got, defaultPushBackoff, 2*defaultPushBackoff)
	}
}

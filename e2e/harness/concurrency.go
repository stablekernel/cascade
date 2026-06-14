package harness

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"sync"
)

// maxConcurrentEnv is the environment variable that overrides the computed
// scenario-concurrency cap. A positive integer pins the cap exactly (clamped to
// at least 1); any other value (unset, empty, zero, negative, non-numeric) falls
// back to the host-derived default. This lets a constrained or oversized runner
// tune the cap without a code change, e.g. E2E_MAX_CONCURRENT=1 to force fully
// serial heavy-scenario setup.
const maxConcurrentEnv = "E2E_MAX_CONCURRENT"

// scenarioConcurrency is a process-wide semaphore that bounds how many scenarios
// stand up their docker/gitea/act stack CONCURRENTLY, independent of
// `go test -parallel`. Each scenario stands up its own docker network + gitea
// container + act container, and act spawns nested job containers per workflow
// run; the heaviest (hotfix-family) scenarios hold many concurrent network
// endpoints, containers, and memory. Run enough of them at once and they
// oversubscribe the docker address pool (#125), host RAM, or gitea (#121) - the
// self-inflicted contention that the old five-attempt retry budget masked.
// `go test -parallel` alone cannot bound this because it counts test functions,
// not the heavyweight infra each one provisions. This semaphore caps the real
// resource pressure so heavy scenarios serialize against each other under load
// and infra-saturation transients stop happening in the first place.
var scenarioConcurrency = make(chan struct{}, computeScenarioConcurrency())

// computeScenarioConcurrency derives the concurrency cap. An E2E_MAX_CONCURRENT
// override (positive integer) wins. Otherwise the cap is host-derived and
// deliberately conservative, because each admitted scenario provisions a full
// docker/gitea/act stack plus nested job containers:
//
//   - From CPUs: floor(NumCPU/2), so a 4-core runner admits 2 and an 8-core
//     runner admits 4. Halving leaves headroom for the docker daemon, gitea, and
//     act's own orchestration rather than saturating every core with job
//     containers.
//   - From memory: floor(totalGiB/3), budgeting ~3 GiB per concurrent stack
//     (gitea + act + nested job containers). On an 8 GiB runner that is 2.
//   - The cap is the MIN of the two (the binding resource), floored at 1 so the
//     suite always makes progress even on a tiny host. Total memory is read
//     best-effort; when it is unknown the CPU bound alone applies.
//
// On the diagnosis's 4-core / ~8 GiB reference runner this yields min(2, 2) = 2,
// which keeps the heavy hotfix scenarios from oversubscribing docker/gitea.
func computeScenarioConcurrency() int {
	if n, ok := parsePositiveInt(os.Getenv(maxConcurrentEnv)); ok {
		return n
	}

	limit := runtime.NumCPU() / 2
	if limit < 1 {
		limit = 1
	}

	if gib := totalMemoryGiB(); gib > 0 {
		memLimit := gib / 3
		if memLimit < 1 {
			memLimit = 1
		}
		if memLimit < limit {
			limit = memLimit
		}
	}

	return limit
}

// parsePositiveInt parses s as a positive integer, returning (n, true) only for
// a strictly-positive value. Empty, non-numeric, zero, and negative inputs
// return (0, false) so the caller falls back to the host-derived default.
func parsePositiveInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// acquireScenarioSlot blocks until a scenario-concurrency slot is free (or ctx
// is cancelled), then returns a release func the caller must invoke (via defer)
// to return the slot. Acquiring before SetupInfra and releasing after Cleanup
// bounds how many docker/gitea/act stacks exist at once. On ctx cancellation it
// returns a non-nil error and a no-op release, so a cancelled caller neither
// blocks nor double-releases.
func acquireScenarioSlot(ctx context.Context) (release func(), err error) {
	select {
	case scenarioConcurrency <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-scenarioConcurrency }) }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}

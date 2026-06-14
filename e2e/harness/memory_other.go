//go:build !darwin && !linux

package harness

// totalMemoryGiB returns 0 on platforms without a best-effort total-memory
// probe, so the scenario-concurrency cap falls back to the CPU-only bound. The
// e2e suite runs on darwin and linux; this stub keeps the package buildable
// everywhere without a platform-specific dependency.
func totalMemoryGiB() int { return 0 }

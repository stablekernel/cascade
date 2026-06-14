//go:build darwin

package harness

import (
	"os/exec"
	"strconv"
	"strings"
)

// totalMemoryGiB returns the host's total physical memory in whole GiB on
// darwin, read best-effort via `sysctl -n hw.memsize` (bytes). It returns 0 when
// the value cannot be read or parsed so the caller falls back to the CPU-only
// concurrency bound. Reading via sysctl avoids promoting a memory-introspection
// dependency to direct for what is a best-effort sizing hint.
func totalMemoryGiB() int {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	bytes, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return int(bytes / (1 << 30))
}

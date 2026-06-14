//go:build linux

package harness

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// totalMemoryGiB returns the host's total physical memory in whole GiB on
// linux, read best-effort from /proc/meminfo's MemTotal (kibibytes). It returns
// 0 when the file cannot be read or the field cannot be parsed so the caller
// falls back to the CPU-only concurrency bound. Reading /proc directly avoids
// promoting a memory-introspection dependency to direct for what is a
// best-effort sizing hint.
func totalMemoryGiB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// MemTotal:       16331156 kB
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return int(kib / (1 << 20))
		}
	}
	return 0
}

// Package globals provides access to global CLI flags and state.
// These are set by the root command and can be read by subcommands.
package globals

import "sync"

var (
	mu     sync.RWMutex
	dryRun bool
	json   bool
)

// SetDryRun sets the dry-run mode flag.
func SetDryRun(v bool) {
	mu.Lock()
	defer mu.Unlock()
	dryRun = v
}

// DryRun returns true if dry-run mode is enabled.
func DryRun() bool {
	mu.RLock()
	defer mu.RUnlock()
	return dryRun
}

// SetJSON sets the JSON output mode flag.
func SetJSON(v bool) {
	mu.Lock()
	defer mu.Unlock()
	json = v
}

// JSON returns true if JSON output mode is enabled.
func JSON() bool {
	mu.RLock()
	defer mu.RUnlock()
	return json
}

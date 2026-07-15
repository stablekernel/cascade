package globals

import "fmt"

// GuardMutation is the dry-run backstop at the lowest boundaries that mutate
// external state: GitHub API writes (releases, tags, branch protection, the
// contents API) and git pushes. Every command gates --dry-run explicitly and
// prints its preview before any of these boundaries is reached, so on a
// correct dry run this guard is never hit and on a real run it is a no-op.
//
// If a mutation path misses its explicit gate, the guard refuses the
// operation with a loud error rather than silently mutating (the data-safety
// failure) or silently skipping (which would hide the missing gate). Callers
// place it immediately before executing the state-changing request or
// command, never on read paths.
func GuardMutation(operation string) error {
	if !DryRun() {
		return nil
	}
	return fmt.Errorf("dry-run guard: refusing to %s: --dry-run is set but this mutation path did not gate on it; no changes were made. This is a cascade bug, please report it at https://github.com/stablekernel/cascade/issues", operation)
}

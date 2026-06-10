package generate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// This file holds the emit helpers for the per-callback job attributes that
// apply to cascade-owned inline run: jobs: runs-on (#12), permissions (#35,
// including the id-token: write OIDC use case #15), and concurrency (#17).
//
// These attributes are valid ONLY on cascade-owned jobs (inline run: callbacks
// and cascade-owned setup/finalize jobs), never on reusable-workflow
// jobs.<id>.uses callbacks (GHA forbids runs-on/concurrency there). Schema
// validation already rejects runs_on/concurrency on reusable callbacks, so the
// generator only ever calls these helpers from the inline-run emit paths.

// defaultRunner is the runner used for cascade-owned jobs when neither a
// per-callback runs_on nor a config-level runs_on default is set.
const defaultRunner = "ubuntu-latest"

// resolveRunsOn returns the runs-on value (as a YAML scalar/flow string) for a
// cascade-owned job, applying the precedence: per-callback runs_on > config-level
// runs_on default > "ubuntu-latest". It is rendered inline after "runs-on: ".
func resolveRunsOn(callback *config.RunsOn, configDefault *config.RunsOn) string {
	if v := renderRunsOnValue(callback); v != "" {
		return v
	}
	if v := renderRunsOnValue(configDefault); v != "" {
		return v
	}
	return defaultRunner
}

// renderRunsOnValue renders a *config.RunsOn into the value that follows
// "runs-on: " on a single line. It handles the three union forms:
//
//   - scalar label        -> ubuntu-latest
//   - list of labels      -> [self-hosted, linux, arm64]
//   - {group[, labels]}   -> {group: my-group, labels: [self-hosted]}
//
// Returns "" when r is nil or carries no usable value, so callers can fall back
// to the next precedence level.
func renderRunsOnValue(r *config.RunsOn) string {
	if r == nil {
		return ""
	}
	switch {
	case r.Group != "":
		if len(r.Labels) > 0 {
			return fmt.Sprintf("{group: %s, labels: [%s]}", r.Group, strings.Join(r.Labels, ", "))
		}
		return fmt.Sprintf("{group: %s}", r.Group)
	case len(r.Labels) > 0:
		return fmt.Sprintf("[%s]", strings.Join(r.Labels, ", "))
	case r.Label != "":
		return r.Label
	default:
		return ""
	}
}

// writeRunsOn emits the "runs-on:" line for a cascade-owned job at the given
// indent (e.g. "    " for a job-level key). The value is resolved via
// resolveRunsOn so the config-level default and "ubuntu-latest" fallback apply.
func writeRunsOn(sb *strings.Builder, indent string, callback, configDefault *config.RunsOn) {
	fmt.Fprintf(sb, "%sruns-on: %s\n", indent, resolveRunsOn(callback, configDefault))
}

// writeJobPermissions emits a "permissions:" map on a cascade-owned job from the
// callback's permissions. When perms is empty nothing is emitted, so the
// workflow-level permissions continue to apply (backward-compatible). The
// id-token: write OIDC use case is just one entry in this map (#15) and needs no
// dedicated field. Keys are emitted in deterministic (sorted) order.
func writeJobPermissions(sb *strings.Builder, indent string, perms map[string]string) {
	if len(perms) == 0 {
		return
	}
	fmt.Fprintf(sb, "%spermissions:\n", indent)
	scopes := make([]string, 0, len(perms))
	for scope := range perms {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	for _, scope := range scopes {
		fmt.Fprintf(sb, "%s  %s: %s\n", indent, scope, perms[scope])
	}
}

// writeJobConcurrency emits a job-level "concurrency:" block on a cascade-owned
// job from the callback's concurrency config. When c is nil nothing is emitted.
// A bare group renders as a scalar; a group plus cancel_in_progress renders as
// the object form. cancel_in_progress is always emitted (the zero value false is
// meaningful: queue rather than cancel).
func writeJobConcurrency(sb *strings.Builder, indent string, c *config.ConcurrencyConfig) {
	if c == nil {
		return
	}
	fmt.Fprintf(sb, "%sconcurrency:\n", indent)
	fmt.Fprintf(sb, "%s  group: %s\n", indent, c.Group)
	fmt.Fprintf(sb, "%s  cancel-in-progress: %t\n", indent, c.CancelInProgress)
}

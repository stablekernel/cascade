package simulate

import (
	"sort"
	"strconv"

	"github.com/stablekernel/cascade/internal/config"
)

// noneValue is the placeholder rendered for an empty field value.
const noneValue = "(none)"

// FieldChange records a single field transitioning from one value to another.
// Changed is true only when From and To differ.
type FieldChange struct {
	Field   string `json:"field"`
	From    string `json:"from"`
	To      string `json:"to"`
	Changed bool   `json:"changed"`
}

// newFieldChange builds a FieldChange, rendering empty values as (none).
func newFieldChange(field, from, to string) FieldChange {
	return FieldChange{
		Field:   field,
		From:    orNone(from),
		To:      orNone(to),
		Changed: from != to,
	}
}

func orNone(s string) string {
	if s == "" {
		return noneValue
	}
	return s
}

// DeployDiff captures the field changes for a single named deploy within an
// environment.
type DeployDiff struct {
	Name    string      `json:"name"`
	SHA     FieldChange `json:"sha"`
	Version FieldChange `json:"version"`
}

// changed reports whether any tracked field of the deploy changed.
func (d DeployDiff) changed() bool {
	return d.SHA.Changed || d.Version.Changed
}

// EnvDiff captures the field-by-field changes for a single environment. The
// run-stamped CommittedAt and CommittedBy fields are deliberately excluded:
// timestamps are run-stamped and not part of the what-if outcome.
type EnvDiff struct {
	Environment  string       `json:"environment"`
	Version      FieldChange  `json:"version"`
	SHA          FieldChange  `json:"sha"`
	Divergence   FieldChange  `json:"divergence"`
	PreviousRing FieldChange  `json:"previous_ring"`
	Deploys      []DeployDiff `json:"deploys,omitempty"`
}

// changed reports whether any tracked field of the environment changed.
func (e EnvDiff) changed() bool {
	if e.Version.Changed || e.SHA.Changed || e.Divergence.Changed || e.PreviousRing.Changed {
		return true
	}
	for _, d := range e.Deploys {
		if d.changed() {
			return true
		}
	}
	return false
}

// StateDiff is the difference between a before and after manifest state, keyed
// by environment name in deterministic (sorted) order.
type StateDiff struct {
	// Envs holds the per-environment diffs in sorted-key order.
	Envs []EnvDiff `json:"envs"`
}

// Changed reports whether any environment in the diff changed.
func (s StateDiff) Changed() bool {
	for _, e := range s.Envs {
		if e.changed() {
			return true
		}
	}
	return false
}

// Env returns the diff for a single environment by name.
func (s StateDiff) Env(name string) (EnvDiff, bool) {
	for _, e := range s.Envs {
		if e.Environment == name {
			return e, true
		}
	}
	return EnvDiff{}, false
}

// DiffState computes the StateDiff between before and after environment states.
// Environment keys are sorted for deterministic output. Run-stamped timestamps
// (CommittedAt, CommittedBy) are ignored so output stays stable across runs.
func DiffState(before, after map[string]*config.EnvState) StateDiff {
	names := unionKeys(before, after)
	sort.Strings(names)

	diff := StateDiff{}
	for _, name := range names {
		b := before[name]
		a := after[name]
		ed := diffEnv(name, b, a)
		if ed.changed() {
			diff.Envs = append(diff.Envs, ed)
		}
	}
	return diff
}

func diffEnv(name string, before, after *config.EnvState) EnvDiff {
	b := stateOrEmpty(before)
	a := stateOrEmpty(after)

	ed := EnvDiff{
		Environment:  name,
		Version:      newFieldChange("version", b.Version, a.Version),
		SHA:          newFieldChange("sha", b.SHA, a.SHA),
		Divergence:   newFieldChange("divergence", boolWord(before.IsDiverged()), boolWord(after.IsDiverged())),
		PreviousRing: newFieldChange("previous_ring", strconv.Itoa(len(b.Previous)), strconv.Itoa(len(a.Previous))),
		Deploys:      diffDeploys(b.Deploys, a.Deploys),
	}
	return ed
}

func diffDeploys(before, after map[string]*config.DeployState) []DeployDiff {
	names := deployUnionKeys(before, after)
	sort.Strings(names)

	var out []DeployDiff
	for _, name := range names {
		b := deployOrEmpty(before[name])
		a := deployOrEmpty(after[name])
		dd := DeployDiff{
			Name:    name,
			SHA:     newFieldChange("sha", b.SHA, a.SHA),
			Version: newFieldChange("version", b.Version, a.Version),
		}
		if dd.changed() {
			out = append(out, dd)
		}
	}
	return out
}

func unionKeys(before, after map[string]*config.EnvState) []string {
	seen := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		seen[k] = struct{}{}
	}
	for k := range after {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

func deployUnionKeys(before, after map[string]*config.DeployState) []string {
	seen := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		seen[k] = struct{}{}
	}
	for k := range after {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

func stateOrEmpty(s *config.EnvState) *config.EnvState {
	if s == nil {
		return &config.EnvState{}
	}
	return s
}

func deployOrEmpty(d *config.DeployState) *config.DeployState {
	if d == nil {
		return &config.DeployState{}
	}
	return d
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// Package coverage records, for every workflow kind the generator emits, the
// executing coverage that exercises it: e2e scenarios, e2e harness tests, and
// fleet lanes. The map is a positive record of what runs each generated
// workflow. A companion test derives the full set of emitted workflow kinds
// from the generator source and fails when an emitted kind has no entry here, so
// a new generated workflow cannot ship without a scenario or lane that runs it.
package coverage

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed registry.yaml
var registryYAML []byte

// Coverage lists the executing references that exercise a single workflow kind.
// A kind is covered when it names at least one scenario, harness test, or fleet
// lane that runs the generated workflow.
type Coverage struct {
	Summary    string   `yaml:"summary"`
	Scenarios  []string `yaml:"scenarios,omitempty"`
	E2ETests   []string `yaml:"e2e_tests,omitempty"`
	FleetLanes []string `yaml:"fleet_lanes,omitempty"`
}

// Refs reports the number of executing-coverage references recorded for a kind.
func (c Coverage) Refs() int {
	return len(c.Scenarios) + len(c.E2ETests) + len(c.FleetLanes)
}

// Registry maps each generated workflow kind to its executing coverage.
type Registry struct {
	Kinds map[string]Coverage `yaml:"kinds"`
}

// KnownFleetLanes is the canonical fleet roster, mirroring the single source of
// truth in .github/workflows/fleet-e2e.yaml (the Select lanes step). A fleet
// lane reference in the registry must name one of these.
var KnownFleetLanes = map[string]struct{}{
	"primary":           {},
	"artifact-a":        {},
	"artifact-b":        {},
	"4env":              {},
	"3env":              {},
	"2env":              {},
	"single-env":        {},
	"release-only":      {},
	"no-env":            {},
	"callbacks":         {},
	"rollback-dispatch": {},
	"monorepo":          {},
}

// Load parses the embedded coverage registry.
func Load() (*Registry, error) {
	var r Registry
	if err := yaml.Unmarshal(registryYAML, &r); err != nil {
		return nil, fmt.Errorf("parsing coverage registry: %w", err)
	}
	return &r, nil
}

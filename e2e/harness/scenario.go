package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
)

// Scenario represents a complete E2E test scenario
type Scenario struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description"`
	Setup       Setup   `yaml:"setup"`
	Trigger     Trigger `yaml:"trigger"`
	Expect      Expect  `yaml:"expect"`
}

// Setup defines the initial repository state
type Setup struct {
	Config   Config         `yaml:"config"`
	Commits  []Commit       `yaml:"commits"`
	Manifest map[string]any `yaml:"manifest"`
	Tags     []string       `yaml:"tags"`
	Releases []ReleaseSetup `yaml:"releases"`
}

// Config is the scenario's trunk-config block. It is a direct alias of
// config.TrunkConfig, the cascade CLI's own manifest type, so every field the
// CLI understands is marshalable into a scenario's generated manifest.yaml with
// no parallel struct to keep in sync. The prior hand-mirrored struct silently
// dropped any manifest field nobody remembered to copy across, and each new
// generator feature needed a matching harness edit before a scenario could
// reach it. Reusing the source of truth removes that failure mode: a field
// added to config.TrunkConfig is reachable from a scenario immediately (#386).
// The multi-repo path already carries config.TrunkConfig directly, so
// single-step and multi-step scenarios now match it.
type Config = config.TrunkConfig

// Commit defines a commit to create
type Commit struct {
	Message string            `yaml:"message"`
	Files   map[string]string `yaml:"files"`
}

// ReleaseSetup defines a release to create during setup
type ReleaseSetup struct {
	Tag        string `yaml:"tag"`
	Prerelease bool   `yaml:"prerelease"`
}

// Trigger defines how to invoke the workflow
type Trigger struct {
	Workflow  string            `yaml:"workflow"`
	Event     string            `yaml:"event"`
	Inputs    map[string]string `yaml:"inputs"`
	EventJSON string            `yaml:"event_json"`
}

// Expect defines expected outcomes
type Expect struct {
	Tags     []TagExpect     `yaml:"tags"`
	Manifest map[string]any  `yaml:"manifest"`
	Workflow WorkflowExpect  `yaml:"workflow"`
	Releases []ReleaseExpect `yaml:"releases"`
}

// TagExpect defines an expected tag
type TagExpect struct {
	Pattern string `yaml:"pattern"`
	OnSHA   string `yaml:"on_sha"`
}

// WorkflowExpect defines expected workflow outcome
type WorkflowExpect struct {
	Conclusion string               `yaml:"conclusion"`
	Jobs       map[string]JobExpect `yaml:"jobs"`
}

// JobExpect defines expected job outcome
type JobExpect struct {
	Conclusion string `yaml:"conclusion"`
}

// ReleaseExpect defines an expected release
type ReleaseExpect struct {
	Tag        string `yaml:"tag"`
	Prerelease bool   `yaml:"prerelease"`
}

// ParseScenario parses YAML bytes into a Scenario. Decoding is strict: a key the
// schema does not define is an error rather than a silently dropped field, so a
// typo cannot quietly erase an expectation and leave the scenario green. An
// empty document decodes to a zero scenario rather than surfacing yaml.v3's raw
// io.EOF, which would read as an I/O fault.
func ParseScenario(data []byte) (*Scenario, error) {
	var s Scenario
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return &s, nil
		}
		return nil, fmt.Errorf("parse scenario: %w", err)
	}
	return &s, nil
}

// DiscoverScenarios finds and parses all scenario YAML files in a directory
func DiscoverScenarios(dir string) ([]*Scenario, error) {
	var scenarios []*Scenario

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Skip multi-step and multi-repo scenarios so single-step discovery stays
		// exclusive. Mirrors DiscoverMultiStepScenarios' "repos:" skip and adds the
		// "steps:" guard so a lifecycle scenario is never also parsed as single-step.
		if strings.Contains(string(data), "\nsteps:") || strings.Contains(string(data), "\nrepos:") {
			return nil
		}

		scenario, err := ParseScenario(data)
		if err != nil {
			return err
		}

		// Store relative path for test naming
		relPath, _ := filepath.Rel(dir, path)
		scenario.Description = relPath + ": " + scenario.Description

		scenarios = append(scenarios, scenario)
		return nil
	})

	return scenarios, err
}

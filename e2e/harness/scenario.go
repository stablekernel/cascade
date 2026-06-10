package harness

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
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

// Config mirrors trunk-config.yaml structure
type Config struct {
	TrunkBranch       string         `yaml:"trunk_branch"`
	Environments      []string       `yaml:"environments"`
	JobTimeoutMinutes int            `yaml:"job_timeout_minutes,omitempty"`
	Builds            []BuildConfig  `yaml:"builds"`
	Deploys           []DeployConfig `yaml:"deploys"`
	Publish           *PublishConfig `yaml:"publish,omitempty"`
	// DispatchInputs carries operator-facing workflow_dispatch inputs through to
	// the generated manifest untouched. A generic map (rather than a typed
	// struct) is used so the harness stays decoupled from the generator's
	// DispatchInput shape while preserving every key (type, options, default,
	// description, required) across the marshal round-trip.
	DispatchInputs map[string]map[string]any `yaml:"dispatch_inputs,omitempty"`
}

// PublishConfig defines a publish callback invoked after a release is published
type PublishConfig struct {
	Workflow string `yaml:"workflow"`
}

// BuildConfig defines a build component
type BuildConfig struct {
	Name              string            `yaml:"name"`
	Workflow          string            `yaml:"workflow,omitempty"`
	Run               string            `yaml:"run,omitempty"`
	Shell             string            `yaml:"shell,omitempty"`
	Triggers          []string          `yaml:"triggers"`
	DependsOn         []string          `yaml:"depends_on"`
	OptionalDependsOn []string          `yaml:"optional_depends_on,omitempty"`
	TimeoutMinutes    int               `yaml:"timeout_minutes,omitempty"`
	RunsOn            any               `yaml:"runs_on,omitempty"`
	Permissions       map[string]string `yaml:"permissions,omitempty"`
	Concurrency       *ConcurrencySpec  `yaml:"concurrency,omitempty"`
}

// DeployConfig defines a deploy component
type DeployConfig struct {
	Name              string            `yaml:"name"`
	Workflow          string            `yaml:"workflow,omitempty"`
	Run               string            `yaml:"run,omitempty"`
	Shell             string            `yaml:"shell,omitempty"`
	Triggers          []string          `yaml:"triggers"`
	DependsOn         []string          `yaml:"depends_on"`
	OptionalDependsOn []string          `yaml:"optional_depends_on,omitempty"`
	TimeoutMinutes    int               `yaml:"timeout_minutes,omitempty"`
	RunsOn            any               `yaml:"runs_on,omitempty"`
	Permissions       map[string]string `yaml:"permissions,omitempty"`
	Concurrency       *ConcurrencySpec  `yaml:"concurrency,omitempty"`
}

// ConcurrencySpec defines the per-callback concurrency block written to trunk-config.yaml.
type ConcurrencySpec struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel_in_progress"`
}

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

// ParseScenario parses YAML bytes into a Scenario
func ParseScenario(data []byte) (*Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
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

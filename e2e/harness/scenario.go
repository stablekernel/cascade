package harness

import (
	"os"
	"path/filepath"
	"strings"

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
	// ReleaseToken carries the release_token field through to the generated
	// manifest. It accepts a full ${{ secrets.* }} expression or a bare secret
	// name; the generator normalizes a bare name to a resolvable expression.
	ReleaseToken string `yaml:"release_token,omitempty"`
	Builds            []BuildConfig  `yaml:"builds"`
	Deploys           []DeployConfig `yaml:"deploys"`
	Publish           *PublishConfig `yaml:"publish,omitempty"`
	// Changelog carries the changelog block (custom workflow, contributors)
	// through to the generated manifest untouched. A generic map keeps the
	// harness decoupled from the generator's ChangelogConfig shape while
	// preserving every key across the marshal round-trip.
	Changelog map[string]any `yaml:"changelog,omitempty"`
	// DispatchInputs carries operator-facing workflow_dispatch inputs through to
	// the generated manifest untouched. A generic map (rather than a typed
	// struct) is used so the harness stays decoupled from the generator's
	// DispatchInput shape while preserving every key (type, options, default,
	// description, required) across the marshal round-trip.
	DispatchInputs map[string]map[string]any `yaml:"dispatch_inputs,omitempty"`
	// EnvironmentConfig carries per-environment settings (gha_environment plus the
	// additive required_reviewers, wait_timer, branch_policy, branch_patterns,
	// tag_patterns, secrets, and variables fields) into the generated manifest so
	// the generator emits the job-level environment: key and the cascade
	// environments command can emit the per-env config. A generic map per env keeps
	// the harness decoupled from the generator's EnvironmentConfig struct while
	// preserving every key across the marshal round-trip, so a scenario can declare
	// any per-env field without a harness change. Keyed by env name.
	EnvironmentConfig map[string]map[string]any `yaml:"environment_config,omitempty"`
	// Validate, ValidateCheck, MergeQueue, PRPreview, Notify, and External carry
	// the optional generator features through to the generated manifest untouched.
	// Each uses a generic map (rather than a typed struct) so the harness stays
	// decoupled from the generator's struct shapes while preserving every key
	// across the marshal round-trip. As the generator gains new keys under any of
	// these blocks, scenarios can exercise them without a harness change.
	Validate      map[string]any   `yaml:"validate,omitempty"`
	ValidateCheck map[string]any   `yaml:"validate_check,omitempty"`
	MergeQueue    map[string]any   `yaml:"merge_queue,omitempty"`
	PRPreview     map[string]any   `yaml:"pr_preview,omitempty"`
	// DriftCheck carries the opt-in drift-check lane (enabled, comment) through to
	// the generated manifest untouched, so a scenario can enable the generated PR
	// drift-check workflow and its fork-safe comment companion (#229).
	DriftCheck map[string]any `yaml:"drift_check,omitempty"`
	Notify        map[string]any   `yaml:"notify,omitempty"`
	External      []map[string]any `yaml:"external,omitempty"`
	// Telemetry carries the reserved vendor-neutral telemetry block (enabled,
	// adapter, webhook, job_summary) through to the generated manifest untouched.
	// A generic map keeps the harness decoupled from the generator's
	// TelemetryConfig shape, so a scenario can declare any reserved telemetry
	// field without the harness needing to know its structure.
	Telemetry map[string]any `yaml:"telemetry,omitempty"`
	// Release carries the release block (disabled, tag, version_overrides)
	// through to the generated manifest untouched. A generic map keeps the
	// harness decoupled from the generator's ReleaseConfig shape, so a scenario
	// can declare any reserved release field without the harness needing to know
	// its structure.
	Release map[string]any `yaml:"release,omitempty"`
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
	// Secrets carries the per-callback secrets union (scalar "inherit", the
	// mapping {inherit: true}, or a per-secret map) through to the generated
	// manifest untouched. A generic value keeps the harness decoupled from the
	// generator's SecretsConfig shape while preserving every accepted form across
	// the marshal round-trip. Omitted entirely when unset so the generator sees no
	// secrets field (the opt-in default emits no secrets block).
	Secrets any `yaml:"secrets,omitempty"`
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
	SupportsDryRun    bool              `yaml:"supports_dry_run,omitempty"`
	RunsOn            any               `yaml:"runs_on,omitempty"`
	Permissions       map[string]string `yaml:"permissions,omitempty"`
	Concurrency       *ConcurrencySpec  `yaml:"concurrency,omitempty"`
	// Secrets carries the per-callback secrets union through to the generated
	// manifest untouched. See BuildConfig.Secrets for the accepted forms and the
	// rationale for the generic value type.
	Secrets any `yaml:"secrets,omitempty"`
	// Rollout carries the rollout sub-block (type, canary, blue_green) through to
	// the generated manifest untouched. A generic map keeps the harness decoupled
	// from the generator's RolloutConfig shape, so a scenario can declare any
	// reserved rollout field without the harness needing to know its structure.
	Rollout map[string]any `yaml:"rollout,omitempty"`
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

package harness

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MultiStepScenario represents a full lifecycle E2E test with multiple steps
type MultiStepScenario struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Config      Config      `yaml:"config"`
	Setup       *SetupState `yaml:"setup,omitempty"` // Optional initial state
	Steps       []Step      `yaml:"steps"`
}

// SetupState defines optional initial state for the scenario
type SetupState struct {
	State    map[string]*EnvStateSetup `yaml:"state,omitempty"`
	Tags     []string                  `yaml:"tags,omitempty"`
	Releases []ReleaseSetup            `yaml:"releases,omitempty"`
}

// EnvStateSetup defines initial environment state
type EnvStateSetup struct {
	SHA     string `yaml:"sha,omitempty"`
	Version string `yaml:"version,omitempty"`
}

// Step represents a single action in the scenario
type Step struct {
	Name    string       `yaml:"name"`
	Action  string       `yaml:"action"` // commit, orchestrate, promote
	Commit  *CommitStep  `yaml:"commit,omitempty"`
	Promote *PromoteStep `yaml:"promote,omitempty"`
	Expect  *StepExpect  `yaml:"expect,omitempty"`
}

// CommitStep defines a commit action
type CommitStep struct {
	Message string            `yaml:"message"`
	Files   map[string]string `yaml:"files"`
}

// PromoteStep defines a promote action
type PromoteStep struct {
	Mode          string `yaml:"mode"`             // default, cascade
	Target        string `yaml:"target,omitempty"` // for cascade: dev-to-prod
	AllowBreaking bool   `yaml:"allow_breaking,omitempty"`
	ExpectFailure bool   `yaml:"expect_failure,omitempty"`
}

// StepExpect defines expected outcomes for a step
type StepExpect struct {
	State         map[string]*StateExpect `yaml:"state,omitempty"`
	Jobs          map[string]string       `yaml:"jobs,omitempty"` // job name -> success/skipped/failure
	Releases      []ReleaseExpectStep     `yaml:"releases,omitempty"`
	Tags          *TagsExpect             `yaml:"tags,omitempty"`
	Preflight     *PreflightExpect        `yaml:"preflight,omitempty"`
	WorkflowFiles []WorkflowFileExpect    `yaml:"workflow_files,omitempty"` // Generated workflow file content checks
}

// WorkflowFileExpect asserts a generated workflow file contains/excludes
// specific substrings. Verifies manifest fields make it into the emitted
// YAML, orthogonal to behavior checks (state/jobs/etc.) which observe the
// run outcome. Used for features whose effect is purely the generated
// workflow shape (#92 concurrency, #97 timeout-minutes, #101/#102 push
// retry loops).
type WorkflowFileExpect struct {
	Path        string   `yaml:"path"`                   // Path inside the test repo (e.g., ".github/workflows/orchestrate.yaml")
	Contains    []string `yaml:"contains,omitempty"`     // Substrings that must appear
	NotContains []string `yaml:"not_contains,omitempty"` // Substrings that must NOT appear
}

// StateExpect defines expected state for an environment
type StateExpect struct {
	SHA       string                   `yaml:"sha,omitempty"` // Can be "commit1", "commit2", etc.
	Version   string                   `yaml:"version,omitempty"`
	Wiped     bool                     `yaml:"wiped,omitempty"`     // State should not exist
	Unchanged bool                     `yaml:"unchanged,omitempty"` // State should match previous
	Deploys   map[string]*DeployExpect `yaml:"deploys,omitempty"`
}

// DeployExpect defines expected deploy state
type DeployExpect struct {
	SHA string `yaml:"sha,omitempty"`
}

// ReleaseExpectStep defines expected release state
type ReleaseExpectStep struct {
	Tag        string   `yaml:"tag"`
	Prerelease bool     `yaml:"prerelease,omitempty"`
	Draft      bool     `yaml:"draft,omitempty"`
	Latest     bool     `yaml:"latest,omitempty"`
	Deleted    bool     `yaml:"deleted,omitempty"`   // Tag should be deleted
	Changelog  []string `yaml:"changelog,omitempty"` // Commits that should appear
}

// TagsExpect defines expected tag state
type TagsExpect struct {
	Exist   []string `yaml:"exist,omitempty"`
	Deleted []string `yaml:"deleted,omitempty"`
}

// PreflightExpect defines expected preflight outputs
type PreflightExpect struct {
	HasBreaking bool   `yaml:"has_breaking,omitempty"`
	CanProceed  bool   `yaml:"can_proceed,omitempty"`
	SourceEnv   string `yaml:"source_env,omitempty"`
	TargetEnv   string `yaml:"target_env,omitempty"`
}

// ParseMultiStepScenario parses YAML bytes into a MultiStepScenario
func ParseMultiStepScenario(data []byte) (*MultiStepScenario, error) {
	var s MultiStepScenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// DiscoverMultiStepScenarios finds and parses all multi-step scenario YAML files
func DiscoverMultiStepScenarios(dir string) ([]*MultiStepScenario, error) {
	var scenarios []*MultiStepScenario

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

		// Skip multi-repo scenarios (they use "repos:" instead of "config:")
		if strings.Contains(string(data), "\nrepos:") {
			return nil
		}

		scenario, err := ParseMultiStepScenario(data)
		if err != nil {
			return err
		}

		// Store relative path for test naming
		relPath, _ := filepath.Rel(dir, path)
		if scenario.Description != "" {
			scenario.Description = relPath + ": " + scenario.Description
		} else {
			scenario.Description = relPath
		}

		scenarios = append(scenarios, scenario)
		return nil
	})

	return scenarios, err
}

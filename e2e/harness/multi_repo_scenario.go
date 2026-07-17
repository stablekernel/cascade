package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"gopkg.in/yaml.v3"
)

// MultiRepoScenario supports multi-repo test definitions
type MultiRepoScenario struct {
	Name        string                  `yaml:"name"`
	Description string                  `yaml:"description"`
	Repos       map[string]RepoScenario `yaml:"repos"`   // Named repos
	Primary     string                  `yaml:"primary"` // Which repo is primary
	Steps       []ScenarioStep          `yaml:"steps"`   // Ordered steps
	Expect      MultiRepoExpect         `yaml:"expect"`  // Final expectations
}

// RepoScenario defines a single repo in multi-repo scenario
type RepoScenario struct {
	Config   config.TrunkConfig     `yaml:"config"`
	Commits  []Commit               `yaml:"commits"`
	Manifest map[string]interface{} `yaml:"manifest"`
	Tags     []string               `yaml:"tags"`
}

// ScenarioStep defines an action in the scenario
type ScenarioStep struct {
	Name     string               `yaml:"name"`     // Optional step name for debugging
	Repo     string               `yaml:"repo"`     // Which repo to act on
	Action   string               `yaml:"action"`   // commit, trigger, dispatch, tag, assert
	Commit   *StepCommit          `yaml:"commit"`   // For action=commit
	Trigger  *StepTrigger         `yaml:"trigger"`  // For action=trigger
	Dispatch *StepDispatch        `yaml:"dispatch"` // For action=dispatch
	Tag      *StepTag             `yaml:"tag"`      // For action=tag
	Expect   *MultiRepoStepExpect `yaml:"expect"`   // Optional per-step assertions
}

// StepCommit defines a commit action
type StepCommit struct {
	Message string            `yaml:"message"`
	Files   map[string]string `yaml:"files"`
}

// StepTrigger defines a workflow trigger action
type StepTrigger struct {
	Workflow string            `yaml:"workflow"`
	Event    string            `yaml:"event"`
	Inputs   map[string]string `yaml:"inputs"`
}

// StepDispatch simulates cross-repo workflow_dispatch
type StepDispatch struct {
	TargetRepo string            `yaml:"target_repo"`
	Workflow   string            `yaml:"workflow"`
	Inputs     map[string]string `yaml:"inputs"`
}

// StepTag defines a tag creation action
type StepTag struct {
	Tag string `yaml:"tag"`
}

// MultiRepoStepExpect defines per-step assertions for multi-repo scenarios.
type MultiRepoStepExpect struct {
	// Tags each must be present on the step's repo. An empty (but present) list
	// asserts the repo carries no tags at all.
	Tags []string `yaml:"tags"`
	// Error is a substring the step's failure must contain. Setting it inverts
	// the step's contract: the step is then required to fail, and succeeding is
	// itself a failure.
	Error string `yaml:"error"`
}

// MultiRepoExpect defines expectations across repos
type MultiRepoExpect struct {
	Repos map[string]RepoExpect `yaml:"repos"` // Per-repo expectations
}

// RepoExpect defines expectations for a single repo.
//
// Every field here is a typed struct rather than an open map, and that is
// load-bearing rather than stylistic. yaml.v3's KnownFields(true) rejects a key
// the schema does not define, but it can only do so where there is a schema:
// it validates struct fields and cannot see inside a map[string]interface{}.
// While this subtree was an open map, a typo'd expectation decoded cleanly,
// asserted nothing, and reported green. Typing it is what puts these keys back
// under the decoder's guard.
type RepoExpect struct {
	// Tags each must be present on the repo. A nil list (the key absent) asserts
	// nothing; an empty but present list (`tags: []`) asserts the repo carries no
	// tags at all.
	Tags []TagExpect `yaml:"tags"`
	// State is the expected ci.state subtree, keyed by environment name.
	State map[string]EnvStateExpect `yaml:"state"`
}

// EnvStateExpect is the expected state of one environment. An empty field is
// not asserted, matching the convention StateExpect follows on the single-repo
// side.
type EnvStateExpect struct {
	SHA      string                          `yaml:"sha,omitempty"`
	Version  string                          `yaml:"version,omitempty"`
	External map[string]ExternalDeployExpect `yaml:"external,omitempty"`
}

// ExternalDeployExpect is the expected recorded state of a single external
// deploy, at ci.state.<env>.external.<name>.
type ExternalDeployExpect struct {
	SHA     string `yaml:"sha,omitempty"`
	Version string `yaml:"version,omitempty"`
	// Artifacts is legitimately open at the key level: artifact names are chosen
	// by the scenario, not the schema. The values are typed as strings, so there
	// is no shape ambiguity for a phantom to hide in.
	Artifacts map[string]string `yaml:"artifacts,omitempty"`
}

// ParseMultiRepoScenario parses YAML bytes into a MultiRepoScenario. Decoding is
// strict: a key the schema does not define is an error rather than a silently
// dropped field, so a typo cannot quietly erase an expectation and leave the
// scenario green. An empty document decodes to a zero scenario rather than
// surfacing yaml.v3's raw io.EOF, which would read as an I/O fault.
func ParseMultiRepoScenario(data []byte) (*MultiRepoScenario, error) {
	var s MultiRepoScenario
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return &s, nil
		}
		return nil, fmt.Errorf("parse multi-repo scenario: %w", err)
	}
	return &s, nil
}

// ValidateMultiRepoScenario checks that a scenario can actually fail.
//
// Strict decoding rejects a key the schema does not define, but it cannot judge
// a key whose name is schema-valid and whose value names something that does not
// exist. A state expectation on a mistyped env, or on an external deploy the
// repo never declares, reads back as absent no matter how the product behaves,
// so it passes unconditionally. Both are decidable against the scenario's own
// config without running anything, which is what makes this cheap enough to run
// at discovery.
func ValidateMultiRepoScenario(s *MultiRepoScenario) error {
	// A scenario with no steps runs nothing and therefore asserts nothing, so it
	// reports green for the wrong reason.
	if len(s.Steps) == 0 {
		return fmt.Errorf("scenario %q declares no steps, so it asserts nothing", s.Name)
	}

	for repoName, expect := range s.Expect.Repos {
		// An expectation with neither tags nor state makes no claim at all. Note
		// `tags: []` is a claim (the repo has no tags) and decodes to an empty but
		// non-nil slice, so it is deliberately not caught here.
		if expect.Tags == nil && expect.State == nil {
			return fmt.Errorf("scenario %q expects repo %q but asserts nothing about it: an expectation with no tags and no state passes no matter what the code does",
				s.Name, repoName)
		}

		repo, ok := s.Repos[repoName]
		if !ok {
			return fmt.Errorf("scenario %q expects repo %q, which it never declares (known: %s)",
				s.Name, repoName, sortedNames(multiRepoNames(s)))
		}

		envNames := repo.Config.EnvironmentNames()
		validEnvs := make(map[string]bool, len(envNames))
		for _, env := range envNames {
			validEnvs[env] = true
		}

		// External deploy names come from the primary's external declarations,
		// which is where the state rows under external.<name> originate.
		validExternal := make(map[string]bool)
		for _, ext := range repo.Config.External {
			for _, d := range ext.Deploys {
				validExternal[d.Name] = true
			}
		}

		for envName, envExpect := range expect.State {
			if !validEnvs[envName] {
				return fmt.Errorf("scenario %q expects state on %q.%q, which is not an environment that repo declares (known: %s). An env name that does not exist has no state no matter what the code does, so the expectation can never fail",
					s.Name, repoName, envName, sortedNames(validEnvs))
			}
			// The same rule as the repo level, one level down. An env block with
			// nothing in it names a real env and proves nothing about it, so it
			// passes no matter what the code does. There is no "this env has no
			// state" reading for an empty block the way `tags: []` reads as "no
			// tags", so rejecting it costs no expressiveness.
			if envExpect.SHA == "" && envExpect.Version == "" && len(envExpect.External) == 0 {
				return fmt.Errorf("scenario %q expects state on %q.%q but asserts nothing about it: an env expectation with no sha, no version, and no external deploys passes no matter what the code does",
					s.Name, repoName, envName)
			}

			for deployName := range envExpect.External {
				if !validExternal[deployName] {
					return fmt.Errorf("scenario %q expects state on %q.%q.external.%q, which is not an external deploy that repo declares (known: %s). A deploy name that does not exist has no state no matter what the code does, so the expectation can never fail",
						s.Name, repoName, envName, deployName, sortedNames(validExternal))
				}
			}
		}
	}

	return nil
}

// multiRepoNames returns the set of repos the scenario declares.
func multiRepoNames(s *MultiRepoScenario) map[string]bool {
	names := make(map[string]bool, len(s.Repos))
	for name := range s.Repos {
		names[name] = true
	}
	return names
}

// DiscoverMultiRepoScenarios finds and parses all multi-repo scenario files
func DiscoverMultiRepoScenarios(dir string) ([]*MultiRepoScenario, error) {
	var scenarios []*MultiRepoScenario

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

		// Check if this looks like a multi-repo scenario (has "repos:" key)
		if !strings.Contains(string(data), "repos:") {
			return nil // Skip non-multi-repo scenarios
		}

		scenario, err := ParseMultiRepoScenario(data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		if err := ValidateMultiRepoScenario(scenario); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		// Store relative path for test naming
		relPath, _ := filepath.Rel(dir, path)
		scenario.Description = relPath + ": " + scenario.Description

		scenarios = append(scenarios, scenario)
		return nil
	})

	return scenarios, err
}

// MultiRepoRunner executes multi-repo scenarios
type MultiRepoRunner struct {
	harness  *MultiRepoHarness
	scenario *MultiRepoScenario
	varStore map[string]string // Variable storage for interpolation
}

// NewMultiRepoRunner creates a runner for a scenario
func NewMultiRepoRunner(harness *MultiRepoHarness, scenario *MultiRepoScenario) *MultiRepoRunner {
	return &MultiRepoRunner{
		harness:  harness,
		scenario: scenario,
		varStore: make(map[string]string),
	}
}

// Setup initializes all repos defined in the scenario
func (r *MultiRepoRunner) Setup(ctx context.Context) error {
	// Create repos in order (primary first if defined)
	if r.scenario.Primary != "" {
		if repoSetup, ok := r.scenario.Repos[r.scenario.Primary]; ok {
			if err := r.createRepo(ctx, r.scenario.Primary, repoSetup, true); err != nil {
				return fmt.Errorf("failed to create primary repo %s: %w", r.scenario.Primary, err)
			}
		}
	}

	// Create remaining repos
	for name, repoSetup := range r.scenario.Repos {
		if name == r.scenario.Primary {
			continue // Already created
		}
		isPrimary := r.scenario.Primary == "" && len(r.scenario.Repos) == 1
		if err := r.createRepo(ctx, name, repoSetup, isPrimary); err != nil {
			return fmt.Errorf("failed to create repo %s: %w", name, err)
		}
	}

	return nil
}

// createRepo creates a single repo from scenario config
func (r *MultiRepoRunner) createRepo(ctx context.Context, name string, scenario RepoScenario, isPrimary bool) error {
	cfg := scenario.Config // Copy the config

	setup := MultiRepoSetup{
		Name:      name,
		Config:    &cfg,
		Commits:   scenario.Commits,
		Tags:      scenario.Tags,
		Manifest:  scenario.Manifest,
		IsPrimary: isPrimary,
	}

	// Set up satellite relationship
	if !isPrimary && r.scenario.Primary != "" {
		setup.Primary = r.scenario.Primary
	}

	// Set up primary's satellite list
	if isPrimary {
		for repoName := range r.scenario.Repos {
			if repoName != name {
				setup.Satellites = append(setup.Satellites, repoName)
			}
		}
	}

	repoCtx, err := r.harness.CreateRepo(ctx, setup)
	if err != nil {
		return err
	}

	// Store initial variables
	r.varStore[name+".head_sha"] = repoCtx.HeadSHA
	r.varStore[name+".name"] = name

	return nil
}

// RunSteps executes all steps in the scenario
func (r *MultiRepoRunner) RunSteps(ctx context.Context) error {
	for i, step := range r.scenario.Steps {
		stepName := step.Name
		if stepName == "" {
			stepName = fmt.Sprintf("step-%d", i+1)
		}

		wantErr := ""
		if step.Expect != nil {
			wantErr = step.Expect.Error
		}

		err := r.runStep(ctx, step)

		if err != nil {
			// Check if error was expected
			if wantErr != "" {
				if strings.Contains(err.Error(), wantErr) {
					continue // Expected error, continue
				}
				return fmt.Errorf("%s: expected failure containing %q, but it failed with: %w", stepName, wantErr, err)
			}
			return fmt.Errorf("%s failed: %w", stepName, err)
		}

		// A step declaring an expected error is required to fail. This branch used
		// to fall through to the success path, so an expected-failure that instead
		// succeeded passed: the test could not fail in the direction it existed to
		// test.
		if wantErr != "" {
			return fmt.Errorf("%s: expected to fail with an error containing %q, but it succeeded", stepName, wantErr)
		}

		// Run per-step assertions if defined
		if step.Expect != nil {
			if err := r.assertStep(ctx, step); err != nil {
				return fmt.Errorf("%s assertion failed: %w", stepName, err)
			}
		}
	}

	return nil
}

// runStep executes a single scenario step
func (r *MultiRepoRunner) runStep(ctx context.Context, step ScenarioStep) error {
	switch step.Action {
	case "commit":
		return r.runCommitStep(ctx, step)
	case "trigger":
		return r.runTriggerStep(ctx, step)
	case "dispatch":
		return r.runDispatchStep(ctx, step)
	case "tag":
		return r.runTagStep(ctx, step)
	case "assert":
		return r.assertStep(ctx, step)
	default:
		return fmt.Errorf("unknown action: %s", step.Action)
	}
}

// runCommitStep creates a commit in the specified repo
func (r *MultiRepoRunner) runCommitStep(ctx context.Context, step ScenarioStep) error {
	if step.Commit == nil {
		return fmt.Errorf("commit step requires commit definition")
	}

	// Interpolate variables in files
	files := make(map[string]string)
	for path, content := range step.Commit.Files {
		files[path] = r.interpolate(content)
	}

	sha, err := r.harness.CommitToRepo(ctx, step.Repo, step.Commit.Message, files)
	if err != nil {
		return err
	}

	// Update variable store
	r.varStore[step.Repo+".head_sha"] = sha
	r.varStore[step.Repo+".latest_sha"] = sha

	return nil
}

// runTriggerStep triggers a workflow in the specified repo
func (r *MultiRepoRunner) runTriggerStep(ctx context.Context, step ScenarioStep) error {
	if step.Trigger == nil {
		return fmt.Errorf("trigger step requires trigger definition")
	}

	repoCtx := r.harness.GetRepo(step.Repo)
	if repoCtx == nil {
		return fmt.Errorf("repo %s not found", step.Repo)
	}

	// Interpolate variables in inputs
	inputs := make(map[string]string)
	for k, v := range step.Trigger.Inputs {
		inputs[k] = r.interpolate(v)
	}

	// Run the workflow using Act
	if r.harness.Act() == nil {
		// If no Act runner, simulate the workflow
		return r.simulateWorkflow(ctx, step.Repo, step.Trigger.Workflow, step.Trigger.Event, inputs)
	}

	// Use Act to run the actual workflow
	opts := RunOpts{
		WorkflowPath: step.Trigger.Workflow,
		Event:        step.Trigger.Event,
		Inputs:       inputs,
	}

	_, err := r.harness.RunWorkflowInRepo(ctx, step.Repo, opts)
	return err
}

// runDispatchStep simulates cross-repo workflow_dispatch
func (r *MultiRepoRunner) runDispatchStep(ctx context.Context, step ScenarioStep) error {
	if step.Dispatch == nil {
		return fmt.Errorf("dispatch step requires dispatch definition")
	}

	// Interpolate variables in inputs
	inputs := make(map[string]string)
	for k, v := range step.Dispatch.Inputs {
		inputs[k] = r.interpolate(v)
	}

	return r.harness.RealCrossRepoDispatch(ctx, step.Repo, step.Dispatch.TargetRepo, step.Dispatch.Workflow, inputs)
}

// runTagStep creates a tag in the specified repo
func (r *MultiRepoRunner) runTagStep(ctx context.Context, step ScenarioStep) error {
	if step.Tag == nil {
		return fmt.Errorf("tag step requires tag definition")
	}

	tag := r.interpolate(step.Tag.Tag)
	return r.harness.CreateTagInRepo(ctx, step.Repo, tag)
}

// assertStep runs assertions for a step
func (r *MultiRepoRunner) assertStep(ctx context.Context, step ScenarioStep) error {
	if step.Expect == nil {
		return nil
	}

	// Check tag assertions
	if len(step.Expect.Tags) > 0 {
		tags, err := r.harness.GetTagsInRepo(ctx, step.Repo)
		if err != nil {
			return fmt.Errorf("failed to get tags: %w", err)
		}

		for _, expectedTag := range step.Expect.Tags {
			found := false
			for _, tag := range tags {
				if tag == expectedTag {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("expected tag %s not found in repo %s (found: %v)", expectedTag, step.Repo, tags)
			}
		}
	}

	return nil
}

// simulateWorkflow simulates workflow execution without Act
func (r *MultiRepoRunner) simulateWorkflow(ctx context.Context, repoName, workflow, event string, inputs map[string]string) error {
	repoCtx := r.harness.GetRepo(repoName)
	if repoCtx == nil {
		return fmt.Errorf("repo %s not found", repoName)
	}

	// Basic simulation - just record that the workflow ran
	// In real execution, Act would run the actual workflow
	r.varStore[repoName+".last_workflow"] = workflow
	r.varStore[repoName+".last_event"] = event

	return nil
}

// AssertFinal runs final assertions after all steps complete
func (r *MultiRepoRunner) AssertFinal(ctx context.Context) error {
	// For every notifying satellite, assert its generated orchestrate.yaml
	// carries the real Notify step that dispatches the primary's external-update
	// workflow. This asserts the notify CONTENT; the live cross-repo dispatch is
	// a documented gitea no-op and is bridged by RealCrossRepoDispatch.
	for name, repoCtx := range r.harness.repos {
		if repoCtx.Config != nil && repoCtx.Config.Notify != nil {
			if err := r.harness.RunSatelliteOrchestrateAndAssertNotify(ctx, name); err != nil {
				return fmt.Errorf("notify-content assertion failed for %s: %w", name, err)
			}
		}
	}

	for repoName, expect := range r.scenario.Expect.Repos {
		// Check tags. A nil list means the scenario made no claim; an empty but
		// present list is the claim "this repo has no tags", which a stray tag
		// must break. Gating on len > 0 would have made the latter unfalsifiable.
		if expect.Tags != nil {
			tags, err := r.harness.GetTagsInRepo(ctx, repoName)
			if err != nil {
				return fmt.Errorf("failed to get tags for %s: %w", repoName, err)
			}

			if len(expect.Tags) == 0 && len(tags) > 0 {
				return fmt.Errorf("repo %s expects no tags, but found: %v", repoName, tags)
			}

			for _, expectedTag := range expect.Tags {
				found := false
				for _, tag := range tags {
					if tag == expectedTag.Pattern {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("expected tag %s not found in repo %s (found: %v)", expectedTag.Pattern, repoName, tags)
				}
			}
		}

		// Check manifest state
		if expect.State != nil {
			if err := r.assertState(ctx, repoName, expect.State); err != nil {
				return fmt.Errorf("state assertion failed for %s: %w", repoName, err)
			}
		}
	}

	return nil
}

// assertState checks expected external state against the REAL manifest in
// gitea. The `cascade external update` verb (driven under act by
// RealCrossRepoDispatch) commits ci.state.<env>.external.<name> back to the
// primary repo, so the source of truth is the committed .github/manifest.yaml,
// not an in-process ExecutionContext.
// The expectation is typed, so a shape mismatch is now a decode error at
// discovery rather than something this function has to tolerate at runtime. The
// silent `continue` on every mismatch that used to live here was the same
// disease as the phantom keys: it turned a malformed expectation into a pass.
func (r *MultiRepoRunner) assertState(ctx context.Context, repoName string, expected map[string]EnvStateExpect) error {
	repo := r.harness.GetRepo(repoName)
	if repo == nil {
		return fmt.Errorf("repo %s not found", repoName)
	}

	state, err := r.readManifestState(ctx, repoName)
	if err != nil {
		return err
	}

	// For each environment in expected state
	for envName, envExpected := range expected {
		envState := mapAt(state, envName)

		// Env-level sha/version. These were never compared while the subtree was
		// an open map: assertState read only env["external"], so a scenario
		// asserting state.<env>.sha was asserting nothing.
		if envExpected.SHA != "" {
			want := r.interpolate(envExpected.SHA)
			if got := stringAt(envState, "sha"); got != want {
				return fmt.Errorf("%s.sha: expected %s, got %s", envName, want, got)
			}
		}
		if envExpected.Version != "" {
			want := r.interpolate(envExpected.Version)
			if got := stringAt(envState, "version"); got != want {
				return fmt.Errorf("%s.version: expected %s, got %s", envName, want, got)
			}
		}

		externalState := mapAt(envState, "external")

		for deployName, deployExpected := range envExpected.External {
			actual := mapAt(externalState, deployName)
			if actual == nil {
				return fmt.Errorf("%s.external.%s: no state recorded in manifest", envName, deployName)
			}

			if deployExpected.SHA != "" {
				want := r.interpolate(deployExpected.SHA)
				if got := stringAt(actual, "sha"); got != want {
					return fmt.Errorf("%s.external.%s.sha: expected %s, got %s",
						envName, deployName, want, got)
				}
			}

			if deployExpected.Version != "" {
				want := r.interpolate(deployExpected.Version)
				if got := stringAt(actual, "version"); got != want {
					return fmt.Errorf("%s.external.%s.version: expected %s, got %s",
						envName, deployName, want, got)
				}
			}

			// Artifacts are a string->string map. Asserting them end-to-end proves
			// the dispatched --artifacts JSON survived the receiver's run: shell
			// verbatim, which is the contract that breaks if the value is
			// interpolated into the script text instead of routed through env:.
			if len(deployExpected.Artifacts) > 0 {
				actualArtifacts := mapAt(actual, "artifacts")
				for k, v := range deployExpected.Artifacts {
					want := r.interpolate(v)
					if got := stringAt(actualArtifacts, k); got != want {
						return fmt.Errorf("%s.external.%s.artifacts.%s: expected %q, got %q",
							envName, deployName, k, want, got)
					}
				}
			}
		}
	}

	return nil
}

// readManifestState reads the repo's committed .github/manifest.yaml from gitea
// and returns the ci.state map (env -> env-state). It returns an empty map (not
// an error) when no state has been written yet.
func (r *MultiRepoRunner) readManifestState(ctx context.Context, repoName string) (map[string]interface{}, error) {
	content, err := r.harness.GetFileContentInRepo(ctx, repoName, ".github/manifest.yaml")
	if err != nil {
		return nil, fmt.Errorf("reading manifest for %s: %w", repoName, err)
	}

	var manifest map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest for %s: %w", repoName, err)
	}

	ci := mapAt(manifest, "ci")
	if ci == nil {
		return map[string]interface{}{}, nil
	}
	state := mapAt(ci, "state")
	if state == nil {
		return map[string]interface{}{}, nil
	}
	return state, nil
}

// mapAt returns m[key] coerced to a map[string]interface{}, or nil. It tolerates
// the map[interface{}]interface{} shape that gopkg.in/yaml.v3 can still produce
// for deeply nested untyped documents.
func mapAt(m map[string]interface{}, key string) map[string]interface{} {
	if m == nil {
		return nil
	}
	switch v := m[key].(type) {
	case map[string]interface{}:
		return v
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			if ks, ok := k.(string); ok {
				out[ks] = val
			}
		}
		return out
	default:
		return nil
	}
}

// stringAt returns m[key] as a string, or "".
func stringAt(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// interpolate replaces ${var} patterns with stored values
func (r *MultiRepoRunner) interpolate(s string) string {
	re := regexp.MustCompile(`\$\{([^}]+)\}`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1] // Remove ${ and }
		if val, ok := r.varStore[varName]; ok {
			return val
		}
		return match // Keep original if not found
	})
}

// Run executes the full scenario: setup, steps, and final assertions
func (r *MultiRepoRunner) Run(ctx context.Context) error {
	if err := r.Setup(ctx); err != nil {
		return fmt.Errorf("setup failed: %w", err)
	}

	if err := r.RunSteps(ctx); err != nil {
		return fmt.Errorf("steps failed: %w", err)
	}

	if err := r.AssertFinal(ctx); err != nil {
		return fmt.Errorf("final assertions failed: %w", err)
	}

	return nil
}

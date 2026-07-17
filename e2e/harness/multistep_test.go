package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMultiStepScenario(t *testing.T) {
	yaml := `
name: "Two Environment Happy Path"
description: "Full lifecycle for dev → release → prod"

config:
  environments: [dev, prod]
  builds:
    - name: app
      triggers: ["src/**"]
    - name: worker
      triggers: ["workers/**"]
  deploys:
    - name: cdk
      triggers: ["cdk/**"]

steps:
  - name: "Initial feature commit"
    action: commit
    commit:
      message: "feat: add app feature"
      files:
        src/app.ts: "// app code"

  - name: "Orchestrate dev"
    action: orchestrate
    expect:
      state:
        dev:
          version: "v0.1.0-rc.0"
      jobs:
        build-app: success
        build-worker: skipped
        deploy-cdk: skipped
      releases:
        - tag: "v0.1.0-rc.0"
          prerelease: true
          draft: true
`

	scenario, err := ParseMultiStepScenario([]byte(yaml))
	require.NoError(t, err)

	assert.Equal(t, "Two Environment Happy Path", scenario.Name)
	assert.Equal(t, []string{"dev", "prod"}, scenario.Config.EnvironmentNames())
	assert.Len(t, scenario.Steps, 2)

	// First step is a commit
	assert.Equal(t, "commit", scenario.Steps[0].Action)
	assert.Equal(t, "feat: add app feature", scenario.Steps[0].Commit.Message)

	// Second step is orchestrate with expectations
	assert.Equal(t, "orchestrate", scenario.Steps[1].Action)
	assert.Equal(t, "v0.1.0-rc.0", scenario.Steps[1].Expect.State["dev"].Version)
	assert.Equal(t, "success", scenario.Steps[1].Expect.Jobs["build-app"])
	assert.Equal(t, "skipped", scenario.Steps[1].Expect.Jobs["build-worker"])
}

// TestParseMultiStepScenario_RuntimeAssertionFields round-trips the runtime
// de-vacuum surfaces (expect_log on a step expectation; event and expect_no_run
// on an orchestrate step) so a scenario that asserts a runtime log marker or a
// suppressed trigger parses into the fields the runner consumes.
func TestParseMultiStepScenario_RuntimeAssertionFields(t *testing.T) {
	yaml := `
name: "Runtime assertion fields"
config:
  environments: [dev]
steps:
  - name: "Orchestrate on push; assert state-write marker"
    action: orchestrate
    expect:
      state:
        dev:
          version: "v0.1.0-rc.0"
      expect_log: "cascade-state-write: ok attempt=1"
  - name: "Push does not trigger a dispatch-only orchestrate"
    action: orchestrate
    orchestrate:
      event: push
      expect_no_run: true
  - name: "Dispatch triggers the orchestrate"
    action: orchestrate
    orchestrate:
      event: workflow_dispatch
    expect:
      state:
        dev:
          version: "v0.1.0-rc.0"
`

	scenario, err := ParseMultiStepScenario([]byte(yaml))
	require.NoError(t, err)
	require.Len(t, scenario.Steps, 3)

	assert.Equal(t, "cascade-state-write: ok attempt=1", scenario.Steps[0].Expect.ExpectLog)

	require.NotNil(t, scenario.Steps[1].Orchestrate)
	assert.Equal(t, "push", scenario.Steps[1].Orchestrate.Event)
	assert.True(t, scenario.Steps[1].Orchestrate.ExpectNoRun)

	require.NotNil(t, scenario.Steps[2].Orchestrate)
	assert.Equal(t, "workflow_dispatch", scenario.Steps[2].Orchestrate.Event)
	assert.False(t, scenario.Steps[2].Orchestrate.ExpectNoRun)
}

// TestParseMultiStepScenario_UnknownKeyIsError proves an unrecognized scenario
// key is a hard parse error rather than silently dropped field. A typo'd or
// stale key used to decode into nothing, leaving a scenario that ran fewer
// assertions than its author wrote and still reported green.
func TestParseMultiStepScenario_UnknownKeyIsError(t *testing.T) {
	yaml := `
name: "Typo'd key"
config:
  environments: [dev]
stepz:
  - name: "Never runs because the key is misspelled"
    action: orchestrate
`

	_, err := ParseMultiStepScenario([]byte(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stepz")
}

// TestParseMultiStepScenario_EmptyDocumentIsNotEOF proves an empty scenario file
// decodes into an empty scenario rather than surfacing yaml.v3's raw io.EOF,
// which reads as an I/O fault instead of an empty-scenario problem. The steps
// check in DiscoverMultiStepScenarios is what rejects it, with the file path.
func TestParseMultiStepScenario_EmptyDocumentIsNotEOF(t *testing.T) {
	scenario, err := ParseMultiStepScenario([]byte("\n"))
	require.NoError(t, err)
	assert.Empty(t, scenario.Steps)
}

// TestDiscoverMultiStepScenarios_NoStepsIsError proves a scenario that declares
// no steps is rejected, naming the offending file. A step-less scenario runs
// nothing and asserts nothing, so it passes for the wrong reason. Strict
// decoding alone does not catch it: the steps can go missing without any
// unknown key being present.
func TestDiscoverMultiStepScenarios_NoStepsIsError(t *testing.T) {
	dir := t.TempDir()
	body := `
name: "Asserts nothing"
description: "Declares no steps"
config:
  environments: [dev]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "no-steps.yaml"), []byte(body), 0644))

	_, err := DiscoverMultiStepScenarios(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-steps.yaml")
	assert.Contains(t, err.Error(), "no steps")
}

// TestDiscoverMultiStepScenarios_UnknownStateEnvIsError proves a state
// expectation naming an env the scenario never declares is rejected at
// discovery. This is what keeps `unchanged` honest. A typo'd env is absent no
// matter how the code behaves, so `unchanged: true` on it compares nothing
// against nothing and can never go red. Nothing at runtime can tell that apart
// from a legitimate absence, so the name is checked against the config instead.
func TestDiscoverMultiStepScenarios_UnknownStateEnvIsError(t *testing.T) {
	dir := t.TempDir()
	body := `
name: "Typo'd env"
description: "Asserts unchanged against an env that does not exist"
config:
  environments: [dev, prod]
steps:
  - name: "Deploy dev"
    action: commit
    commit:
      message: "test"
      files:
        test.txt: "content"
    expect:
      state:
        prodd:
          unchanged: true
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "typo-env.yaml"), []byte(body), 0644))

	_, err := DiscoverMultiStepScenarios(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "typo-env.yaml")
	assert.Contains(t, err.Error(), "prodd")
	assert.Contains(t, err.Error(), "dev")
	assert.Contains(t, err.Error(), "prod")
}

// TestDiscoverMultiStepScenarios_AcceptsDeclaredPseudoAndComponentEnvs proves
// the env-name check accepts every name a scenario can legitimately assert on:
// a top-level env, the release/prerelease pseudo-envs the runner records from
// ci.latest_release, and an env declared only inside a component.
func TestDiscoverMultiStepScenarios_AcceptsDeclaredPseudoAndComponentEnvs(t *testing.T) {
	dir := t.TempDir()
	body := `
name: "Declared envs"
description: "Asserts on top-level, pseudo, and component-scoped envs"
config:
  environments: [dev]
  components:
    api:
      path: api
      environments: [staging]
steps:
  - name: "Deploy dev"
    action: commit
    commit:
      message: "test"
      files:
        test.txt: "content"
    expect:
      state:
        dev:
          unchanged: true
        release:
          unchanged: true
        prerelease:
          unchanged: true
        staging:
          component: api
          unchanged: true
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "declared.yaml"), []byte(body), 0644))

	scenarios, err := DiscoverMultiStepScenarios(dir)
	require.NoError(t, err)
	assert.Len(t, scenarios, 1)
}

// TestDiscoverMultiStepScenarios_ComponentEnvOverrideIsValidated proves that
// when a component expectation sets an explicit Env, that name is the one
// checked, not the map key which only disambiguates two components asserted at
// the same env in one step.
func TestDiscoverMultiStepScenarios_ComponentEnvOverrideIsValidated(t *testing.T) {
	dir := t.TempDir()
	body := `
name: "Component env override"
description: "Explicit env on a component expectation is the validated name"
config:
  environments: [dev]
  components:
    api:
      path: api
      environments: [staging]
steps:
  - name: "Deploy dev"
    action: commit
    commit:
      message: "test"
      files:
        test.txt: "content"
    expect:
      state:
        api-row:
          component: api
          env: stagingg
          unchanged: true
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "override.yaml"), []byte(body), 0644))

	_, err := DiscoverMultiStepScenarios(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "override.yaml")
	assert.Contains(t, err.Error(), "stagingg")
}

// TestDiscoverMultiStepScenarios_UnknownStateComponentIsError proves the check
// covers the component axis as well as the env axis. A component the scenario
// never declares produces a composite state key that can never exist, so
// unchanged reads true no matter what the code does. That is the same
// unfalsifiable expectation a typo'd env name produces, one axis over.
func TestDiscoverMultiStepScenarios_UnknownStateComponentIsError(t *testing.T) {
	dir := t.TempDir()
	body := `
name: "Typo'd component"
description: "Asserts unchanged against a component that does not exist"
config:
  environments: [dev, prod]
  components:
    api:
      path: api
    web:
      path: web
steps:
  - name: "Deploy dev"
    action: commit
    commit:
      message: "test"
      files:
        test.txt: "content"
    expect:
      state:
        api-prod:
          component: apii
          env: prod
          unchanged: true
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "typo-component.yaml"), []byte(body), 0644))

	_, err := DiscoverMultiStepScenarios(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "typo-component.yaml")
	assert.Contains(t, err.Error(), "apii")
	assert.Contains(t, err.Error(), "api")
	assert.Contains(t, err.Error(), "web")
}

// TestDiscoverMultiStepScenarios_AcceptsAbsentComponent proves the component
// check only fires when an expectation names a component. A single-component
// scenario asserts on the flat state rows with no component at all, and those
// must stay valid.
func TestDiscoverMultiStepScenarios_AcceptsAbsentComponent(t *testing.T) {
	dir := t.TempDir()
	body := `
name: "No component"
description: "Flat state rows carry no component"
config:
  environments: [dev, prod]
steps:
  - name: "Deploy dev"
    action: commit
    commit:
      message: "test"
      files:
        test.txt: "content"
    expect:
      state:
        prod:
          unchanged: true
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "flat.yaml"), []byte(body), 0644))

	scenarios, err := DiscoverMultiStepScenarios(dir)
	require.NoError(t, err)
	assert.Len(t, scenarios, 1)
}

func TestDiscoverMultiStepScenarios(t *testing.T) {
	// Create temp directory with test scenarios
	dir := t.TempDir()

	// Write a multi-step scenario
	scenario1 := `
name: "Test Scenario 1"
description: "First test"
config:
  environments: [dev, prod]
steps:
  - name: "Step 1"
    action: commit
    commit:
      message: "test"
      files:
        test.txt: "content"
`
	err := os.WriteFile(filepath.Join(dir, "scenario1.yaml"), []byte(scenario1), 0644)
	require.NoError(t, err)

	// Discover scenarios
	scenarios, err := DiscoverMultiStepScenarios(dir)
	require.NoError(t, err)

	assert.Len(t, scenarios, 1)
	assert.Equal(t, "Test Scenario 1", scenarios[0].Name)
}

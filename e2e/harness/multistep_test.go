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
	assert.Equal(t, []string{"dev", "prod"}, scenario.Config.Environments)
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

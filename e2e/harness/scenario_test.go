package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseScenario(t *testing.T) {
	yaml := `
name: "Test scenario"
description: "A test scenario"

setup:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
  commits:
    - message: "feat: initial"
      files:
        src/main.go: |
          package main

trigger:
  workflow: orchestrate.yaml
  event: push

expect:
  workflow:
    conclusion: success
`
	scenario, err := ParseScenario([]byte(yaml))
	require.NoError(t, err)
	assert.Equal(t, "Test scenario", scenario.Name)
	assert.Equal(t, "main", scenario.Setup.Config.TrunkBranch)
	assert.Len(t, scenario.Setup.Config.Environments, 2)
	assert.Equal(t, "orchestrate.yaml", scenario.Trigger.Workflow)
	assert.Equal(t, "success", scenario.Expect.Workflow.Conclusion)
}

func TestDiscoverScenarios(t *testing.T) {
	// Create temp directory with test scenarios
	dir := t.TempDir()

	scenario1 := `
name: "Scenario 1"
description: "First"
setup:
  config:
    trunk_branch: main
trigger:
  workflow: test.yaml
  event: push
expect:
  workflow:
    conclusion: success
`
	scenario2 := `
name: "Scenario 2"
description: "Second"
setup:
  config:
    trunk_branch: main
trigger:
  workflow: test.yaml
  event: push
expect:
  workflow:
    conclusion: success
`
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "orchestrate"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orchestrate", "test1.yaml"), []byte(scenario1), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orchestrate", "test2.yaml"), []byte(scenario2), 0644))

	scenarios, err := DiscoverScenarios(dir)
	require.NoError(t, err)
	assert.Len(t, scenarios, 2)
}

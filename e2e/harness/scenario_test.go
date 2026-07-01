package harness

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
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

// TestConfigReusesTrunkConfig locks in the harness Config sharing the CLI's own
// manifest type. When these are the same type, every field the CLI parses is
// reachable from a scenario without a parallel struct to hand-maintain, which is
// the whole point of the reuse: a field added to config.TrunkConfig needs no
// harness edit to be marshalable into a scenario's manifest.yaml.
func TestConfigReusesTrunkConfig(t *testing.T) {
	assert.Equal(t, reflect.TypeOf(config.TrunkConfig{}), reflect.TypeOf(Config{}),
		"harness Config must be config.TrunkConfig so new manifest fields flow through without a hand-edit")
}

// TestConfigCarriesFieldWithoutHarnessEdit proves the regression the reuse
// closes: a manifest field that the retired hand-mirrored struct never listed is
// now parsed from a scenario and marshaled back into the generated ci.config
// block with no harness change. tag_prefix stands in for any such field. It was
// absent from the old parallel struct, so before the reuse it was silently
// dropped; now it round-trips because the harness marshals the CLI's own type.
func TestConfigCarriesFieldWithoutHarnessEdit(t *testing.T) {
	const scenarioYAML = `
name: "Field reach"
description: "tag_prefix survives the marshal round-trip"
setup:
  config:
    trunk_branch: main
    tag_prefix: component-
    environments:
      - dev
trigger:
  workflow: orchestrate.yaml
  event: push
expect:
  workflow:
    conclusion: success
`
	scenario, err := ParseScenario([]byte(scenarioYAML))
	require.NoError(t, err)
	require.Equal(t, "component-", scenario.Setup.Config.TagPrefix,
		"scenario YAML must parse the field the old struct dropped")

	// Mirror how the harness writes manifest.yaml: the config under ci.config.
	manifest := map[string]any{
		"ci": map[string]any{
			"config": scenario.Setup.Config,
		},
	}
	out, err := yaml.Marshal(manifest)
	require.NoError(t, err)
	assert.Contains(t, string(out), "tag_prefix: component-",
		"the field must reach the generated manifest without a harness edit")

	// The config block must parse back into the CLI's type unchanged, so the
	// field is not merely emitted but actually consumed as the CLI sees it.
	configOut, err := yaml.Marshal(scenario.Setup.Config)
	require.NoError(t, err)
	var roundTrip config.TrunkConfig
	require.NoError(t, yaml.Unmarshal(configOut, &roundTrip))
	assert.Equal(t, "component-", roundTrip.TagPrefix)
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

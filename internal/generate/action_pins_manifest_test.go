package generate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestDefaultActionPins_MatchesPriorHardcodedTable is the value-preserving
// contract for extracting the pin table into action_pins.yaml. It pins the
// EXACT values the hardcoded defaultActionPins map carried before the manifest
// existed; if parsing the embedded YAML produces anything different the refactor
// has changed behavior and this test fails. Update the values here only when an
// intentional bump (a later PR) changes them.
func TestDefaultActionPins_MatchesPriorHardcodedTable(t *testing.T) {
	priorHardcodedTable := map[string]actionPin{
		actionCheckout:         {tag: "v6", sha: "df4cb1c069e1874edd31b4311f1884172cec0e10", shaVersion: "v6.0.3"},
		actionGithubScript:     {tag: "v7", sha: "f28e40c7f34bde8b3046d885e986cb6290c5673b", shaVersion: "v7.1.0"},
		actionDownloadArtifact: {tag: "v4", sha: "d3f86a106a0bac45b974a628896c90dbdf5c8093", shaVersion: "v4.3.0"},
		actionUploadArtifact:   {tag: "v4", sha: "ea165f8d65b6e75b540449e92b4886f43607fa02", shaVersion: "v4.6.2"},
		actionCreateAppToken:   {tag: "v3", sha: "bcd2ba49218906704ab6c1aa796996da409d3eb1", shaVersion: "v3.2.0"},
	}

	// The parsed generator table must contain exactly the prior emit:true set,
	// value-for-value, with no extra keys leaking in from emit:false entries.
	assert.Len(t, defaultActionPins, len(priorHardcodedTable),
		"parsed table must hold exactly the prior emit:true actions")
	for action, want := range priorHardcodedTable {
		got, ok := defaultActionPins[action]
		require.Truef(t, ok, "parsed table is missing %s", action)
		assert.Equalf(t, want.tag, got.tag, "tag for %s", action)
		assert.Equalf(t, want.sha, got.sha, "sha for %s", action)
		assert.Equalf(t, want.shaVersion, got.shaVersion, "shaVersion for %s", action)
	}
}

// TestActionPinsManifest_EmitFlags asserts every action in the embedded manifest
// carries the correct emit flag: the five generator-emitted actions are
// emit:true and the four maintainer-only actions are emit:false. This guards the
// filter mustParseActionPins applies when building the generator table.
func TestActionPinsManifest_EmitFlags(t *testing.T) {
	var manifest actionPinsManifest
	require.NoError(t, yaml.Unmarshal(actionPinsYAML, &manifest))

	wantEmit := map[string]bool{
		actionCheckout:                  true,
		actionGithubScript:              true,
		actionDownloadArtifact:          true,
		actionUploadArtifact:            true,
		actionCreateAppToken:            true,
		"actions/setup-go":              false,
		"actions/setup-node":            false,
		"actions/upload-pages-artifact": false,
		"actions/deploy-pages":          false,
	}

	assert.Len(t, manifest.Actions, len(wantEmit),
		"manifest must list exactly the known actions")
	for action, want := range wantEmit {
		entry, ok := manifest.Actions[action]
		require.Truef(t, ok, "manifest is missing %s", action)
		assert.Equalf(t, want, entry.Emit, "emit flag for %s", action)
		assert.NotEmptyf(t, entry.Tag, "tag for %s", action)
		assert.NotEmptyf(t, entry.Version, "version for %s", action)
		assert.Lenf(t, entry.SHA, 40, "sha for %s must be a 40-char commit SHA", action)
	}
}

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
		actionCheckout:         {tag: "v7", sha: "9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0", shaVersion: "v7.0.0"},
		actionGithubScript:     {tag: "v9", sha: "3a2844b7e9c422d3c10d287c895573f7108da1b3", shaVersion: "v9.0.0"},
		actionDownloadArtifact: {tag: "v8", sha: "3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c", shaVersion: "v8.0.1"},
		actionUploadArtifact:   {tag: "v7", sha: "043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", shaVersion: "v7.0.1"},
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

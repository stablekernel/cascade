package generate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestDefaultActionPins_OnlyEmitTrueEntriesWellFormed guards the shape of the
// parsed generator table, not frozen pin values. action_pins.yaml is the
// single source of truth for tag/sha/version, and it legitimately changes
// over time (Dependabot bumps, and the self-heal reconcile companion adopting
// a new governed pin); a test that freezes exact values fails on every such
// legitimate change, which is exactly the failure this test replaces.
//
// What it still checks:
//  1. defaultActionPins contains exactly the manifest's emit:true actions,
//     nothing more and nothing less, so no emit:false entry leaks into the
//     table the generator renders and no emit:true action is dropped.
//  2. Every entry is well-formed: a non-empty tag, a non-empty shaVersion,
//     and a sha matching commitSHAPattern (40-char lowercase hex).
func TestDefaultActionPins_OnlyEmitTrueEntriesWellFormed(t *testing.T) {
	var manifest actionPinsManifest
	require.NoError(t, yaml.Unmarshal(actionPinsYAML, &manifest))

	wantEmitTrue := make(map[string]bool)
	for action, entry := range manifest.Actions {
		if entry.Emit {
			wantEmitTrue[action] = true
		}
	}

	assert.Lenf(t, defaultActionPins, len(wantEmitTrue),
		"parsed table must hold exactly the manifest's emit:true actions")

	for action := range wantEmitTrue {
		pin, ok := defaultActionPins[action]
		require.Truef(t, ok, "parsed table is missing emit:true action %s", action)

		assert.NotEmptyf(t, pin.tag, "tag for %s", action)
		assert.NotEmptyf(t, pin.shaVersion, "shaVersion for %s", action)
		assert.Truef(t, commitSHAPattern.MatchString(pin.sha),
			"sha for %s must be a 40-char lowercase hex commit sha, got %q", action, pin.sha)
	}

	for action := range defaultActionPins {
		assert.Truef(t, wantEmitTrue[action],
			"defaultActionPins contains %s, which is not emit:true in action_pins.yaml", action)
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
		"actions/cache":                 false,
		"actions/setup-go":              false,
		"actions/setup-node":            false,
		"actions/upload-pages-artifact": false,
		"actions/deploy-pages":          false,
		"sigstore/cosign-installer":     false,
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

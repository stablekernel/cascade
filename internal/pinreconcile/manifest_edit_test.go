package pinreconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// decodedActionPins is the minimal shape needed to assert what a standard
// (comment-stripping) yaml.Unmarshal actually sees after a surgical edit, which
// is the guarantee that matters: a sha-plus-version adoption must survive a
// real re-parse, not just look right as raw bytes.
type decodedActionPins struct {
	CI struct {
		ActionPins map[string]string `yaml:"action_pins"`
	} `yaml:"ci"`
}

func TestSetActionPinSurgical_PreservesCommentsAndOrder(t *testing.T) {
	doc := []byte("" +
		"ci:\n" +
		"  pin_mode: sha\n" +
		"  # keep this comment\n" +
		"  action_pins:\n" +
		"    actions/checkout: oldsha # v5.0.0\n" +
		"    actions/upload-artifact: keepme # v4.0.0\n")

	out, err := SetActionPinSurgical(doc, "actions/checkout", "newsha # v6.0.0")
	require.NoError(t, err)
	s := string(out)

	// The adopted value carries a literal "#", which yaml.v3 quotes so the
	// comment stays part of the string instead of becoming a real YAML
	// comment that a later re-parse would silently drop.
	require.Contains(t, s, "actions/checkout: 'newsha # v6.0.0'")
	require.Contains(t, s, "# keep this comment")                        // untouched comment survives
	require.Contains(t, s, "actions/upload-artifact: keepme # v4.0.0") // untouched key survives, order kept

	var got decodedActionPins
	require.NoError(t, yaml.Unmarshal(out, &got))
	require.Equal(t, "newsha # v6.0.0", got.CI.ActionPins["actions/checkout"],
		"the adopted sha must keep its version comment through a real re-parse")
	require.Equal(t, "keepme", got.CI.ActionPins["actions/upload-artifact"],
		"the untouched entry's trailing text was always a plain comment, not adopted data")
}

func TestSetActionPinSurgical_AddsMissingKey(t *testing.T) {
	doc := []byte("ci:\n  action_pins:\n    actions/checkout: a # v5\n")
	out, err := SetActionPinSurgical(doc, "actions/upload-artifact", "b # v4")
	require.NoError(t, err)
	require.Contains(t, string(out), "actions/upload-artifact: 'b # v4'")

	var got decodedActionPins
	require.NoError(t, yaml.Unmarshal(out, &got))
	require.Equal(t, "b # v4", got.CI.ActionPins["actions/upload-artifact"])
	require.Equal(t, "a", got.CI.ActionPins["actions/checkout"], "untouched key is preserved")
}

func TestSetActionPinSurgical_PlainTagStaysUnquoted(t *testing.T) {
	doc := []byte("ci:\n  action_pins:\n    actions/checkout: v5\n")
	out, err := SetActionPinSurgical(doc, "actions/checkout", "v6")
	require.NoError(t, err)
	require.Contains(t, string(out), "actions/checkout: v6\n",
		"a plain tag with no embedded comment token needs no quoting")
}

func TestSetActionPinSurgical_CreatesMissingActionPinsMapping(t *testing.T) {
	doc := []byte("ci:\n  pin_mode: sha\n")
	out, err := SetActionPinSurgical(doc, "actions/checkout", "v6")
	require.NoError(t, err)

	var got decodedActionPins
	require.NoError(t, yaml.Unmarshal(out, &got))
	require.Equal(t, "v6", got.CI.ActionPins["actions/checkout"])
}

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateActionPins_RejectsNewline guards the action_pins override sink:
// actionRef splices the value raw into a generated workflow, so a value
// carrying a newline (or otherwise breaking out of the emitted YAML scalar)
// must be rejected rather than reaching the generator.
func TestValidateActionPins_RejectsNewline(t *testing.T) {
	t.Parallel()
	cfg := &TrunkConfig{ActionPins: map[string]string{
		"actions/checkout": "v5\n      run: curl evil",
	}}
	errs := validateActionPins(cfg)
	require.NotEmpty(t, errs)
}

// TestValidateActionPins_AllowsShaWithComment confirms the accepted shape: a
// full-length sha plus its trailing "# <version>" comment, the form a
// reconcile adoption writes, still validates clean.
func TestValidateActionPins_AllowsShaWithComment(t *testing.T) {
	t.Parallel()
	cfg := &TrunkConfig{ActionPins: map[string]string{
		"actions/checkout": "abc123def4567890abc123def4567890abc12345 # v5.1.0",
	}}
	require.Empty(t, validateActionPins(cfg))
}

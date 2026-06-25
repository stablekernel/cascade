package simulate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func helpText(t *testing.T, args ...string) string {
	t.Helper()

	cmd := NewCommand()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute())
	return buf.String()
}

func TestSimulateHelp_MentionsScopeAndIsolation(t *testing.T) {
	t.Parallel()

	out := strings.ToLower(helpText(t, "--help"))
	assert.Contains(t, out, "orchestration")
	assert.Contains(t, out, "no github")
	assert.Contains(t, out, "no containers")
	assert.Contains(t, out, "not your real deploy scripts")
}

func TestSimulatePromoteHelp_MentionsScopeAndIsolation(t *testing.T) {
	t.Parallel()

	out := strings.ToLower(helpText(t, "promote", "--help"))
	assert.Contains(t, out, "orchestration")
	assert.Contains(t, out, "no github")
	assert.Contains(t, out, "no containers")
	assert.Contains(t, out, "not your deploy scripts")
}

func TestSimulatePromote_InvalidMode(t *testing.T) {
	t.Parallel()

	_, err := parseMode("bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mode")
}

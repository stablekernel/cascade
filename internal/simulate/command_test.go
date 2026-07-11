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

func TestSimulateSubcommands_Registered(t *testing.T) {
	t.Parallel()

	out := helpText(t, "--help")
	for _, sub := range []string{"promote", "rollback", "release", "hotfix"} {
		assert.Contains(t, out, sub)
	}
}

func TestSimulateRollbackHelp_MentionsScopeAndIsolation(t *testing.T) {
	t.Parallel()

	out := strings.ToLower(helpText(t, "rollback", "--help"))
	assert.Contains(t, out, "orchestration")
	assert.Contains(t, out, "no github")
	assert.Contains(t, out, "no containers")
}

func TestSimulateReleaseHelp_MentionsScopeAndIsolation(t *testing.T) {
	t.Parallel()

	out := strings.ToLower(helpText(t, "release", "--help"))
	assert.Contains(t, out, "orchestration")
	assert.Contains(t, out, "no github")
	assert.Contains(t, out, "no containers")
}

func TestSimulateHotfixHelp_MentionsScopeAndIsolation(t *testing.T) {
	t.Parallel()

	out := strings.ToLower(helpText(t, "hotfix", "--help"))
	assert.Contains(t, out, "orchestration")
	assert.Contains(t, out, "no github")
	assert.Contains(t, out, "no containers")
}

func TestSimulateHelp_MentionsDeployResultFlag(t *testing.T) {
	t.Parallel()

	out := helpText(t, "--help")
	assert.Contains(t, out, "--deploy-result")
}

func TestCommonFlags_EngineOptions_InvalidDeployResult(t *testing.T) {
	t.Parallel()

	cf := &commonFlags{deployResults: []string{"services=maybe"}}
	_, err := cf.engineOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown outcome")
}

func TestCommonFlags_EngineOptions_DefaultIsActorAndComponent(t *testing.T) {
	t.Parallel()

	cf := &commonFlags{actor: "tester"}
	opts, err := cf.engineOptions()
	require.NoError(t, err)
	assert.Len(t, opts, 2, "actor and component options with no deploy-result pairs")
}

func TestValidateComponentSelection(t *testing.T) {
	t.Parallel()

	mono := writeRaw(t, simMonorepoManifest)
	single := seedManifest(t)

	t.Run("monorepo without component is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateComponentSelection(mono, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pass --component")
		assert.Contains(t, err.Error(), "api, web")
	})

	t.Run("monorepo with unknown component is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateComponentSelection(mono, "billing")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown component")
	})

	t.Run("monorepo with declared component is accepted", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, validateComponentSelection(mono, "api"))
	})

	t.Run("single-component without component is accepted", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, validateComponentSelection(single, ""))
	})

	t.Run("single-component with a component is rejected", func(t *testing.T) {
		t.Parallel()
		err := validateComponentSelection(single, "api")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "declares no components")
	})
}

func TestSimulateHelp_MentionsComponentFlag(t *testing.T) {
	t.Parallel()

	out := helpText(t, "--help")
	assert.Contains(t, out, "--component")
}

func TestParseCommaList(t *testing.T) {
	t.Parallel()

	got, err := parseCommaList(" a , b ,c")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, got)

	_, err = parseCommaList("")
	require.Error(t, err)

	_, err = parseCommaList("a,,b")
	require.Error(t, err)
}

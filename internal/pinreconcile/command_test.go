package pinreconcile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()

	assert.Equal(t, "reconcile", cmd.Use)

	checkFlag := cmd.Flags().Lookup("check")
	require.NotNil(t, checkFlag)
	assert.Equal(t, "bool", checkFlag.Value.Type())
	assert.Equal(t, "false", checkFlag.DefValue)

	ownRepoFlag := cmd.Flags().Lookup("own-repo")
	require.NotNil(t, ownRepoFlag)
	assert.Equal(t, "bool", ownRepoFlag.Value.Type())
	assert.Equal(t, "false", ownRepoFlag.DefValue)

	rootFlag := cmd.Flags().Lookup("root")
	require.NotNil(t, rootFlag)

	configFlag := cmd.Flags().Lookup("config")
	require.NotNil(t, configFlag)
}

// TestNewCommand_RejectsCheckAndOwnRepoTogether asserts --check (a read-only
// detector) and --own-repo (a write mode) cannot be combined: cobra's
// mutually-exclusive flag annotation must reject the pair before RunE runs.
func TestNewCommand_RejectsCheckAndOwnRepoTogether(t *testing.T) {
	cmd := NewCommand()
	cmd.SetArgs([]string{"--check", "--own-repo"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "own-repo")
}

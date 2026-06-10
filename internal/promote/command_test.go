package promote

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPromoteCommand_HasPreflightSubcommand(t *testing.T) {
	cmd := NewCommand()
	preflight, _, err := cmd.Find([]string{"preflight"})
	require.NoError(t, err)
	require.NotNil(t, preflight)
	require.Equal(t, "preflight", preflight.Name())
}

func TestPromoteCommand_HasFinalizeSubcommand(t *testing.T) {
	cmd := NewCommand()
	finalize, _, err := cmd.Find([]string{"finalize"})
	require.NoError(t, err)
	require.NotNil(t, finalize)
	require.Equal(t, "finalize", finalize.Name())
}

func TestPromoteCommand_HasPersistentFlags(t *testing.T) {
	cmd := NewCommand()

	// Check that the parent command has the expected persistent flags
	flags := []string{"config", "dry-run", "json", "gha-output", "actor"}
	for _, flagName := range flags {
		flag := cmd.PersistentFlags().Lookup(flagName)
		require.NotNil(t, flag, "Expected persistent flag %s to exist", flagName)
	}
}

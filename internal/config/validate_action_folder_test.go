package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateActionFolder_RejectsTraversal guards the action_folder sink:
// the generator joins it directly into a filesystem path
// (.github/actions/<folder>/action.yaml), so it must reject a traversal or
// nested-path shape and accept only a plain folder name.
func TestValidateActionFolder_RejectsTraversal(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, validateActionFolder("../../etc/foo"))
	require.NotEmpty(t, validateActionFolder("a/b"))
	require.Empty(t, validateActionFolder("manage-release"))
}

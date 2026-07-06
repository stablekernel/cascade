package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadActionPinTable_FromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "action_pins.yaml")
	require.NoError(t, os.WriteFile(path, actionPinsYAML, 0o600))

	table, err := LoadActionPinTable(path)
	require.NoError(t, err)
	require.Equal(t, defaultActionPins[actionCheckout], table[actionCheckout])
}

func TestLoadActionPinTable_RejectsBadSHA(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "action_pins.yaml")
	require.NoError(t, os.WriteFile(path, []byte("actions:\n  actions/checkout:\n    tag: v5\n    sha: nope\n    version: v5.0.0\n    emit: true\n"), 0o600))
	_, err := LoadActionPinTable(path)
	require.Error(t, err)
}

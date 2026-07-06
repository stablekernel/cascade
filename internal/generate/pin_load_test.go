package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
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

func TestApplyDiskPinOverrides_ReachesConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "action_pins.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"actions:\n"+
			"  actions/checkout:\n    tag: v9\n    sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    version: v9.9.9\n    emit: true\n"), 0o600))

	cfg := &config.TrunkConfig{PinMode: config.PinModeSHA}
	require.NoError(t, ApplyDiskPinOverrides(cfg, path))
	require.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa # v9.9.9", cfg.ActionPins["actions/checkout"])
	require.Equal(t, "actions/checkout@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa # v9.9.9", actionRef(cfg, "actions/checkout"))
}

func TestApplyDiskPinOverrides_DoesNotClobberUserPins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "action_pins.yaml")
	require.NoError(t, os.WriteFile(path, actionPinsYAML, 0o600))
	cfg := &config.TrunkConfig{ActionPins: map[string]string{"actions/checkout": "v3"}}
	require.NoError(t, ApplyDiskPinOverrides(cfg, path))
	require.Equal(t, "v3", cfg.ActionPins["actions/checkout"], "an explicit user override must win over the disk overlay")
}

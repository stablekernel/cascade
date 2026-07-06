package generate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// TestRenderLocalActions_RejectsUnsafeActionFolder is a defense-in-depth
// guard: even though config validation already rejects an unsafe
// action_folder, the generator itself refuses to join an unsafe value into a
// filesystem path rather than trusting the caller validated it.
func TestRenderLocalActions_RejectsUnsafeActionFolder(t *testing.T) {
	t.Parallel()
	cfg := &config.TrunkConfig{ActionFolder: "../../etc/foo"}
	_, err := RenderLocalActions(t.TempDir(), cfg)
	require.Error(t, err)
}

// TestGenerateLocalActions_RejectsUnsafeActionFolder mirrors the above for the
// disk-writing entry point.
func TestGenerateLocalActions_RejectsUnsafeActionFolder(t *testing.T) {
	t.Parallel()
	cfg := &config.TrunkConfig{ActionFolder: "a/b"}
	err := GenerateLocalActions(t.TempDir(), cfg)
	require.Error(t, err)
}

// TestRenderLocalActions_AllowsDefaultActionFolder is the regression lock: a
// safe, default action_folder still renders with no error.
func TestRenderLocalActions_AllowsDefaultActionFolder(t *testing.T) {
	t.Parallel()
	cfg := &config.TrunkConfig{}
	_, err := RenderLocalActions(t.TempDir(), cfg)
	require.NoError(t, err)
}

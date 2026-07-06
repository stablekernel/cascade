package pinreconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStagePathspec_ManifestOnlyByDefault covers the common case: a reconcile
// commit stages only the manifest, never a wildcard pathspec that could pick
// up an unrelated working-tree change.
func TestStagePathspec_ManifestOnlyByDefault(t *testing.T) {
	require.Equal(t, []string{".github/manifest.yaml"}, StagePathspec(".github/manifest.yaml", false))
}

// TestStagePathspec_IncludesWorkflowsWhenRegenerating covers the mode where a
// regenerate must itself push workflow files alongside the manifest.
func TestStagePathspec_IncludesWorkflowsWhenRegenerating(t *testing.T) {
	require.Equal(t,
		[]string{".github/manifest.yaml", ".github/workflows/*.yaml"},
		StagePathspec(".github/manifest.yaml", true))
}

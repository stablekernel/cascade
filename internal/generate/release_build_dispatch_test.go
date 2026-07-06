package generate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generatePromote builds a promote workflow from the given config and returns
// its rendered content. It centralizes the generator setup so the
// release-build dispatch table tests stay focused on the assertions.
func generatePromote(t *testing.T, cfg *config.TrunkConfig) string {
	t.Helper()
	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)
	return content
}

// TestPromoteGenerator_ReleaseBuildDispatch asserts that the finalize stage only
// emits the "Trigger Release Build" step when release.workflow is configured, and
// that when emitted it dispatches the configured reusable workflow rather than a
// hardcoded literal.
func TestPromoteGenerator_ReleaseBuildDispatch(t *testing.T) {
	tests := []struct {
		name            string
		release         *config.ReleaseConfig
		wantStep        bool
		wantDispatch    string
		notWantDispatch string
	}{
		{
			name:            "no release workflow configured omits the step",
			release:         nil,
			wantStep:        false,
			notWantDispatch: "gh workflow run Release",
		},
		{
			name:            "release config without workflow omits the step",
			release:         &config.ReleaseConfig{Tag: "callback.output.release_id"},
			wantStep:        false,
			notWantDispatch: "gh workflow run Release",
		},
		{
			name:         "release workflow configured emits a dispatch to it",
			release:      &config.ReleaseConfig{Workflow: ".github/workflows/release-build.yaml"},
			wantStep:     true,
			wantDispatch: "gh workflow run release-build.yaml",
		},
		{
			name:         "bare release workflow filename dispatches unchanged",
			release:      &config.ReleaseConfig{Workflow: "release-build.yaml"},
			wantStep:     true,
			wantDispatch: "gh workflow run release-build.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.TrunkConfig{
				TrunkBranch:  "main",
				Environments: []string{"dev", "prod"},
				Release:      tt.release,
			}

			content := generatePromote(t, cfg)

			if tt.wantStep {
				assert.Contains(t, content, "- name: Trigger Release Build",
					"expected the Trigger Release Build step when release.workflow is set")
				body := stepRunBody(t, content, "Trigger Release Build")
				assert.Contains(t, body, tt.wantDispatch,
					"expected the step to dispatch the configured release workflow")
				assert.NotContains(t, content, "gh workflow run Release \\",
					"the configured dispatch must not fall back to the hardcoded Release literal")
			} else {
				assert.NotContains(t, content, "Trigger Release Build",
					"the Trigger Release Build step must be absent without release.workflow")
				assert.NotContains(t, content, tt.notWantDispatch,
					"no hardcoded Release dispatch may be emitted without release.workflow")
			}

			// The literal "gh workflow run Release " (trailing space before the
			// line continuation) must never appear; only normalized paths.
			assert.False(t, strings.Contains(content, "gh workflow run Release \\"),
				"the hardcoded Release dispatch must never be emitted")
		})
	}
}

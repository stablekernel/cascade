package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// candidateDispatchConfig builds a minimal orchestrate config with a single
// build callback, allowing the caller to toggle dispatch mode and the
// release.workflow that the candidate build is dispatched against.
func candidateDispatchConfig(t *testing.T, dispatchOnly bool, release *config.ReleaseConfig) (*config.TrunkConfig, string) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch: "main",
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Release: release,
	}
	if dispatchOnly {
		cfg.ReleaseTrigger = config.ReleaseTriggerDispatch
	}
	return cfg, tmpDir
}

// TestGenerator_CandidateDispatchStep asserts that the orchestrate finalize job
// emits an explicit release-workflow dispatch against the candidate tag only
// when the trunk is dispatch-driven AND a release.workflow is configured. The
// tag-push trigger is unreliable for a candidate tag that points at a CI-skip
// state commit, so the explicit dispatch is the dependable build path; gating it
// on dispatch mode keeps it from racing a push-mode trunk's native tag trigger.
func TestGenerator_CandidateDispatchStep(t *testing.T) {
	tests := []struct {
		name         string
		dispatchOnly bool
		release      *config.ReleaseConfig
		wantStep     bool
	}{
		{
			name:         "dispatch mode with release workflow emits the dispatch",
			dispatchOnly: true,
			release:      &config.ReleaseConfig{Workflow: ".github/workflows/release.yaml"},
			wantStep:     true,
		},
		{
			name:         "dispatch mode without release workflow omits the dispatch",
			dispatchOnly: true,
			release:      nil,
			wantStep:     false,
		},
		{
			name:         "push mode with release workflow omits the dispatch",
			dispatchOnly: false,
			release:      &config.ReleaseConfig{Workflow: ".github/workflows/release.yaml"},
			wantStep:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, tmpDir := candidateDispatchConfig(t, tt.dispatchOnly, tt.release)

			content, err := NewGenerator(cfg, tmpDir).Generate()
			require.NoError(t, err)

			if tt.wantStep {
				require.Contains(t, content, "- name: Dispatch Release Candidate Build",
					"expected the candidate dispatch step in dispatch mode with a release workflow")
				body := stepRunBody(t, content, "Dispatch Release Candidate Build")
				assert.Contains(t, body, "gh workflow run release.yaml",
					"the step must dispatch the configured release workflow by its bare file name; "+
						"gh workflow run 404s on a \"./\"-prefixed repo path")
				assert.Contains(t, body, `--ref "$TAG"`,
					"the dispatch must target the candidate tag")
			} else {
				assert.NotContains(t, content, "Dispatch Release Candidate Build",
					"the candidate dispatch step must be absent")
			}
		})
	}
}

// TestGenerator_CandidateDispatchNormalizesBareFilename asserts a bare release
// workflow filename dispatches unchanged: gh workflow run addresses a same-repo
// workflow by file name (or numeric ID), never by a "./"-prefixed repository
// path, so an already-bare name needs no further normalization.
func TestGenerator_CandidateDispatchNormalizesBareFilename(t *testing.T) {
	cfg, tmpDir := candidateDispatchConfig(t, true, &config.ReleaseConfig{Workflow: "release.yaml"})

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	body := stepRunBody(t, content, "Dispatch Release Candidate Build")
	assert.Contains(t, body, "gh workflow run release.yaml",
		"a bare release workflow filename must dispatch unchanged")
}

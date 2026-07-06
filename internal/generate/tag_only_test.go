package generate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockBetween returns the substring of content starting at the first occurrence
// of start (inclusive) up to the next occurrence of end after it (exclusive). It
// scopes an assertion to a single workflow step so a token line from an unrelated
// step cannot satisfy or defeat it.
func blockBetween(t *testing.T, content, start, end string) string {
	t.Helper()
	i := strings.Index(content, start)
	require.GreaterOrEqual(t, i, 0, "start marker %q not found", start)
	rest := content[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return content[i:]
	}
	return content[i : i+len(start)+j]
}

// pat is a distinct, non-default release token so the tag-create token switch is
// observable: the default release_token already resolves to GITHUB_TOKEN, which
// would make "uses GITHUB_TOKEN" true in both modes and hide the switch.
const pat = "${{ secrets.CASCADE_STATE_TOKEN }}"

// TestGenerator_OrchestrateTagOnly asserts the orchestrate finalize Manage
// Release step honors release.tag_only: it emits tag_only: 'true' and creates
// the tag with the non-triggering GITHUB_TOKEN (not the configured PAT), while
// the default path keeps the PAT and emits no tag_only. This is both the feature
// assertion and the scoping regression guard: a manifest without tag_only is
// unchanged.
func TestGenerator_OrchestrateTagOnly(t *testing.T) {
	tests := []struct {
		name         string
		tagOnly      bool
		wantTagOnly  bool
		wantStepPAT  bool // Manage Release step uses the PAT (else GITHUB_TOKEN)
	}{
		{name: "tag-only cuts the tag with GITHUB_TOKEN and skips the draft", tagOnly: true, wantTagOnly: true, wantStepPAT: false},
		{name: "default keeps the PAT and pre-creates the draft", tagOnly: false, wantTagOnly: false, wantStepPAT: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := &config.ReleaseConfig{Workflow: ".github/workflows/release.yaml", TagOnly: tt.tagOnly}
			cfg, tmpDir := candidateDispatchConfig(t, true /* dispatch */, release)
			cfg.ReleaseToken = pat

			content, err := NewGenerator(cfg, tmpDir).Generate()
			require.NoError(t, err)

			step := blockBetween(t, content, "- name: Manage Release", "- name: Dispatch Release Candidate Build")

			if tt.wantTagOnly {
				assert.Contains(t, step, "tag_only: 'true'", "tag-only manage-release step must set tag_only")
			} else {
				assert.NotContains(t, step, "tag_only:", "default manage-release step must not set tag_only")
			}

			if tt.wantStepPAT {
				assert.Contains(t, step, "token: "+pat, "default manage-release step must keep the release PAT")
			} else {
				assert.Contains(t, step, "token: ${{ secrets.GITHUB_TOKEN }}",
					"tag-only manage-release step must create the tag with the non-triggering GITHUB_TOKEN")
				assert.NotContains(t, step, pat, "tag-only manage-release step must not use the triggering PAT")
			}

			// The explicit candidate dispatch always keeps the release PAT so the
			// dispatched Release run still cascades to fleet-e2e via workflow_run.
			dispatch := blockBetween(t, content, "- name: Dispatch Release Candidate Build", "- name: Update Manifest")
			assert.Contains(t, dispatch, "GITHUB_TOKEN: "+pat,
				"the release-workflow dispatch must use the PAT so its run cascades downstream")
		})
	}
}

// TestGenerator_ManageReleaseActionTagOnlyInput asserts the generated composite
// action exposes the tag_only input and forwards it to the CLI as --tag-only.
// The input is additive (default 'false'), so downstream behavior is unchanged.
func TestGenerator_ManageReleaseActionTagOnlyInput(t *testing.T) {
	action := generateManageReleaseAction()
	assert.Contains(t, action, "  tag_only:", "composite action must expose the tag_only input")
	assert.Contains(t, action, "default: 'false'", "tag_only must default off so existing callers are unchanged")
	assert.Contains(t, action, "INPUT_TAG_ONLY: ${{ inputs.tag_only }}", "tag_only must be wired to an env var")
	assert.Contains(t, action, `[[ "$INPUT_TAG_ONLY" == "true" ]] && CMD_ARGS+=(--tag-only)`,
		"tag_only must forward --tag-only to the CLI")
}

// TestPromoteGenerator_PublishTagOnlyToken asserts the promote Publish Release
// step cuts the semver tag with the non-triggering GITHUB_TOKEN under
// release.tag_only, and keeps the configured PAT otherwise.
func TestPromoteGenerator_PublishTagOnlyToken(t *testing.T) {
	tests := []struct {
		name        string
		tagOnly     bool
		wantStepPAT bool
	}{
		{name: "tag-only publishes the semver tag with GITHUB_TOKEN", tagOnly: true, wantStepPAT: false},
		{name: "default keeps the release PAT", tagOnly: false, wantStepPAT: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.TrunkConfig{
				TrunkBranch:    "main",
				ReleaseTrigger: config.ReleaseTriggerDispatch,
				Environments:   []string{"dev", "prod"},
				ReleaseToken:   pat,
				Release:        &config.ReleaseConfig{Workflow: ".github/workflows/release.yaml", TagOnly: tt.tagOnly},
			}

			content := generatePromote(t, cfg)
			step := blockBetween(t, content, "- name: Publish Release", "- name: Trigger Release Build")

			if tt.wantStepPAT {
				assert.Contains(t, step, "token: "+pat, "default publish step must keep the release PAT")
			} else {
				assert.Contains(t, step, "token: ${{ secrets.GITHUB_TOKEN }}",
					"tag-only publish step must cut the semver tag with the non-triggering GITHUB_TOKEN")
			}
		})
	}
}

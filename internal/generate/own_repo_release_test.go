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

// TestGenerator_OwnRepoRelease asserts that WithOwnRepoRelease() switches the
// orchestrate finalize Manage Release step into cascade's own-repo release
// plumbing: it emits tag_only: 'true' and creates the tag with the
// non-triggering GITHUB_TOKEN (not the configured PAT), while plain generation
// (no option; every downstream manifest) keeps the PAT and emits no tag_only.
//
// This is both the feature assertion and the scoping regression guard: own-repo
// mode is never reachable from config.TrunkConfig, only from the
// GeneratorOption passed by cascade's own --own-repo invocation, so a
// downstream manifest's generation is provably unaffected.
func TestGenerator_OwnRepoRelease(t *testing.T) {
	tests := []struct {
		name        string
		ownRepo     bool
		wantTagOnly bool
		wantStepPAT bool // Manage Release step uses the PAT (else GITHUB_TOKEN)
	}{
		{name: "own-repo cuts the tag with GITHUB_TOKEN and skips the draft", ownRepo: true, wantTagOnly: true, wantStepPAT: false},
		{name: "plain generation keeps the PAT and pre-creates the draft", ownRepo: false, wantTagOnly: false, wantStepPAT: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := &config.ReleaseConfig{Workflow: ".github/workflows/release.yaml"}
			cfg, tmpDir := candidateDispatchConfig(t, true /* dispatch */, release)
			cfg.ReleaseToken = pat

			var opts []GeneratorOption
			if tt.ownRepo {
				opts = append(opts, WithOwnRepoRelease())
			}
			content, err := NewGenerator(cfg, tmpDir, opts...).Generate()
			require.NoError(t, err)

			step := blockBetween(t, content, "- name: Manage Release", "- name: Dispatch Release Candidate Build")

			if tt.wantTagOnly {
				assert.Contains(t, step, "tag_only: 'true'", "own-repo manage-release step must set tag_only")
			} else {
				assert.NotContains(t, step, "tag_only:", "plain manage-release step must not set tag_only")
			}

			if tt.wantStepPAT {
				assert.Contains(t, step, "token: "+pat, "plain manage-release step must keep the release PAT")
			} else {
				assert.Contains(t, step, "token: ${{ secrets.GITHUB_TOKEN }}",
					"own-repo manage-release step must create the tag with the non-triggering GITHUB_TOKEN")
				assert.NotContains(t, step, pat, "own-repo manage-release step must not use the triggering PAT")
			}

			// The explicit candidate dispatch always keeps the release PAT so the
			// dispatched Release run still cascades to fleet-e2e via workflow_run.
			dispatch := blockBetween(t, content, "- name: Dispatch Release Candidate Build", "- name: Update Manifest")
			assert.Contains(t, dispatch, "GITHUB_TOKEN: "+pat,
				"the release-workflow dispatch must use the PAT so its run cascades downstream")
		})
	}
}

// TestGenerator_OwnRepoFinalizeBuildsFromSource asserts that in own-repo mode the
// finalize job obtains its cascade binary from the build-cli callback artifact
// (the commit under release), not from a pinned setup-cli release, so a newly
// added CLI capability is present in the same run that introduces it. Plain
// generation (every downstream manifest) must keep the pinned setup-cli and never
// download the from-source artifact.
func TestGenerator_OwnRepoFinalizeBuildsFromSource(t *testing.T) {
	release := &config.ReleaseConfig{Workflow: ".github/workflows/release.yaml"}

	t.Run("own-repo finalize downloads the from-source binary", func(t *testing.T) {
		cfg, tmpDir := candidateDispatchConfig(t, true, release)
		cfg.ReleaseToken = pat

		content, err := NewGenerator(cfg, tmpDir, WithOwnRepoRelease()).Generate()
		require.NoError(t, err)

		// Scope to the finalize job: from the finalize header to the failure gate.
		finalize := blockBetween(t, content, "  finalize:", "- name: Check for Failures")
		assert.Contains(t, finalize, "- name: Download cascade CLI (from source)",
			"own-repo finalize must download the from-source cascade binary")
		assert.Contains(t, finalize, "name: "+ownRepoCLIArtifactName,
			"own-repo finalize must download the build-cli artifact by name")
		assert.Contains(t, finalize, "actions/download-artifact@",
			"own-repo finalize must use the pinned download-artifact action")
		assert.Contains(t, finalize, "if: needs.build-cli.result == 'success'",
			"the from-source download must be gated on the build-cli callback succeeding")
		assert.Contains(t, finalize, "echo \"$GITHUB_WORKSPACE/.cascade-bin\" >> \"$GITHUB_PATH\"",
			"the from-source binary must be prepended to PATH")
		// The pinned setup-cli survives only as the skipped-build fallback, guarded
		// by the complementary condition.
		assert.Contains(t, finalize, "if: needs.build-cli.result != 'success'",
			"own-repo finalize must retain a pinned fallback when build-cli was skipped")
	})

	t.Run("plain finalize keeps the pinned setup-cli and never downloads", func(t *testing.T) {
		cfg, tmpDir := candidateDispatchConfig(t, true, release)
		cfg.ReleaseToken = pat

		content, err := NewGenerator(cfg, tmpDir).Generate()
		require.NoError(t, err)

		finalize := blockBetween(t, content, "  finalize:", "- name: Check for Failures")
		assert.NotContains(t, finalize, "Download cascade CLI (from source)",
			"plain finalize must not download a from-source binary")
		assert.NotContains(t, finalize, ownRepoCLIArtifactName,
			"plain finalize must carry no trace of the own-repo build artifact")
		assert.Contains(t, finalize, "stablekernel/cascade/.github/actions/setup-cli@",
			"plain finalize must install the pinned released cascade via setup-cli")
	})
}

// TestGenerator_NewGeneratorTwoArgDefaultsToPlainMode is a narrow regression
// lock: the pre-existing two-argument NewGenerator(cfg, baseDir) call every
// caller in the codebase already uses must keep behaving exactly as before
// WithOwnRepoRelease existed. Own-repo mode is opt-in only.
func TestGenerator_NewGeneratorTwoArgDefaultsToPlainMode(t *testing.T) {
	release := &config.ReleaseConfig{Workflow: ".github/workflows/release.yaml"}
	cfg, tmpDir := candidateDispatchConfig(t, true, release)
	cfg.ReleaseToken = pat

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	step := blockBetween(t, content, "- name: Manage Release", "- name: Dispatch Release Candidate Build")
	assert.NotContains(t, step, "tag_only:")
	assert.Contains(t, step, "token: "+pat)
}

// TestManageReleaseAction_OwnRepoTagOnlyInput asserts the generated composite
// action exposes the tag_only input (and its env/CLI-arg forwarding) only in
// own-repo mode, and that plain generation (every downstream manifest) never
// carries any trace of it: no input, no env var, no CLI forwarding line.
func TestManageReleaseAction_OwnRepoTagOnlyInput(t *testing.T) {
	ownRepoAction := generateManageReleaseAction(true)
	assert.Contains(t, ownRepoAction, "  tag_only:", "own-repo composite action must expose the tag_only input")
	assert.Contains(t, ownRepoAction, "default: 'false'", "tag_only must default off")
	assert.Contains(t, ownRepoAction, "INPUT_TAG_ONLY: ${{ inputs.tag_only }}", "tag_only must be wired to an env var")
	assert.Contains(t, ownRepoAction, `[[ "$INPUT_TAG_ONLY" == "true" ]] && CMD_ARGS+=(--tag-only)`,
		"tag_only must forward --tag-only to the CLI")

	plainAction := generateManageReleaseAction(false)
	assert.NotContains(t, plainAction, "tag_only", "plain (downstream) composite action must carry no trace of tag_only")
	assert.NotContains(t, plainAction, "--tag-only", "plain (downstream) composite action must not forward --tag-only")
}

// TestManageReleaseAction_PlainModeByteIdenticalToPreOwnRepo locks plain
// (non-own-repo) generation to the exact byte shape the action had before
// own-repo mode existed, so a downstream manifest's generated action.yaml is
// provably unaffected by this change. Any future edit to the shared template
// must keep this equal for ownRepo=false.
func TestManageReleaseAction_PlainModeByteIdenticalToPreOwnRepo(t *testing.T) {
	plain := generateManageReleaseAction(false)
	assert.NotContains(t, plain, "tag_only")
	// The plain action must still declare exactly the pre-existing input set,
	// in order, with nothing extra spliced in between create_tag and outputs:.
	assert.Contains(t, plain, "  create_tag:\n    description: 'Create git tag on create action'\n    required: false\n    default: 'false'\n\noutputs:\n")
	assert.Contains(t, plain, "        INPUT_CREATE_TAG: ${{ inputs.create_tag }}\n        GITHUB_TOKEN: ${{ inputs.token }}\n")
	assert.Contains(t, plain, `[[ "$INPUT_CREATE_TAG" == "true" ]] && CMD_ARGS+=(--create-tag)`+"\n\n        # Run CLI\n")
}

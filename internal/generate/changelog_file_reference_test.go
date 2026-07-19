package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The built-in release changelog window is bounded by design but can still grow
// large on a big-history environment. Passing that content through a composite
// action input places it on an environment variable, and execve caps a single
// env var near 128KB, so a large changelog fails the step with E2BIG. The
// built-in callers must therefore pass the changelog by file reference: the
// Generate Changelog step writes the content to a fixed path under the runner
// temp dir, and the manage-release step reads that path via changelog_file.
//
// These assertions pin that wiring for every built-in caller: the single-env
// release workflow, the multi-env promote workflow, and the orchestrate
// finalize job. The custom cross-job changelog path is out of scope here and is
// covered by custom_changelog_test.go, which keeps the content input.

const (
	changelogFileInput   = "changelog_file: ${{ runner.temp }}/cascade-changelog.md"
	changelogFileRedirect = `> "$RUNNER_TEMP/cascade-changelog.md"`
	changelogContentInput = "changelog: ${{ steps.changelog.outputs.changelog }}"
	changelogHeredocStart = "changelog<<"
)

// assertBuiltinChangelogByFile asserts a generated workflow passes its built-in
// changelog by file reference and never places the content on an action input.
func assertBuiltinChangelogByFile(t *testing.T, content string) {
	t.Helper()
	assert.Contains(t, content, changelogFileInput,
		"built-in caller must pass the changelog by file path via changelog_file")
	assert.Contains(t, content, changelogFileRedirect,
		"Generate Changelog step must write the changelog to the runner temp file")
	assert.NotContains(t, content, changelogContentInput,
		"built-in caller must not pass changelog content on the action input")
	assert.NotContains(t, content, changelogHeredocStart,
		"Generate Changelog step must not emit the changelog as a heredoc output")
}

// TestReleasePublish_BuiltinChangelogByFileNotContent covers the single-env
// release workflow (promote.yaml), whose create-draft, prerelease, and publish
// steps all consume the built-in changelog.
func TestReleasePublish_BuiltinChangelogByFileNotContent(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
	}

	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assertBuiltinChangelogByFile(t, content)
}

// TestPromote_BuiltinChangelogByFileNotContent covers the multi-env promote
// workflow (promote.yaml), whose update, prerelease, and publish steps all
// consume the built-in changelog.
func TestPromote_BuiltinChangelogByFileNotContent(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "staging", "prod"),
	}

	content, err := NewPromoteGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assertBuiltinChangelogByFile(t, content)
}

// TestOrchestrateFinalize_BuiltinChangelogByFileNotContent covers the
// orchestrate workflow finalize job, whose Manage Release step consumes the
// built-in changelog produced by the in-job Generate Changelog step.
func TestOrchestrateFinalize_BuiltinChangelogByFileNotContent(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/build.yaml"),
		[]byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	assertBuiltinChangelogByFile(t, content)
}

// TestManageReleaseAction_SupportsChangelogFileInput pins the composite action
// contract in both the own-repo and standard variants: it declares a
// changelog_file input, threads it to INPUT_CHANGELOG_FILE, prefers a
// caller-provided file, passes the resolved path to --changelog-file, and only
// removes a temp file it created itself, never a caller-provided file.
func TestManageReleaseAction_SupportsChangelogFileInput(t *testing.T) {
	for _, ownRepo := range []bool{false, true} {
		ownRepo := ownRepo
		name := "standard"
		if ownRepo {
			name = "own-repo"
		}
		t.Run(name, func(t *testing.T) {
			action := generateManageReleaseAction(ownRepo)

			assert.Contains(t, action, "changelog_file:",
				"action must declare a changelog_file input")
			assert.Contains(t, action, "INPUT_CHANGELOG_FILE: ${{ inputs.changelog_file }}",
				"action must thread changelog_file to INPUT_CHANGELOG_FILE")
			assert.Contains(t, action, `if [[ -n "$INPUT_CHANGELOG_FILE" ]]; then`,
				"action must prefer a caller-provided changelog file")
			assert.Contains(t, action, `--changelog-file "$CHANGELOG_FILE"`,
				"action must pass the resolved changelog path to the CLI")
			assert.Contains(t, action, `[[ -n "${CHANGELOG_TEMP:-}" ]] && rm -f "$CHANGELOG_TEMP"`,
				"action must only remove a temp file it created, never a caller file")
			assert.NotContains(t, action, `rm -f "$CHANGELOG_FILE"`,
				"action must not unconditionally delete the changelog file")
		})
	}
}

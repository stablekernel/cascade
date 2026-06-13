package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// changelogReusableStub is a minimal valid workflow_call reusable workflow that
// declares exactly the inputs the generator threads to a custom changelog
// workflow (changelog_base_sha, head_sha, repo) and the changelog output the
// finalize/release step consumes via needs.changelog.outputs.changelog. This
// lets actionlint resolve the caller job's with: block and the downstream
// needs.changelog.outputs.changelog reference under full strictness.
const changelogReusableStub = `name: Stub Changelog
on:
  workflow_call:
    inputs:
      changelog_base_sha:
        required: false
        type: string
      head_sha:
        required: false
        type: string
      repo:
        required: false
        type: string
    outputs:
      changelog:
        description: Generated changelog markdown
        value: ${{ jobs.changelog.outputs.changelog }}
jobs:
  changelog:
    runs-on: ubuntu-latest
    outputs:
      changelog: ${{ steps.gen.outputs.changelog }}
    steps:
      - id: gen
        run: echo "changelog=stub" >> "$GITHUB_OUTPUT"
`

// callbackReusableStub is a permissive workflow_call target for the build/deploy
// callbacks referenced by the generated orchestrate workflow, declaring the
// inputs cascade threads to callbacks so actionlint does not flag them.
const callbackReusableStub = `name: Stub Callback
on:
  workflow_call:
    inputs:
      environment:
        required: false
        type: string
      sha:
        required: false
        type: string
      target_env:
        required: false
        type: string
      dry_run:
        required: false
        type: string
    outputs:
      image_tag:
        value: stub
jobs:
  stub:
    runs-on: ubuntu-latest
    outputs:
      image_tag: stub
    steps:
      - run: 'true'
`

// writeCustomChangelogStubs writes changelogReusableStub at every local
// reusable-workflow reference (uses: ./...) found in the generated content and a
// minimal build stub for any other local reusable-workflow call, so actionlint
// can resolve each call site honestly.
func writeCustomChangelogStubs(t *testing.T, root, content string) {
	t.Helper()

	const marker = "uses: ./"
	seen := make(map[string]struct{})
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		idx := strings.Index(trimmed, marker)
		if idx < 0 {
			continue
		}
		ref := strings.Fields(trimmed[idx+len("uses: "):])[0]
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		rel := strings.TrimPrefix(ref, "./")
		stubPath := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(stubPath), 0755))

		stub := changelogReusableStub
		if !strings.Contains(rel, "custom-changelog") {
			// Any other local reusable workflow (e.g. the build callback) must
			// declare the inputs the generator threads to it so actionlint
			// resolves the caller with: block honestly.
			stub = callbackReusableStub
		}
		require.NoError(t, os.WriteFile(stubPath, []byte(stub), 0644))
	}
}

// TestCustomChangelog_Actionlint generates the orchestrate workflow for a
// custom-changelog repo and runs actionlint over it. It proves both F7 hazards
// are gone: 7a (a reusable workflow emitted as a step uses: is rejected at parse
// time) and 7b (an input value referencing a non-existent setup output). The
// custom changelog is now a job-level uses: with inputs keyed to the setup
// job's real outputs, so actionlint reports no issues. Skipped when actionlint
// is not installed so the suite stays hermetic.
func TestCustomChangelog_Actionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	tmpDir := t.TempDir()
	wfDir := filepath.Join(tmpDir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	// The generator reads the build and changelog reusable workflows to
	// discover their inputs/outputs at generation time.
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "build.yaml"),
		[]byte("on:\n  workflow_call:\n    outputs:\n      image_tag:\n        value: stub\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "custom-changelog.yaml"),
		[]byte(changelogReusableStub), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Changelog:    &config.ChangelogConfig{Workflow: ".github/workflows/custom-changelog.yaml", Contributors: true},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	// Run actionlint against the generated workflow in an isolated git repo so
	// local reusable-workflow refs (uses: ./...) resolve against the repo root.
	dir := t.TempDir()
	lintDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(lintDir, 0755))
	wfPath := filepath.Join(lintDir, "orchestrate.yaml")
	require.NoError(t, os.WriteFile(wfPath, []byte(content), 0644))

	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")

	writeCustomChangelogStubs(t, dir, content)

	// Disable shellcheck: inline run: bodies trip style nits orthogonal to F7.
	cmd := exec.Command(bin, "-shellcheck=", wfPath)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues for the custom-changelog workflow:\n%s", string(out))
}

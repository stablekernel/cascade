package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dryRunFinalizeConfig builds a minimal framework-managed orchestrate config
// (no external release) whose finalize job emits the Manage Release and Update
// Manifest steps, so a test can assert both mutations are gated on the dry_run
// dispatch input.
func dryRunFinalizeConfig(t *testing.T) (*config.TrunkConfig, string) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".github/workflows/build.yaml"),
		[]byte("name: build\non:\n  workflow_call:\n"), 0644))
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
	}
	return cfg, tmpDir
}

// TestManageReleaseAction_ThreadsDryRunFlag proves the generated manage-release
// composite action declares a dry_run input and forwards it to the CLI as
// --dry-run. Without this the finalize "Manage Release" step invokes
// `cascade manage-release` with no --dry-run flag, so a dry-run orchestrate
// dispatch cuts a real tag and creates a real release even though the CLI's own
// printDryRunPlan gate (internal/release/command.go) was built to prevent
// exactly that. Both the plain (downstream) and own-repo variants must thread
// it, since framework-managed downstream releases are the ones that mutate.
func TestManageReleaseAction_ThreadsDryRunFlag(t *testing.T) {
	for _, ownRepo := range []bool{false, true} {
		mode := "plain"
		if ownRepo {
			mode = "own-repo"
		}
		t.Run(mode, func(t *testing.T) {
			action := generateManageReleaseAction(ownRepo)

			assert.Contains(t, action, "  dry_run:\n    description:",
				"the action must declare a dry_run input")
			assert.Contains(t, action, "INPUT_DRY_RUN: ${{ inputs.dry_run }}",
				"the dry_run input must be wired to an env var")
			assert.Contains(t, action, `[[ "$INPUT_DRY_RUN" == "true" ]] && CMD_ARGS+=(--dry-run)`,
				"a true dry_run must append --dry-run so the CLI previews instead of mutating")
		})
	}
}

// TestWriteReleaseStep_ForwardsDryRunDispatchInput proves the finalize Manage
// Release step passes the orchestrate dry_run dispatch input to the composite
// action, coerced to a real boolean via the null-safe github.event.inputs
// accessor (orchestrate also runs on push/schedule/workflow_run, where the
// inputs context is null). Removing the passthrough leaves the release step
// unconditionally mutating on a dry-run rehearsal.
func TestWriteReleaseStep_ForwardsDryRunDispatchInput(t *testing.T) {
	cfg, tmpDir := dryRunFinalizeConfig(t)

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	step := findStep(t, content, "Manage Release")
	with, ok := step["with"].(map[string]interface{})
	require.True(t, ok, "Manage Release step has no with: block")
	assert.Equal(t, "${{ github.event.inputs.dry_run == 'true' }}", with["dry_run"],
		"the Manage Release step must forward the dry_run dispatch input, coerced to a boolean")
}

// TestWriteManifestUpdateStep_GatesMutationOnDryRun proves the finalize Update
// Manifest step previews and exits before committing state when dry_run is set,
// rather than committing real state to trunk on a dry-run rehearsal. The gate
// must sit BEFORE the state commit/push loop, or the mutation still happens.
func TestWriteManifestUpdateStep_GatesMutationOnDryRun(t *testing.T) {
	cfg, tmpDir := dryRunFinalizeConfig(t)

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	block := updateManifestStep(t, content)
	assert.Contains(t, block, "DRY_RUN: ${{ github.event.inputs.dry_run == 'true' }}",
		"the Update Manifest step must derive DRY_RUN from the dispatch input")

	body := stepRunBody(t, content, "Update Manifest")
	assert.Contains(t, body, `if [[ "$DRY_RUN" == "true" ]]; then`,
		"the mutating portion must be gated on DRY_RUN")
	assert.Contains(t, body, "cascade-state-write: dry-run preview (no commit)",
		"a dry-run must print a runtime preview marker the e2e harness can assert on")

	gateIdx := strings.Index(body, `if [[ "$DRY_RUN" == "true" ]]; then`)
	pushIdx := strings.Index(body, "cascade-state-write: attempt=")
	require.GreaterOrEqual(t, gateIdx, 0)
	require.GreaterOrEqual(t, pushIdx, 0)
	assert.Less(t, gateIdx, pushIdx,
		"the dry-run gate must precede the state commit/push loop so the push never runs on a rehearsal")
}

// TestWriteCandidateDispatchStep_GatedOnDryRun proves the finalize Dispatch
// Release Candidate Build step, which fires an external release workflow run,
// is suppressed on a dry-run rehearsal. The step never runs under the e2e act
// harness (its if: pins github.com), so this generation-correctness assertion
// is its executing-proof ceiling; the fleet exercises the live skip.
func TestWriteCandidateDispatchStep_GatedOnDryRun(t *testing.T) {
	cfg, tmpDir := candidateDispatchConfig(t, true, &config.ReleaseBuildConfig{Workflow: "release.yaml"})

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)

	step := findStep(t, content, "Dispatch Release Candidate Build")
	cond, ok := step["if"].(string)
	require.True(t, ok, "Dispatch Release Candidate Build step has no if: condition")
	assert.Contains(t, cond, "github.event.inputs.dry_run != 'true'",
		"the candidate dispatch must be suppressed on a dry-run rehearsal")
}

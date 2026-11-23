package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeBuildStubRequiredInputs writes a build callback stub whose workflow_call
// inputs are declared with the caller-chosen required flag (required: true
// entries carry no default, so ParseWorkflowRequiredInputs counts them). The
// input order is sorted for a deterministic stub.
func writeBuildStubRequiredInputs(t *testing.T, required map[string]bool) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755))
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	var inputs strings.Builder
	for _, name := range names {
		if required[name] {
			fmt.Fprintf(&inputs, "      %s:\n        required: true\n        type: string\n", name)
		} else {
			fmt.Fprintf(&inputs, "      %s:\n        required: false\n        type: string\n", name)
		}
	}
	stub := fmt.Sprintf(`name: Build
on:
  workflow_call:
    inputs:
%s
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: 'true'
`, inputs.String())
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "workflows", "build.yaml"), []byte(stub), 0o644))
	return dir
}

// requiredInputManifestConfig returns a builds-only manifest with one build
// whose callback workflow is the on-disk stub and whose manifest inputs are
// the given map.
func requiredInputManifestConfig(inputs map[string]interface{}) *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "test", "prod"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Inputs: inputs},
		},
	}
}

// TestGenerator_ValidateRequiredInputs_CountsManifestInputs guards the
// providability rules for required callback inputs: a required input the
// manifest passes under the build's inputs: counts as providable (the
// generator threads it through the callback's with: block), a required input
// that is neither standard nor manifest-passed still fails with the same
// message, and a required input colliding with a standard name behaves as
// before (the standard wiring satisfies it and is not duplicated).
func TestGenerator_ValidateRequiredInputs_CountsManifestInputs(t *testing.T) {
	t.Run("required_operator_input_passed_by_manifest_passes", func(t *testing.T) {
		// The callback requires both the standard environment and an operator
		// input the manifest passes; generation must succeed and thread it.
		dir := writeBuildStubRequiredInputs(t, map[string]bool{
			"environment":     true,
			"release_channel": true,
		})
		cfg := requiredInputManifestConfig(map[string]interface{}{
			"release_channel": "canary",
		})

		content, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err,
			"a required input the manifest passes under the build's inputs: is providable and must not fail generation")

		buildJob := extractJobSection(t, content, "build-app:")
		require.NotEmpty(t, buildJob, "build job section should be present")
		assert.Contains(t, buildJob, "      release_channel: 'canary'",
			"the manifest-passed input must actually be threaded to the callback")
	})

	t.Run("required_input_neither_passed_nor_standard_fails_with_same_message", func(t *testing.T) {
		dir := writeBuildStubRequiredInputs(t, map[string]bool{
			"environment": true,
			"flavor":      true,
		})
		// The manifest passes an unrelated input; flavor is neither standard
		// nor manifest-passed, so validation must fail exactly as before.
		cfg := requiredInputManifestConfig(map[string]interface{}{
			"release_channel": "canary",
		})

		_, err := NewGenerator(cfg, dir).Generate()
		require.Error(t, err)
		assert.Equal(t,
			"required input validation failed:\n  - Build (app) requires input 'flavor' but it cannot be provided (not a standard input and no dependency outputs it)",
			err.Error(),
			"a required input that is neither standard nor manifest-passed must fail with the unchanged message")
	})

	t.Run("required_input_colliding_with_standard_name_behaves_as_before", func(t *testing.T) {
		// sha is a standard input the generator always wires when the callee
		// declares it, so a required sha passes with or without a manifest
		// input of the same name, and the manifest value must not duplicate or
		// override the standard wiring.
		dir := writeBuildStubRequiredInputs(t, map[string]bool{
			"environment": true,
			"sha":         true,
		})
		cfg := requiredInputManifestConfig(map[string]interface{}{
			"sha": "operators-own-sha",
		})

		content, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err,
			"a required input colliding with a standard name is satisfied by the standard wiring, as before")

		buildJob := extractJobSection(t, content, "build-app:")
		require.NotEmpty(t, buildJob, "build job section should be present")
		assert.Contains(t, buildJob, "      sha: ${{ needs.setup.outputs.head_sha }}",
			"the standard sha wiring must satisfy the required input")
		assert.NotContains(t, buildJob, "operators-own-sha",
			"a manifest input sharing a standard input's name must not duplicate it")
		assert.Equal(t, 1, strings.Count(buildJob, "      sha:"),
			"the sha input must appear exactly once in the with: block")
	})
}

// TestGenerator_ValidateRequiredInputs_RejectsMatrixKeyedManifestInputs guards
// the matrix-keyed exclusion in the manifest-input counting: a required input
// whose manifest value references the matrix context still fails generation
// with the unchanged message (main's behavior, kept; #683 tracks lifting it),
// while a plain literal that merely contains the text "matrix" counts as
// providable exactly like any other manifest-passed input.
func TestGenerator_ValidateRequiredInputs_RejectsMatrixKeyedManifestInputs(t *testing.T) {
	t.Run("required_matrix_keyed_manifest_input_fails_with_same_message", func(t *testing.T) {
		// The manifest passes the required image keyed on the matrix context;
		// validation must reject it exactly as main does, with the unchanged
		// message, rather than counting the matrix-keyed value as providable.
		dir := writeBuildStubRequiredInputs(t, map[string]bool{
			"environment": true,
			"image":       true,
		})
		cfg := requiredInputManifestConfig(map[string]interface{}{
			"image": "img-${{ matrix.os }}",
		})

		_, err := NewGenerator(cfg, dir).Generate()
		require.Error(t, err,
			"a required input satisfied only by a matrix-keyed manifest value must fail generation, as on main")
		assert.Equal(t,
			"required input validation failed:\n  - Build (app) requires input 'image' but it cannot be provided (not a standard input and no dependency outputs it)",
			err.Error(),
			"the matrix-keyed exclusion must fail with the unchanged message")
	})

	t.Run("required_input_from_literal_containing_matrix_text_passes", func(t *testing.T) {
		// The manifest passes a plain literal that merely contains the text
		// "matrix"; it references no matrix context, so it counts as providable
		// and threads like any other manifest-passed input.
		dir := writeBuildStubRequiredInputs(t, map[string]bool{
			"environment": true,
			"image":       true,
		})
		cfg := requiredInputManifestConfig(map[string]interface{}{
			"image": "ci/matrix.yaml",
		})

		content, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err,
			"a manifest-passed literal that merely contains the text 'matrix' must not be mistaken for a matrix-keyed value")

		buildJob := extractJobSection(t, content, "build-app:")
		require.NotEmpty(t, buildJob, "build job section should be present")
		assert.Contains(t, buildJob, "      image: 'ci/matrix.yaml'",
			"the literal manifest-passed input must actually be threaded to the callback")
	})
}

// TestGenerator_ValidateRequiredInputs_RejectsNonContextNeedsManifestInputs
// guards the needs exclusion in the manifest-input counting: a required input
// whose manifest value references `needs.<job>.*` other than `needs.context.*`
// still fails generation with the unchanged message, mirroring the hotfix
// lane's drop of the same references (the hotfix build job's only need is
// `context`, so its with: block never carries the input and the dispatch
// would fail at run time for the missing required input), while a
// `needs.context.*` value and a plain literal that merely contains the text
// count as providable exactly like any other manifest-passed input.
func TestGenerator_ValidateRequiredInputs_RejectsNonContextNeedsManifestInputs(t *testing.T) {
	t.Run("required_non_context_needs_manifest_input_fails_with_same_message", func(t *testing.T) {
		// The manifest passes the required upstream input as a needs.setup
		// reference; validation must reject it with the unchanged message,
		// because the hotfix build job (needs: context only) leaves the
		// reference out of its with: block.
		dir := writeBuildStubRequiredInputs(t, map[string]bool{
			"environment": true,
			"upstream":    true,
		})
		cfg := requiredInputManifestConfig(map[string]interface{}{
			"upstream": "${{ needs.setup.outputs.head_sha }}",
		})

		_, err := NewGenerator(cfg, dir).Generate()
		require.Error(t, err,
			"a required input satisfied only by a non-context needs manifest value must fail generation")
		assert.Equal(t,
			"required input validation failed:\n  - Build (app) requires input 'upstream' but it cannot be provided (not a standard input and no dependency outputs it)",
			err.Error(),
			"the needs exclusion must fail with the unchanged message")
	})

	t.Run("required_needs_context_manifest_input_counts", func(t *testing.T) {
		// `needs.context.*` threads in the hotfix build job (context is its
		// only need), so it counts as providable and threads like any other
		// manifest-passed input.
		dir := writeBuildStubRequiredInputs(t, map[string]bool{
			"environment": true,
			"fix":         true,
		})
		cfg := requiredInputManifestConfig(map[string]interface{}{
			"fix": "${{ needs.context.outputs.fix_sha }}",
		})

		content, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err,
			"a required input passed as a needs.context manifest value is providable and must not fail generation")

		buildJob := extractJobSection(t, content, "build-app:")
		require.NotEmpty(t, buildJob, "build job section should be present")
		assert.Contains(t, buildJob, "      fix: '${{ needs.context.outputs.fix_sha }}'",
			"the needs.context manifest-passed input must actually be threaded to the callback")
	})

	t.Run("required_input_from_literal_containing_needs_text_passes", func(t *testing.T) {
		// The manifest passes a plain literal that merely contains the text
		// "needs.setup"; it references no needs context, so it counts as
		// providable and threads like any other manifest-passed input.
		dir := writeBuildStubRequiredInputs(t, map[string]bool{
			"environment": true,
			"upstream":    true,
		})
		cfg := requiredInputManifestConfig(map[string]interface{}{
			"upstream": "needs.setup",
		})

		content, err := NewGenerator(cfg, dir).Generate()
		require.NoError(t, err,
			"a manifest-passed literal that merely contains the text 'needs.setup' must not be mistaken for a needs reference")

		buildJob := extractJobSection(t, content, "build-app:")
		require.NotEmpty(t, buildJob, "build job section should be present")
		assert.Contains(t, buildJob, "      upstream: 'needs.setup'",
			"the literal manifest-passed input must actually be threaded to the callback")
	})
}

// TestHotfixGenerator_RequiredManifestBuildInputGenerates guards the hotfix
// lane for the same manifest: a build callback that requires an operator input
// the manifest passes must generate (the hotfix build job has no required-input
// validator, and it threads the manifest input through the shared
// renderOperatorInputs rules).
func TestHotfixGenerator_RequiredManifestBuildInputGenerates(t *testing.T) {
	dir := writeBuildStubRequiredInputs(t, map[string]bool{
		"environment":     true,
		"sha":             false,
		"release_channel": true,
	})
	cfg := requiredInputManifestConfig(map[string]interface{}{
		"release_channel": "canary",
	})

	content, err := NewHotfixGenerator(cfg, dir).Generate()
	require.NoError(t, err,
		"the hotfix lane must generate for the same manifest the orchestrate generator accepts")

	buildJob := extractJobSection(t, content, "build-app:")
	require.NotEmpty(t, buildJob, "hotfix build job section should be present")
	assert.Contains(t, buildJob, "      release_channel: 'canary'",
		"the hotfix build job must thread the manifest-passed input to the callback")
}

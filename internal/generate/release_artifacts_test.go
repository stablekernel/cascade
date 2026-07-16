package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// releaseArtifactsConfig builds a minimal single-build manifest whose build
// declares the given release artifacts, backed by a real reusable workflow
// file so generation succeeds.
func releaseArtifactsConfig(t *testing.T, artifacts []config.ArtifactConfig) (*config.TrunkConfig, string) {
	t.Helper()
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{
				Name:      "app",
				Workflow:  ".github/workflows/build.yaml",
				Triggers:  []string{"src/**"},
				Artifacts: artifacts,
			},
		},
	}
	return cfg, tmpDir
}

// uploadStepBlock slices out the Upload Release Artifacts step body (up to the
// next step) so assertions about its shell cannot match unrelated steps.
func uploadStepBlock(t *testing.T, result string) string {
	t.Helper()
	start := strings.Index(result, "- name: Upload Release Artifacts")
	require.NotEqual(t, -1, start, "expected an Upload Release Artifacts step")
	rest := result[start+1:]
	end := strings.Index(rest, "- name: ")
	if end == -1 {
		return result[start:]
	}
	return result[start : start+1+end]
}

// TestGenerator_ReleaseArtifacts_DownloadAndUploadSteps asserts that a build
// declaring release artifacts gets a Download Release Artifacts step (before
// the release is managed) and an Upload Release Artifacts step (after), with
// the documented download pattern and the gh release upload command targeting
// the artifact path under release-artifacts/.
func TestGenerator_ReleaseArtifacts_DownloadAndUploadSteps(t *testing.T) {
	cfg, tmpDir := releaseArtifactsConfig(t, []config.ArtifactConfig{
		{Name: "binaries", Path: "dist/*.tar.gz"},
	})

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	// Download step: merge every release-* artifact into release-artifacts/.
	assert.Contains(t, result, "- name: Download Release Artifacts",
		"release artifacts must emit a download step")
	assert.Contains(t, result, "pattern: release-*",
		"download step must match the release-* artifact naming convention")
	assert.Contains(t, result, "path: release-artifacts",
		"download step must land artifacts in release-artifacts/")
	assert.Contains(t, result, "merge-multiple: true",
		"download step must merge multiple artifacts into one directory")

	// Upload step: gh release upload against the candidate tag.
	assert.Contains(t, result, "- name: Upload Release Artifacts",
		"release artifacts must emit an upload step")
	assert.Contains(t, result, "TAG=\"${{ needs.setup.outputs.version }}\"",
		"upload step must target the candidate version tag")
	assert.Contains(t, result, "if ls release-artifacts/dist/*.tar.gz 2>/dev/null; then",
		"upload step must probe the artifact path before uploading")
	assert.Contains(t, result, "gh release upload \"$TAG\" release-artifacts/dist/*.tar.gz --clobber",
		"upload step must upload the artifact path with --clobber")

	// Ordering: download before the release is managed, upload after.
	download := strings.Index(result, "- name: Download Release Artifacts")
	release := strings.Index(result, "- name: Manage Release")
	upload := strings.Index(result, "- name: Upload Release Artifacts")
	require.NotEqual(t, -1, download)
	require.NotEqual(t, -1, release)
	require.NotEqual(t, -1, upload)
	assert.Less(t, download, release, "download step must precede Manage Release")
	assert.Less(t, release, upload, "upload step must follow Manage Release")
}

// TestGenerator_ReleaseArtifacts_OmittedRequiredDefaultsToRequired asserts the
// documented default: an artifact that does not set required: is required, so
// the emitted shell fails the release (exit 1) when it is missing instead of
// downgrading to a warning.
func TestGenerator_ReleaseArtifacts_OmittedRequiredDefaultsToRequired(t *testing.T) {
	cfg, tmpDir := releaseArtifactsConfig(t, []config.ArtifactConfig{
		{Name: "binaries", Path: "dist/*.tar.gz"},
	})

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	upload := uploadStepBlock(t, result)
	assert.Contains(t, upload, "echo \"::error::Required artifact 'release-app-binaries' not found\"",
		"omitted required must default to required and emit the error gate")
	assert.Contains(t, upload, "exit 1",
		"missing required artifact must fail the job")
	assert.NotContains(t, upload, "::warning::Optional artifact 'release-app-binaries' not found",
		"omitted required must not be treated as optional")
}

// TestGenerator_ReleaseArtifacts_OptionalArtifactWarnsInsteadOfFailing asserts
// that required: false downgrades a missing artifact to a warning and emits no
// failure gate for it.
func TestGenerator_ReleaseArtifacts_OptionalArtifactWarnsInsteadOfFailing(t *testing.T) {
	cfg, tmpDir := releaseArtifactsConfig(t, []config.ArtifactConfig{
		{Name: "checksums", Path: "dist/checksums.txt", Required: boolPtr(false)},
	})

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	upload := uploadStepBlock(t, result)
	assert.Contains(t, upload, "echo \"::warning::Optional artifact 'release-app-checksums' not found\"",
		"required: false must downgrade a missing artifact to a warning")
	assert.NotContains(t, upload, "::error::Required artifact 'release-app-checksums' not found",
		"optional artifact must not emit the required-artifact error")
	assert.NotContains(t, upload, "exit 1",
		"an upload step with only optional artifacts must not emit a failure gate")
}

// TestGenerator_ReleaseArtifacts_MixedRequiredAndOptional asserts a build with
// one required and one optional artifact emits both upload commands with the
// correct per-artifact gating.
func TestGenerator_ReleaseArtifacts_MixedRequiredAndOptional(t *testing.T) {
	cfg, tmpDir := releaseArtifactsConfig(t, []config.ArtifactConfig{
		{Name: "binaries", Path: "dist/*.tar.gz", Required: boolPtr(true)},
		{Name: "checksums", Path: "dist/checksums.txt", Required: boolPtr(false)},
	})

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, result, "gh release upload \"$TAG\" release-artifacts/dist/*.tar.gz --clobber")
	assert.Contains(t, result, "gh release upload \"$TAG\" release-artifacts/dist/checksums.txt --clobber")
	assert.Contains(t, result, "::error::Required artifact 'release-app-binaries' not found")
	assert.Contains(t, result, "::warning::Optional artifact 'release-app-checksums' not found")
}

// TestGenerator_NoReleaseArtifacts_NoArtifactSteps asserts a build without a
// release artifacts list emits neither the download nor the upload step.
func TestGenerator_NoReleaseArtifacts_NoArtifactSteps(t *testing.T) {
	cfg, tmpDir := releaseArtifactsConfig(t, nil)

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	assert.NotContains(t, result, "- name: Download Release Artifacts",
		"no release artifacts: must not emit a download step")
	assert.NotContains(t, result, "- name: Upload Release Artifacts",
		"no release artifacts: must not emit an upload step")
	assert.NotContains(t, result, "gh release upload",
		"no release artifacts: must not emit upload commands")
}

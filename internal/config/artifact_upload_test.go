package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestArtifactUpload_ParsesOnBuildAndDeploy asserts that the renamed
// artifact_upload: key populates the passthrough block on both build and deploy
// callbacks, and that a manifest using it validates clean.
func TestArtifactUpload_ParsesOnBuildAndDeploy(t *testing.T) {
	y := `ci:
  config:
    schema_version: 1
    environments: [dev]
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
        triggers: [dev]
        artifact_upload:
          upload: dist/
    deploys:
      - name: dep
        workflow: .github/workflows/deploy.yaml
        triggers: [dev]
        artifact_upload:
          downloads: [app]
`
	file, err := ParseManifestBytes([]byte(y), "ci")
	require.NoError(t, err)
	require.NotNil(t, file.Config)

	require.Len(t, file.Config.Builds, 1)
	require.NotNil(t, file.Config.Builds[0].PassthroughArtifact, "artifact_upload must populate the build passthrough block")
	assert.Equal(t, "dist/", file.Config.Builds[0].PassthroughArtifact.Upload)

	require.Len(t, file.Config.Deploys, 1)
	require.NotNil(t, file.Config.Deploys[0].PassthroughArtifact, "artifact_upload must populate the deploy passthrough block")
	assert.Equal(t, []string{"app"}, file.Config.Deploys[0].PassthroughArtifact.Downloads)

	assert.Empty(t, Validate(file.Config), "artifact_upload manifest must validate clean")
}

// TestArtifactUpload_OldArtifactKeyRejected asserts that the former singular
// artifact: key is no longer modeled: it lands in the callback Extra map and is
// rejected as an unknown field with a did-you-mean pointing at artifact_upload.
func TestArtifactUpload_OldArtifactKeyRejected(t *testing.T) {
	for _, section := range []string{"builds", "deploys"} {
		y := `ci:
  config:
    schema_version: 1
    environments: [dev]
    ` + section + `:
      - name: app
        workflow: .github/workflows/wf.yaml
        triggers: [dev]
        artifact:
          upload: dist/
`
		file, err := ParseManifestBytes([]byte(y), "ci")
		require.NoError(t, err, section)

		errs := Validate(file.Config)
		joined := strings.Join(errs, "\n")
		assert.Contains(t, joined, `unknown field "artifact"`, "old artifact key must be rejected on %s: %v", section, errs)
		assert.Contains(t, joined, `did you mean "artifact_upload"`, "rejection must suggest artifact_upload on %s: %v", section, errs)
	}
}

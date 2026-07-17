package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidate_ReleaseArtifacts covers the release-artifact grammar: names are
// spliced into the emitted upload shell inside double quotes and paths are
// spliced unquoted (so the documented glob can expand), so both must stay
// within a conservative character set or a manifest value could break or
// extend the generated script.
func TestValidate_ReleaseArtifacts(t *testing.T) {
	base := func(artifacts []ArtifactConfig) TrunkConfig {
		return TrunkConfig{
			TrunkBranch:  "main",
			Environments: EnvNames("dev"),
			Builds: []BuildConfig{
				{Name: "app", Workflow: "w.yaml", Artifacts: artifacts},
			},
		}
	}

	tests := []struct {
		name      string
		artifacts []ArtifactConfig
		wantErrs  []string
	}{
		{
			name:      "valid glob path",
			artifacts: []ArtifactConfig{{Name: "linux-amd64", Path: "dist/*.tar.gz"}},
			wantErrs:  nil,
		},
		{
			name:      "valid dotted name and character-class glob",
			artifacts: []ArtifactConfig{{Name: "checksums.txt", Path: "dist/app-[0-9]?.zip"}},
			wantErrs:  nil,
		},
		{
			name:      "missing name",
			artifacts: []ArtifactConfig{{Path: "dist/*.tar.gz"}},
			wantErrs:  []string{"builds[0].artifacts[0].name is required"},
		},
		{
			name:      "missing path",
			artifacts: []ArtifactConfig{{Name: "binaries"}},
			wantErrs:  []string{"builds[0].artifacts[0].path is required"},
		},
		{
			name:      "shell metacharacters in name",
			artifacts: []ArtifactConfig{{Name: "bin$(id)", Path: "dist/*.tar.gz"}},
			wantErrs:  []string{`builds[0].artifacts[0].name "bin$(id)" must contain only letters, digits, dots, hyphens, and underscores`},
		},
		{
			name:      "space in path",
			artifacts: []ArtifactConfig{{Name: "binaries", Path: "dist/my app/*.tar.gz"}},
			wantErrs:  []string{`builds[0].artifacts[0].path "dist/my app/*.tar.gz" must contain only letters, digits, dots, slashes, hyphens, underscores, and glob characters`},
		},
		{
			name:      "command separator in path",
			artifacts: []ArtifactConfig{{Name: "binaries", Path: "dist/*.tar.gz;id"}},
			wantErrs:  []string{`builds[0].artifacts[0].path "dist/*.tar.gz;id" must contain only letters, digits, dots, slashes, hyphens, underscores, and glob characters`},
		},
		{
			name:      "path starting with a hyphen reads as a flag",
			artifacts: []ArtifactConfig{{Name: "binaries", Path: "--flag"}},
			wantErrs:  []string{`builds[0].artifacts[0].path "--flag" must not start with a hyphen`},
		},
		{
			name: "second artifact reported with its own index",
			artifacts: []ArtifactConfig{
				{Name: "binaries", Path: "dist/*.tar.gz"},
				{Name: "bad`name`", Path: "dist/checksums.txt"},
			},
			wantErrs: []string{"builds[0].artifacts[1].name \"bad`name`\" must contain only letters, digits, dots, hyphens, and underscores"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base(tt.artifacts)
			errs := Validate(&cfg)
			if tt.wantErrs == nil {
				assert.Empty(t, errs, "expected no validation errors")
				return
			}
			for _, want := range tt.wantErrs {
				assert.Contains(t, errs, want)
			}
		})
	}
}

// TestArtifactConfig_IsRequired proves the nil-safe default: an omitted
// required: means the artifact is required, matching the schema's documented
// default, and only an explicit required: false makes it optional.
func TestArtifactConfig_IsRequired(t *testing.T) {
	tr := true
	fa := false

	assert.True(t, ArtifactConfig{Name: "a", Path: "p"}.IsRequired(), "omitted required must default to true")
	assert.True(t, ArtifactConfig{Name: "a", Path: "p", Required: &tr}.IsRequired())
	assert.False(t, ArtifactConfig{Name: "a", Path: "p", Required: &fa}.IsRequired())
}

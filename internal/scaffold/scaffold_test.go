package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

const wantSchemaDirective = "# yaml-language-server: $schema=https://stablekernel.github.io/cascade/manifest.schema.json"

// writeFiles materializes a scaffold output map under dir, creating any parent
// directories, and returns the absolute path to the manifest.
func writeFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
	}
	return filepath.Join(dir, config.DefaultManifestFile)
}

// topologyCases enumerates each preset and its expected ordered env list.
func topologyCases() []struct {
	name string
	envs []string
} {
	return []struct {
		name string
		envs []string
	}{
		{"no-env", []string{}},
		{"two-env", []string{"dev", "prod"}},
		{"three-env", []string{"dev", "staging", "prod"}},
		{"four-env", []string{"dev", "test", "uat", "prod"}},
	}
}

func TestTopologies_PresetEnvLists(t *testing.T) {
	got := Topologies()
	require.Equal(t, []string{}, got["no-env"])
	require.Equal(t, []string{"dev", "prod"}, got["two-env"])
	require.Equal(t, []string{"dev", "staging", "prod"}, got["three-env"])
	require.Equal(t, []string{"dev", "test", "uat", "prod"}, got["four-env"])
}

func TestScaffold_ExpectedFileSet(t *testing.T) {
	for _, tc := range topologyCases() {
		t.Run(tc.name, func(t *testing.T) {
			files, err := Scaffold("acme", "main", tc.envs)
			require.NoError(t, err)

			_, hasManifest := files[config.DefaultManifestFile]
			assert.True(t, hasManifest, "manifest always present")
			_, hasBuild := files[".github/workflows/build.yaml"]
			assert.True(t, hasBuild, "build stub always present")

			_, hasDeploy := files[".github/workflows/deploy.yaml"]
			if len(tc.envs) > 0 {
				assert.True(t, hasDeploy, "deploy stub present when envs non-empty")
				assert.Len(t, files, 3)
			} else {
				assert.False(t, hasDeploy, "no deploy stub for release-only")
				assert.Len(t, files, 2)
			}
		})
	}
}

func TestScaffold_ManifestParsesValidatesAndGenerates(t *testing.T) {
	for _, tc := range topologyCases() {
		t.Run(tc.name, func(t *testing.T) {
			files, err := Scaffold("acme", "main", tc.envs)
			require.NoError(t, err)

			dir := t.TempDir()
			manifestPath := writeFiles(t, dir, files)

			parsed, err := config.ParseManifestFile(manifestPath, config.DefaultManifestKey)
			require.NoError(t, err)
			require.NotNil(t, parsed.Config)

			problems := config.Validate(parsed.Config)
			require.Empty(t, problems, "validation problems: %v", problems)

			// Positional env ordering preserved. A release-only manifest omits
			// the environments key, so the parsed slice is nil there.
			if len(tc.envs) > 0 {
				require.Equal(t, tc.envs, parsed.Config.Environments)
			} else {
				require.Empty(t, parsed.Config.Environments)
			}

			out, err := generate.NewGenerator(parsed.Config, dir).Generate()
			require.NoError(t, err)
			require.NotEmpty(t, out)

			if len(tc.envs) > 0 {
				require.NotEmpty(t, parsed.Config.Deploys)
				pout, perr := generate.NewPromoteGenerator(parsed.Config, dir).Generate()
				require.NoError(t, perr)
				require.NotEmpty(t, pout)
			} else {
				require.Empty(t, parsed.Config.Deploys)
			}
		})
	}
}

func TestScaffold_SchemaDirectiveIsFirstLine(t *testing.T) {
	for _, tc := range topologyCases() {
		t.Run(tc.name, func(t *testing.T) {
			files, err := Scaffold("acme", "main", tc.envs)
			require.NoError(t, err)
			manifest := files[config.DefaultManifestFile]
			first := manifest
			if idx := strings.IndexByte(manifest, '\n'); idx >= 0 {
				first = manifest[:idx]
			}
			require.Equal(t, wantSchemaDirective, first, "schema directive must be the first line")
		})
	}
}

func TestScaffold_CLIVersionDefaulted(t *testing.T) {
	files, err := Scaffold("acme", "main", []string{"dev", "prod"})
	require.NoError(t, err)
	assert.Contains(t, files[config.DefaultManifestFile], config.DefaultCLIVersion)
}

func TestScaffold_WithCLIVersionOverride(t *testing.T) {
	files, err := Scaffold("acme", "main", []string{"dev", "prod"}, WithCLIVersion("v9.9.9"))
	require.NoError(t, err)
	assert.Contains(t, files[config.DefaultManifestFile], "v9.9.9")
}

func TestScaffold_BuildStubDeclaresCallbackInputsOutputs(t *testing.T) {
	files, err := Scaffold("acme", "main", []string{"dev", "prod"})
	require.NoError(t, err)
	stub := files[".github/workflows/build.yaml"]

	// Project name woven in.
	assert.Contains(t, stub, "name: Build acme")
	// GHA expressions survive literally (collision-safe).
	assert.Contains(t, stub, "${{ inputs.sha }}")
	assert.Contains(t, stub, "${{ jobs.build.outputs.artifact_id }}")

	var doc map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(stub), &doc))
	on, _ := doc["on"].(map[string]any)
	require.NotNil(t, on)
	wc, _ := on["workflow_call"].(map[string]any)
	require.NotNil(t, wc)
	inputs, _ := wc["inputs"].(map[string]any)
	require.NotNil(t, inputs)
	for _, k := range []string{"environment", "sha", "dry_run"} {
		_, ok := inputs[k]
		assert.True(t, ok, "build inputs missing %s", k)
	}
	outputs, _ := wc["outputs"].(map[string]any)
	require.NotNil(t, outputs)
	_, ok := outputs["artifact_id"]
	assert.True(t, ok, "build outputs missing artifact_id")
}

func TestScaffold_DeployStubDeclaresCallbackInputs(t *testing.T) {
	files, err := Scaffold("acme", "main", []string{"dev", "prod"})
	require.NoError(t, err)
	stub := files[".github/workflows/deploy.yaml"]

	assert.Contains(t, stub, "name: Deploy acme")
	assert.Contains(t, stub, "${{ inputs.environment }}")

	var doc map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(stub), &doc))
	on, _ := doc["on"].(map[string]any)
	require.NotNil(t, on)
	wc, _ := on["workflow_call"].(map[string]any)
	require.NotNil(t, wc)
	inputs, _ := wc["inputs"].(map[string]any)
	require.NotNil(t, inputs)
	for _, k := range []string{"environment", "sha", "dry_run"} {
		_, ok := inputs[k]
		assert.True(t, ok, "deploy inputs missing %s", k)
	}
}

func TestScaffold_TwoEnv_ProducesDeployStub(t *testing.T) {
	files, err := Scaffold("acme", "main", []string{"dev", "prod"})
	require.NoError(t, err)
	_, ok := files[".github/workflows/deploy.yaml"]
	assert.True(t, ok)
}

func TestScaffold_NoEnv_OmitsDeployStub(t *testing.T) {
	files, err := Scaffold("acme", "main", []string{})
	require.NoError(t, err)
	_, ok := files[".github/workflows/deploy.yaml"]
	assert.False(t, ok)
	assert.NotContains(t, files[config.DefaultManifestFile], "deploys:")
}

func TestSelfCheck_SucceedsOnProducedFiles(t *testing.T) {
	for _, tc := range topologyCases() {
		t.Run(tc.name, func(t *testing.T) {
			files, err := Scaffold("acme", "main", tc.envs)
			require.NoError(t, err)
			require.NoError(t, SelfCheck(files))
		})
	}
}

func TestSelfCheck_FailsOnInvalidEnvironmentName(t *testing.T) {
	// Environment names key job IDs, so a name containing a dot is not
	// job-ID-safe and fails config.Validate.
	files, err := Scaffold("acme", "main", []string{"dev", "prod"})
	require.NoError(t, err)
	corrupt := strings.ReplaceAll(files[config.DefaultManifestFile],
		"- dev", "- dev.bad")
	files[config.DefaultManifestFile] = corrupt

	err = SelfCheck(files)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "validat")
}

func TestSelfCheck_FailsOnInvalidYAML(t *testing.T) {
	files, err := Scaffold("acme", "main", []string{"dev", "prod"})
	require.NoError(t, err)
	files[config.DefaultManifestFile] = wantSchemaDirective + "\nci:\n  config:\n    trunk_branch: [unterminated\n"

	err = SelfCheck(files)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "parsing")
}

func TestSelfCheck_FailsOnBuildWithoutWorkflow(t *testing.T) {
	// A build entry with no workflow: is rejected by config.Validate.
	files := map[string]string{
		config.DefaultManifestFile: wantSchemaDirective + "\n" + `ci:
  config:
    trunk_branch: main
    cli_version: ` + config.DefaultCLIVersion + `
    builds:
      - name: build
        triggers: []
`,
	}
	err := SelfCheck(files)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "validat")
}

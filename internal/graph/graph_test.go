package graph

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// writeManifest lays down a representative multi-environment manifest in a temp
// directory and returns the manifest path. The manifest declares a validate
// callback, one build, and one deploy that depends on the build, so the emitted
// graph carries every node kind and a hard edge.
func writeManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o755))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Validate: &config.ValidateConfig{
			Workflow: ".github/workflows/validate.yaml",
		},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"src/**"}, DependsOn: []string{"app"}},
		},
	}
	manifest := map[string]any{config.DefaultManifestKey: config.CICDFile{Config: cfg}}
	body, err := yaml.Marshal(manifest)
	require.NoError(t, err)
	path := filepath.Join(dir, ".github", "manifest.yaml")
	require.NoError(t, os.WriteFile(path, body, 0o644))
	return path
}

// baseOptions returns valid options pointed at the given manifest, so each test
// can override the one field under exercise.
func baseOptions(manifestPath string) Options {
	return Options{
		ConfigPath:  manifestPath,
		ManifestKey: config.DefaultManifestKey,
		Granularity: string(GranularityJobs),
		Format:      formatMermaid,
		Theme:       defaultThemeName,
	}
}

func TestRun_JobsDefault_EmitsMermaid(t *testing.T) {
	path := writeManifest(t)

	var out bytes.Buffer
	require.NoError(t, Run(baseOptions(path), &out))

	got := out.String()
	require.Contains(t, got, "flowchart TD")
	require.Contains(t, got, "validate")
	require.Contains(t, got, "build_app")
	require.Contains(t, got, "deploy_app")
	// The deploy depends on the build, so a hard edge must connect them.
	require.Contains(t, got, "deploy_app --> build_app")
}

func TestRun_UnknownFormat_Errors(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.Format = "dot"

	var out bytes.Buffer
	err := Run(o, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mermaid")
	require.Empty(t, out.String())
}

func TestRun_StagesGranularity_NotYetSupported(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.Granularity = string(GranularityStages)

	var out bytes.Buffer
	err := Run(o, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stages")
	require.Contains(t, err.Error(), "jobs")
}

func TestRun_EnvGranularity_NotYetSupported(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.Granularity = string(GranularityEnv)

	var out bytes.Buffer
	err := Run(o, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "env")
}

func TestRun_UnknownGranularity_Errors(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.Granularity = "galaxy"

	var out bytes.Buffer
	err := Run(o, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "galaxy")
}

func TestRun_UnknownTheme_Errors(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.Theme = "midnight"

	var out bytes.Buffer
	err := Run(o, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "midnight")
}

func TestRun_MissingManifest_Errors(t *testing.T) {
	o := baseOptions(filepath.Join(t.TempDir(), "absent.yaml"))

	var out bytes.Buffer
	err := Run(o, &out)
	require.Error(t, err)
	require.Empty(t, out.String())
}

func TestRun_JSON_WrapsDiagram(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.JSON = true

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	var payload struct {
		Format      string `json:"format"`
		Granularity string `json:"granularity"`
		Theme       string `json:"theme"`
		Diagram     string `json:"diagram"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Equal(t, formatMermaid, payload.Format)
	require.Equal(t, string(GranularityJobs), payload.Granularity)
	require.Equal(t, defaultThemeName, payload.Theme)
	require.Contains(t, payload.Diagram, "flowchart TD")
}

func TestRun_EmptyDefaults_AutoFills(t *testing.T) {
	// An Options with empty Granularity/Format/Theme should behave as if the
	// command defaults were applied, so Run is usable without a cobra layer.
	path := writeManifest(t)
	o := Options{ConfigPath: path}

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))
	require.Contains(t, out.String(), "flowchart TD")
}

func TestNewCommand_Defaults(t *testing.T) {
	cmd := NewCommand()
	require.Equal(t, "graph", cmd.Name())

	gran, err := cmd.Flags().GetString("granularity")
	require.NoError(t, err)
	require.Equal(t, string(GranularityJobs), gran)

	format, err := cmd.Flags().GetString("format")
	require.NoError(t, err)
	require.Equal(t, formatMermaid, format)
}

func TestNewCommand_RunEmitsMermaid(t *testing.T) {
	path := writeManifest(t)

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", path})

	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "flowchart TD")
}

func TestNewCommand_RejectsUnknownFormat(t *testing.T) {
	path := writeManifest(t)

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config", path, "--format", "svg"})

	err := cmd.Execute()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "mermaid"))
}

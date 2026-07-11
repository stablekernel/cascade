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

// writeDivergedManifest lays down a two-environment manifest whose dev env state
// tracks a hotfix integration branch, so the env projection must draw a
// divergence branch off dev that rejoins at prod.
func writeDivergedManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o755))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
	}
	manifest := map[string]any{
		config.DefaultManifestKey: config.CICDFile{
			Config: cfg,
			State: map[string]*config.EnvState{
				"dev":  {Ref: "hotfix/patch"},
				"prod": {},
			},
		},
	}
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
	require.Contains(t, err.Error(), "d2")
	require.Empty(t, out.String())
}

func TestRun_D2Format_EmitsCrucibleSource(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.Format = formatD2

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	got := out.String()
	// The D2 emitter selects ELK, defines the crucible class catalog, and paints
	// the dark canvas, none of which the Mermaid emitter produces.
	require.Contains(t, got, "layout-engine: elk")
	require.Contains(t, got, `style.fill: "#151b20"`)
	require.NotContains(t, got, "flowchart TD")
	// The job nodes and a hard dependency edge carry their crucible classes.
	require.Contains(t, got, "deploy_app: ")
	require.Contains(t, got, "deploy_app -> build_app: {class: flow}")
}

func TestRun_D2Format_CrossRepoLanes(t *testing.T) {
	path := writeCrossRepoManifest(t)
	o := baseOptions(path)
	o.Format = formatD2
	o.Granularity = string(GranularityCrossRepo)

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	got := out.String()
	// Each repo lane renders as a D2 container and the primary coordinates the
	// external satellite across lanes.
	require.Contains(t, got, `primary: "primary" {`)
	require.Contains(t, got, `repo_org_cdk_infra: "org/cdk-infra" {`)
	require.Contains(t, got, `{class: external}`)
}

func TestRun_D2Format_JSONReportsFormat(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.Format = formatD2
	o.JSON = true

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	var payload struct {
		Format  string `json:"format"`
		Diagram string `json:"diagram"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Equal(t, formatD2, payload.Format)
	require.Contains(t, payload.Diagram, "layout-engine: elk")
}

func TestRun_StagesGranularity_EmitsFlowchart(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.Granularity = string(GranularityStages)

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	got := out.String()
	require.Contains(t, got, "flowchart TD")
	// The stage rollup shows coarse stages, not the per-callback job nodes.
	require.Contains(t, got, "trunk")
	require.Contains(t, got, "build")
	require.Contains(t, got, "deploy")
	require.Contains(t, got, "promote")
	require.Contains(t, got, "release")
	require.NotContains(t, got, "build_app")
	require.NotContains(t, got, "deploy_app")
}

func TestRun_EnvGranularity_EmitsStateMachine(t *testing.T) {
	path := writeDivergedManifest(t)
	o := baseOptions(path)
	o.Granularity = string(GranularityEnv)

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	got := out.String()
	require.Contains(t, got, "stateDiagram-v2")
	require.Contains(t, got, "[*] --> dev")
	require.Contains(t, got, "dev --> prod : promote")
	// The diverged dev env draws a hotfix branch that rejoins downstream.
	require.Contains(t, got, "dev --> dev_hotfix : diverge")
	require.Contains(t, got, "dev_hotfix --> prod : rejoin")
}

// writeCrossRepoManifest lays down a primary manifest that coordinates one
// external satellite and notifies an upstream primary, so the cross-repo
// projection renders a lane per repo and edges in both directions end to end.
func writeCrossRepoManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o755))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
		},
		External: []config.ExternalRepoConfig{
			{
				Repo: "org/cdk-infra",
				Deploys: []config.ExternalDeployConfig{
					{Name: "cdk", Workflow: ".github/workflows/cdk.yaml"},
				},
			},
		},
		Notify: &config.NotifyConfig{Repo: "org/platform"},
	}
	manifest := map[string]any{config.DefaultManifestKey: config.CICDFile{Config: cfg}}
	body, err := yaml.Marshal(manifest)
	require.NoError(t, err)
	path := filepath.Join(dir, ".github", "manifest.yaml")
	require.NoError(t, os.WriteFile(path, body, 0o644))
	return path
}

func TestRun_CrossRepoGranularity_EmitsLanesAndEdges(t *testing.T) {
	path := writeCrossRepoManifest(t)
	o := baseOptions(path)
	o.Granularity = string(GranularityCrossRepo)

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	got := out.String()
	require.Contains(t, got, "flowchart TD")
	// A lane per repository.
	require.Contains(t, got, `subgraph primary["primary"]`)
	require.Contains(t, got, `subgraph repo_org_cdk_infra["org/cdk-infra"]`)
	require.Contains(t, got, `subgraph repo_org_platform["org/platform"]`)
	// Cross-repo edges in both directions: primary coordinates the dependent and
	// the local pipeline notifies its upstream primary.
	require.Contains(t, got, "==>|cdk|")
	require.Contains(t, got, "-. notify .->")
}

func TestRun_CrossRepoGranularity_NoExternals_RendersPrimary(t *testing.T) {
	// A single-repo manifest with no external coordination still renders its
	// primary pipeline with no cross-repo lanes, so the granularity is safe to
	// request on any manifest.
	path := writeManifest(t)
	o := baseOptions(path)
	o.Granularity = string(GranularityCrossRepo)

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	got := out.String()
	require.Contains(t, got, "flowchart TD")
	require.Contains(t, got, "release")
	require.NotContains(t, got, "subgraph")
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

func TestRun_BlandTheme_StylesOutput(t *testing.T) {
	path := writeManifest(t)
	o := baseOptions(path)
	o.Theme = "bland"

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	got := out.String()
	require.Contains(t, got, "flowchart TD")
	// The bland theme sets a neutral Mermaid base and its own line color.
	require.Contains(t, got, `"theme": "neutral"`)
	require.Contains(t, got, "classDef node_validate")
}

func TestRun_CascadeAndBland_Differ(t *testing.T) {
	path := writeManifest(t)

	var cascade, bland bytes.Buffer
	co := baseOptions(path)
	co.Theme = "cascade"
	require.NoError(t, Run(co, &cascade))

	bo := baseOptions(path)
	bo.Theme = "bland"
	require.NoError(t, Run(bo, &bland))

	require.NotEqual(t, cascade.String(), bland.String())
}

func TestRun_DefaultAliasesCascade(t *testing.T) {
	path := writeManifest(t)

	var def, cascade bytes.Buffer
	do := baseOptions(path)
	do.Theme = "default"
	require.NoError(t, Run(do, &def))

	co := baseOptions(path)
	co.Theme = "cascade"
	require.NoError(t, Run(co, &cascade))

	require.Equal(t, cascade.String(), def.String())
}

func TestRun_FileTheme_Applied(t *testing.T) {
	path := writeManifest(t)

	dir := t.TempDir()
	themePath := filepath.Join(dir, "custom.json")
	body := `{"name":"custom","base":"base","lineColor":"#abcdef","nodeStyles":{"validate":{"fill":"#123456"}}}`
	require.NoError(t, os.WriteFile(themePath, []byte(body), 0o600))

	o := baseOptions(path)
	o.Theme = themePath

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))
	require.Contains(t, out.String(), "fill:#123456")
}

func TestRun_FileTheme_JSONReportsName(t *testing.T) {
	path := writeManifest(t)

	dir := t.TempDir()
	themePath := filepath.Join(dir, "custom.json")
	require.NoError(t, os.WriteFile(themePath, []byte(`{"name":"custom","base":"base"}`), 0o600))

	o := baseOptions(path)
	o.Theme = themePath
	o.JSON = true

	var out bytes.Buffer
	require.NoError(t, Run(o, &out))

	var payload struct {
		Theme string `json:"theme"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Equal(t, "custom", payload.Theme)
}

func TestRun_MalformedThemeFile_Errors(t *testing.T) {
	path := writeManifest(t)

	dir := t.TempDir()
	themePath := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(themePath, []byte("{not json"), 0o600))

	o := baseOptions(path)
	o.Theme = themePath

	var out bytes.Buffer
	err := Run(o, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "theme")
	require.Empty(t, out.String())
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

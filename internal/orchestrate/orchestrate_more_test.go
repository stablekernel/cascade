package orchestrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

func TestNewCommand_Structure(t *testing.T) {
	cmd := NewCommand()

	assert.Equal(t, "orchestrate", cmd.Use)
	require.NotNil(t, cmd.PersistentPreRunE)

	for _, name := range []string{"config", "manifest-key", "environment", "gha-output"} {
		assert.NotNilf(t, cmd.PersistentFlags().Lookup(name), "expected persistent flag %q", name)
	}

	var haveSetup, haveFinalize bool
	for _, sub := range cmd.Commands() {
		switch sub.Use {
		case "setup":
			haveSetup = true
		case "finalize":
			haveFinalize = true
		}
	}
	assert.True(t, haveSetup, "expected a setup subcommand")
	assert.True(t, haveFinalize, "expected a finalize subcommand")
}

func TestNewCommand_PersistentPreRunE_AutoDetectsConfig(t *testing.T) {
	prev := configPath
	t.Cleanup(func() { configPath = prev })

	configPath = ""
	cmd := NewCommand()
	require.NoError(t, cmd.PersistentPreRunE(cmd, nil))
	assert.NotEmpty(t, configPath)
}

func TestNewSetupCommand_Structure(t *testing.T) {
	cmd := newSetupCommand()
	assert.Equal(t, "setup", cmd.Use)
	require.NotNil(t, cmd.RunE)
	assert.NotNil(t, cmd.Flags().Lookup("sha"))
}

func TestNewFinalizeCommand_Structure(t *testing.T) {
	cmd := newFinalizeCommand()
	assert.Equal(t, "finalize", cmd.Use)
	require.NotNil(t, cmd.RunE)
	for _, name := range []string{"sha", "version", "deploy-results", "build-results"} {
		assert.NotNilf(t, cmd.Flags().Lookup(name), "expected flag %q", name)
	}
}

func TestRunSetup_InitError(t *testing.T) {
	prevCfg, prevEnv := configPath, environment
	t.Cleanup(func() { configPath, environment = prevCfg, prevEnv })

	configPath = "/nonexistent/path/manifest.yaml"
	environment = "dev"

	err := runSetup(newSetupCommand(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initializing orchestrator")
}

func TestRunFinalize_InitError(t *testing.T) {
	prevCfg, prevEnv := configPath, environment
	t.Cleanup(func() { configPath, environment = prevCfg, prevEnv })

	configPath = "/nonexistent/path/manifest.yaml"
	environment = "dev"

	err := runFinalize("v1.0.0", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initializing orchestrator")
}

func TestNewOrchestrator_NoEnvUsesDefaultStateKey(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, ".github", "manifest.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(configFile), 0o755))

	manifest := `ci:
  config:
    trunk_branch: main
    environments: []
`
	require.NoError(t, os.WriteFile(configFile, []byte(manifest), 0o600))

	orch, err := NewOrchestrator(configFile, "ci", "")
	require.NoError(t, err)
	assert.Equal(t, DefaultStateKey, orch.environment)
}

func TestNewOrchestrator_MissingConfigSection(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "manifest.yaml")
	// A manifest with the ci key but no config section.
	require.NoError(t, os.WriteFile(configFile, []byte("ci:\n  state: {}\n"), 0o600))

	_, err := NewOrchestrator(configFile, "ci", "dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config section not found")
}

func TestCalculateBaseSHAs_Priorities(t *testing.T) {
	repoDir, head := initRepo(t)
	defaultBase := runGit(t, repoDir, "rev-parse", "HEAD~1")

	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "app"},  // own build state (priority 1)
			{Name: "lib"},  // dependent deploy state (priority 2)
			{Name: "tool"}, // env-level SHA (priority 3)
		},
		Deploys: []config.DeployConfig{
			{Name: "infra"},
			{Name: "web", DependsOn: []string{"lib"}},
		},
	}
	o := &Orchestrator{baseDir: repoDir, cicdFile: &config.CICDFile{Config: cfg}}

	// nil envState: every base falls back to defaultBase (HEAD~1).
	base := o.calculateBaseSHAs(nil)
	assert.Equal(t, defaultBase, base["build_app"])
	assert.Equal(t, defaultBase, base["build_lib"])
	assert.Equal(t, defaultBase, base["build_tool"])
	assert.Equal(t, defaultBase, base["deploy_infra"])
	assert.Equal(t, defaultBase, base["deploy_web"])

	// Populated envState exercises the full priority ladder.
	env := &config.EnvState{
		SHA:    "envsha",
		Builds: map[string]*config.BuildState{"app": {SHA: "appbuildsha"}},
		Deploys: map[string]*config.DeployState{
			"web":   {SHA: "webdeploysha"},
			"infra": {SHA: "infradeploysha"},
		},
	}
	base = o.calculateBaseSHAs(env)
	assert.Equal(t, "appbuildsha", base["build_app"])    // 1. own build state
	assert.Equal(t, "webdeploysha", base["build_lib"])   // 2. dependent deploy state
	assert.Equal(t, "envsha", base["build_tool"])        // 3. env-level SHA
	assert.Equal(t, "infradeploysha", base["deploy_infra"])
	assert.Equal(t, "webdeploysha", base["deploy_web"])

	_ = head
}

func TestDetectChanges(t *testing.T) {
	repoDir, head := initRepo(t)
	base := runGit(t, repoDir, "rev-parse", "HEAD~1")
	o := &Orchestrator{baseDir: repoDir}

	// No base SHA: assume changes.
	assert.True(t, o.detectChanges("", head, []string{"src/**"}))
	// No triggers: assume changes.
	assert.True(t, o.detectChanges(base, head, nil))
	// The HEAD commit added src/app.go, which matches src/**.
	assert.True(t, o.detectChanges(base, head, []string{"src/**"}))
	// No trigger matches the changed path.
	assert.False(t, o.detectChanges(base, head, []string{"docs/**"}))
	// A bad base SHA makes the diff fail, which conservatively assumes changes.
	assert.True(t, o.detectChanges("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", head, []string{"src/**"}))
}

func TestCalculateVersion_EnvironmentNotFound(t *testing.T) {
	o := &Orchestrator{
		environment: "ghost",
		cicdFile: &config.CICDFile{
			Config: &config.TrunkConfig{Environments: config.EnvNames("dev", "prod")},
		},
	}

	_, err := o.calculateVersion()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCalculateVersion_MultiEnv(t *testing.T) {
	repoDir, head := initRepo(t)

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoDir))
	t.Cleanup(func() { require.NoError(t, os.Chdir(orig)) })

	o := &Orchestrator{
		environment: "dev",
		baseDir:     repoDir,
		cicdFile: &config.CICDFile{
			Config: &config.TrunkConfig{Environments: config.EnvNames("dev", "prod")},
			State: map[string]*config.EnvState{
				"dev":  {Version: "v1.0.0-rc.0"},
				"prod": {Version: "v0.9.0", SHA: head},
			},
		},
	}

	v, err := o.calculateVersion()
	require.NoError(t, err)
	assert.NotEmpty(t, v)
}

func TestWriteConfig_ReadError(t *testing.T) {
	o := &Orchestrator{
		configPath: "/nonexistent/path/manifest.yaml",
		cicdFile:   &config.CICDFile{Config: &config.TrunkConfig{}},
	}

	err := o.writeConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read config")
}

func TestWriteConfig_CustomManifestKey(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "manifest.yaml")
	seeded := `pipeline:
  config:
    trunk_branch: main
  state:
    dev:
      sha: oldsha
`
	require.NoError(t, os.WriteFile(p, []byte(seeded), 0o600))

	o := &Orchestrator{
		configPath: p,
		cicdFile: &config.CICDFile{
			Config: &config.TrunkConfig{ManifestKey: "pipeline"},
			State:  map[string]*config.EnvState{"dev": {SHA: "newsha"}},
		},
	}

	require.NoError(t, o.writeConfig())

	data, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Contains(t, string(data), "pipeline:")
	assert.Contains(t, string(data), "newsha")
	assert.NotContains(t, string(data), "oldsha")
}

func TestCommitAndPush_NoChanges(t *testing.T) {
	repoDir, _ := initRepo(t)
	manifestPath := writeManifest(t, repoDir, "abc123")
	// Commit the manifest so the working tree is clean for the configured path.
	runGit(t, repoDir, "add", ".github/manifest.yaml")
	runGit(t, repoDir, "commit", "-m", "chore: add manifest")

	o := &Orchestrator{
		baseDir:     repoDir,
		configPath:  manifestPath,
		environment: "prerelease",
		cicdFile:    &config.CICDFile{},
	}

	// With nothing to commit, commitAndPush returns early without needing a remote.
	require.NoError(t, o.commitAndPush("v0.1.0"))
}

func TestGitOutput_Error(t *testing.T) {
	repoDir, _ := initRepo(t)
	o := &Orchestrator{baseDir: repoDir}

	_, err := o.gitOutput("rev-parse", "--verify", "does-not-exist")
	require.Error(t, err)
}

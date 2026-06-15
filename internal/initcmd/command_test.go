package initcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/scaffold"
)

// runInit executes the command with args against a fresh buffer and returns
// stdout plus any error.
func runInit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// manifestPath is the on-disk relative path of the generated manifest.
func manifestPathFor(dir string) string {
	return filepath.Join(dir, config.DefaultManifestFile)
}

func TestResolveEnvs_TopologyPreset(t *testing.T) {
	for name, want := range scaffold.Topologies() {
		t.Run(name, func(t *testing.T) {
			got, err := resolveEnvs(name, "")
			require.NoError(t, err)
			if len(want) == 0 {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, want, got)
		})
	}
}

func TestResolveEnvs_DefaultsToTwoEnv(t *testing.T) {
	got, err := resolveEnvs("", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"dev", "prod"}, got)
}

func TestResolveEnvs_CustomList(t *testing.T) {
	got, err := resolveEnvs("", " staging , production ")
	require.NoError(t, err)
	assert.Equal(t, []string{"staging", "production"}, got)
}

func TestResolveEnvs_EnvsOverridesTopology(t *testing.T) {
	got, err := resolveEnvs("four-env", "only")
	require.NoError(t, err)
	assert.Equal(t, []string{"only"}, got)
}

func TestResolveEnvs_UnknownTopology(t *testing.T) {
	_, err := resolveEnvs("five-env", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown topology")
}

func TestResolveEnvs_EmptyEnvsValue(t *testing.T) {
	_, err := resolveEnvs("", " , , ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no environment names")
}

func TestResolveName_DefaultsToDirBase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-service")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	got, err := resolveName("", dir)
	require.NoError(t, err)
	assert.Equal(t, "my-service", got)
}

func TestResolveName_ExplicitWins(t *testing.T) {
	got, err := resolveName("explicit", "/tmp/whatever")
	require.NoError(t, err)
	assert.Equal(t, "explicit", got)
}

func TestRun_SuccessWritesValidScaffold(t *testing.T) {
	dir := t.TempDir()
	out, err := runInit(t, "--topology", "two-env", "--name", "demo", "--dir", dir)
	require.NoError(t, err)

	assert.Contains(t, out, "Scaffolded project \"demo\"")
	assert.Contains(t, out, "generate-workflow")
	assert.Contains(t, out, "$schema")

	// Expected file set on disk.
	for _, rel := range []string{
		config.DefaultManifestFile,
		".github/workflows/build.yaml",
		".github/workflows/deploy.yaml",
	} {
		_, statErr := os.Stat(filepath.Join(dir, rel))
		require.NoError(t, statErr, "expected file %s", rel)
	}

	// The written manifest must parse, validate, and generate.
	parsed, err := config.ParseManifestFile(manifestPathFor(dir), config.DefaultManifestKey)
	require.NoError(t, err)
	require.NotNil(t, parsed.Config)

	problems := config.Validate(parsed.Config)
	require.Empty(t, problems, "validation problems: %v", problems)

	// Re-run the full self-check (parse + validate + generate) on the bytes
	// that actually landed on disk, proving the written scaffold survives the
	// real generator.
	onDisk := map[string]string{}
	for _, rel := range []string{
		config.DefaultManifestFile,
		".github/workflows/build.yaml",
		".github/workflows/deploy.yaml",
	} {
		b, readErr := os.ReadFile(filepath.Join(dir, rel))
		require.NoError(t, readErr)
		onDisk[rel] = string(b)
	}
	require.NoError(t, scaffold.SelfCheck(onDisk))

	assert.Equal(t, []string{"dev", "prod"}, parsed.Config.Environments)
}

func TestRun_NoEnvOmitsDeployStub(t *testing.T) {
	dir := t.TempDir()
	_, err := runInit(t, "--topology", "no-env", "--name", "demo", "--dir", dir)
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, ".github/workflows/deploy.yaml"))
	assert.True(t, os.IsNotExist(statErr), "deploy stub must be absent for release-only")
}

func TestRun_RefusesOnExistingWithoutForce(t *testing.T) {
	dir := t.TempDir()
	manifest := manifestPathFor(dir)
	require.NoError(t, os.MkdirAll(filepath.Dir(manifest), 0o755))
	require.NoError(t, os.WriteFile(manifest, []byte("pre-existing\n"), 0o644))

	_, err := runInit(t, "--topology", "two-env", "--name", "demo", "--dir", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to overwrite")
	assert.Contains(t, err.Error(), "--force")

	// The pre-existing file is untouched and no new files were written.
	got, readErr := os.ReadFile(manifest)
	require.NoError(t, readErr)
	assert.Equal(t, "pre-existing\n", string(got))
	_, statErr := os.Stat(filepath.Join(dir, ".github/workflows/build.yaml"))
	assert.True(t, os.IsNotExist(statErr), "no files should be written when refusing")
}

func TestRun_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	manifest := manifestPathFor(dir)
	require.NoError(t, os.MkdirAll(filepath.Dir(manifest), 0o755))
	require.NoError(t, os.WriteFile(manifest, []byte("pre-existing\n"), 0o644))

	_, err := runInit(t, "--topology", "two-env", "--name", "demo", "--dir", dir, "--force")
	require.NoError(t, err)

	got, readErr := os.ReadFile(manifest)
	require.NoError(t, readErr)
	assert.NotEqual(t, "pre-existing\n", string(got))
	assert.Contains(t, string(got), "trunk_branch")
}

func TestRun_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	out, err := runInit(t, "--topology", "three-env", "--name", "demo", "--dir", dir, "--dry-run")
	require.NoError(t, err)

	assert.Contains(t, out, "Dry run")
	assert.Contains(t, out, "No files were written.")
	assert.Contains(t, out, config.DefaultManifestFile)
	assert.Contains(t, out, "deploy.yaml")

	// Nothing on disk.
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "dry-run must not write anything")
}

func TestRun_CustomEnvsListedAndReleaseStageLast(t *testing.T) {
	dir := t.TempDir()
	out, err := runInit(t, "--envs", "staging,production", "--name", "svc", "--dir", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "staging -> production")

	parsed, err := config.ParseManifestFile(manifestPathFor(dir), config.DefaultManifestKey)
	require.NoError(t, err)
	assert.Equal(t, []string{"staging", "production"}, parsed.Config.Environments)
}

func TestRun_CLIVersionOverride(t *testing.T) {
	dir := t.TempDir()
	_, err := runInit(t, "--topology", "two-env", "--name", "demo", "--dir", dir, "--cli-version", "v9.9.9")
	require.NoError(t, err)

	got, readErr := os.ReadFile(manifestPathFor(dir))
	require.NoError(t, readErr)
	assert.Contains(t, string(got), "v9.9.9")
}

func TestRun_ScaffoldFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	// An environment name containing a dot is not job-ID-safe; the scaffold
	// SelfCheck rejects it, so nothing must reach disk.
	_, err := runInit(t, "--envs", "dev.bad,prod", "--name", "demo", "--dir", dir)
	require.Error(t, err)
	assert.True(t, strings.Contains(strings.ToLower(err.Error()), "scaffolding"))

	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "no files should be written when the scaffold fails")
}

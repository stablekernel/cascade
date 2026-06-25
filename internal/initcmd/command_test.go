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

// TestRun_AllTopologiesOrderedManifestEnvs drives the cobra command end-to-end
// for every built-in topology and asserts the manifest that lands on disk
// carries exactly the preset's ordered environment list (release stage last).
// This exercises cli.init.topology and cli.init.envs ordering through the real
// command and the real parser, for all four presets, not just the default.
func TestRun_AllTopologiesOrderedManifestEnvs(t *testing.T) {
	cases := []struct {
		topology string
		want     []string
	}{
		{"no-env", nil},
		{"two-env", []string{"dev", "prod"}},
		{"three-env", []string{"dev", "staging", "prod"}},
		{"four-env", []string{"dev", "test", "uat", "prod"}},
	}
	for _, tc := range cases {
		t.Run(tc.topology, func(t *testing.T) {
			dir := t.TempDir()
			_, err := runInit(t, "--topology", tc.topology, "--name", "svc", "--dir", dir)
			require.NoError(t, err)

			parsed, err := config.ParseManifestFile(manifestPathFor(dir), config.DefaultManifestKey)
			require.NoError(t, err)
			require.NotNil(t, parsed.Config)

			if len(tc.want) == 0 {
				assert.Empty(t, parsed.Config.Environments, "release-only manifest omits environments")
				assert.Empty(t, parsed.Config.Deploys, "release-only manifest has no deploys")
			} else {
				// Exact ordered equality: order is load-bearing because the last
				// environment is the release stage.
				assert.Equal(t, tc.want, parsed.Config.Environments)
			}

			// The manifest that landed on disk must survive the real generator.
			parsedAsMap := map[string]string{}
			b, readErr := os.ReadFile(manifestPathFor(dir))
			require.NoError(t, readErr)
			parsedAsMap[config.DefaultManifestFile] = string(b)
			for _, rel := range []string{".github/workflows/build.yaml", ".github/workflows/deploy.yaml"} {
				wb, statErr := os.ReadFile(filepath.Join(dir, rel))
				if statErr == nil {
					parsedAsMap[rel] = string(wb)
				}
			}
			require.NoError(t, scaffold.SelfCheck(parsedAsMap))
		})
	}
}

// TestRun_DirLandsFilesInTargetDir asserts cli.init.dir: a non-current target
// directory receives the scaffold, and the current working directory is left
// untouched.
func TestRun_DirLandsFilesInTargetDir(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "nested", "service")
	require.NoError(t, os.MkdirAll(target, 0o755))

	_, err := runInit(t, "--topology", "two-env", "--name", "svc", "--dir", target)
	require.NoError(t, err)

	// Files land under the target directory.
	for _, rel := range []string{
		config.DefaultManifestFile,
		".github/workflows/build.yaml",
		".github/workflows/deploy.yaml",
	} {
		_, statErr := os.Stat(filepath.Join(target, rel))
		require.NoError(t, statErr, "expected %s under target dir", rel)
	}

	// The parent (outside the target) is not polluted with a manifest.
	_, statErr := os.Stat(filepath.Join(parent, config.DefaultManifestFile))
	assert.True(t, os.IsNotExist(statErr), "no manifest should be written outside the target dir")
}

// TestRun_NameWovenIntoStubs asserts cli.init.name: the project name reaches the
// rendered callback stubs on disk, and defaults to the target directory base
// name when --name is omitted.
func TestRun_NameWovenIntoStubs(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		dir := t.TempDir()
		_, err := runInit(t, "--topology", "two-env", "--name", "payments-api", "--dir", dir)
		require.NoError(t, err)

		build, readErr := os.ReadFile(filepath.Join(dir, ".github/workflows/build.yaml"))
		require.NoError(t, readErr)
		assert.Contains(t, string(build), "Build payments-api")

		deploy, readErr := os.ReadFile(filepath.Join(dir, ".github/workflows/deploy.yaml"))
		require.NoError(t, readErr)
		assert.Contains(t, string(deploy), "Deploy payments-api")
	})

	t.Run("defaults-to-dir-base", func(t *testing.T) {
		parent := t.TempDir()
		dir := filepath.Join(parent, "billing-svc")
		require.NoError(t, os.MkdirAll(dir, 0o755))

		_, err := runInit(t, "--topology", "two-env", "--dir", dir)
		require.NoError(t, err)

		build, readErr := os.ReadFile(filepath.Join(dir, ".github/workflows/build.yaml"))
		require.NoError(t, readErr)
		assert.Contains(t, string(build), "Build billing-svc")
	})
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

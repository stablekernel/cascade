package reset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// newResetRepo creates a real git repo with a deterministic identity, an initial
// commit (so HEAD and the current branch resolve), and an origin remote pointing
// at a GitHub-style SSH URL so getRepoInfo can parse owner/name without any
// network access.
func newResetRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInDir(t, "", "init", "-b", "main", dir)
	configureGitIdentity(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# fixture\n"), 0o600))
	gitInDir(t, dir, "add", "README.md")
	gitInDir(t, dir, "commit", "-m", "chore: init")
	gitInDir(t, dir, "remote", "add", "origin", "git@github.com:test/repo.git")
	return dir
}

func TestNewCommand_Structure(t *testing.T) {
	cmd := NewCommand()

	assert.Equal(t, "reset", cmd.Use)
	require.NotNil(t, cmd.RunE)
	require.NotNil(t, cmd.PersistentPreRunE)

	for _, name := range []string{"state", "dry-run", "push", "repo", "config", "manifest-key"} {
		assert.NotNilf(t, cmd.Flags().Lookup(name), "expected flag %q to be registered", name)
	}

	// The manifest-key flag defaults to the package default.
	mk := cmd.Flags().Lookup("manifest-key")
	require.NotNil(t, mk)
	assert.Equal(t, config.DefaultManifestKey, mk.DefValue)
}

func TestNewCommand_PersistentPreRunE_AutoDetectsConfig(t *testing.T) {
	cmd := NewCommand()
	// With no --config provided, the pre-run hook resolves a default config path
	// rather than leaving it empty.
	require.NoError(t, cmd.PersistentPreRunE(cmd, nil))
	cfg := cmd.Flags().Lookup("config")
	require.NotNil(t, cfg)
	assert.NotEmpty(t, cfg.Value.String())
}

func TestRunReset_InitError(t *testing.T) {
	// A non-git directory makes New fail, surfacing through runReset.
	dir := t.TempDir()
	err := runReset(Options{RepoPath: dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize resetter")
}

func TestRunReset_DryRunInitError(t *testing.T) {
	// Exercises the dry-run banner branch while still failing at initialization.
	dir := t.TempDir()
	err := runReset(Options{RepoPath: dir, DryRun: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize resetter")
}

func TestNew_Success(t *testing.T) {
	dir := newResetRepo(t)

	r, err := New(Options{RepoPath: dir})
	require.NoError(t, err)
	assert.Equal(t, dir, r.repoPath)
	assert.Equal(t, "test", r.repoOwner)
	assert.Equal(t, "repo", r.repoName)
	// ManifestKey defaults when unset.
	assert.Equal(t, config.DefaultManifestKey, r.manifestKey)
	// Without ResetState the manifest is not parsed.
	assert.Nil(t, r.cicdFile)
}

func TestNew_ResetStateLoadsManifest(t *testing.T) {
	dir := newResetRepo(t)
	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
  state:
    dev:
      sha: abc123
      version: v1.0.0
`
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "manifest.yaml"), []byte(manifest), 0o600))

	r, err := New(Options{RepoPath: dir, ResetState: true})
	require.NoError(t, err)
	require.NotNil(t, r.cicdFile)
	require.NotNil(t, r.cicdFile.State["dev"])
	assert.Equal(t, "abc123", r.cicdFile.State["dev"].SHA)
}

func TestNew_ResetStateMissingManifest(t *testing.T) {
	dir := newResetRepo(t)

	// No manifest on disk: parsing for ResetState must surface an error.
	_, err := New(Options{RepoPath: dir, ResetState: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse manifest")
}

func TestNew_CustomConfigAndManifestKey(t *testing.T) {
	dir := newResetRepo(t)
	cfgPath := filepath.Join(dir, "pipeline.yaml")
	manifest := `pipeline:
  config:
    trunk_branch: main
    environments:
      - dev
  state:
    dev:
      sha: zzz999
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(manifest), 0o600))

	r, err := New(Options{RepoPath: dir, ConfigPath: cfgPath, ManifestKey: "pipeline", ResetState: true})
	require.NoError(t, err)
	assert.Equal(t, cfgPath, r.configPath)
	assert.Equal(t, "pipeline", r.manifestKey)
	require.NotNil(t, r.cicdFile)
	require.NotNil(t, r.cicdFile.State["dev"])
	assert.Equal(t, "zzz999", r.cicdFile.State["dev"].SHA)
}

func TestGetRepoInfo_Success(t *testing.T) {
	dir := newResetRepo(t)
	owner, name, err := getRepoInfo(dir)
	require.NoError(t, err)
	assert.Equal(t, "test", owner)
	assert.Equal(t, "repo", name)
}

func TestGetRepoInfo_NoRemote(t *testing.T) {
	dir := t.TempDir()
	gitInDir(t, "", "init", "-b", "main", dir)
	configureGitIdentity(t, dir)

	_, _, err := getRepoInfo(dir)
	require.Error(t, err)
}

func TestCurrentBranch(t *testing.T) {
	dir := newResetRepo(t)
	r := &Resetter{repoPath: dir}

	branch, err := r.currentBranch()
	require.NoError(t, err)
	assert.Equal(t, "main", branch)
}

func TestGitOutput_Error(t *testing.T) {
	dir := newResetRepo(t)
	r := &Resetter{repoPath: dir}

	// rev-parse of a missing ref exits non-zero, surfacing the error path.
	_, err := r.gitOutput("rev-parse", "--verify", "does-not-exist")
	require.Error(t, err)
}

func TestDeleteAllTags_NoTags(t *testing.T) {
	dir := newResetRepo(t)
	r := &Resetter{repoPath: dir, opts: Options{}}

	n, err := r.deleteAllTags()
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestDeleteAllTags_DryRun(t *testing.T) {
	dir := newResetRepo(t)
	gitInDir(t, dir, "tag", "v1.0.0")
	gitInDir(t, dir, "tag", "v1.1.0")

	r := &Resetter{repoPath: dir, opts: Options{DryRun: true}}
	n, err := r.deleteAllTags()
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// Dry-run must not delete the local tags.
	out, err := r.gitOutput("tag", "-l")
	require.NoError(t, err)
	assert.Contains(t, out, "v1.0.0")
	assert.Contains(t, out, "v1.1.0")
}

func TestDeleteAllTags_DeletesLocalAndRemote(t *testing.T) {
	// A local bare remote keeps the remote-tag deletion fast and hermetic
	// (no GitHub network), while still exercising the delete loops.
	remote := t.TempDir()
	gitInDir(t, "", "init", "--bare", "-b", "main", remote)

	dir := t.TempDir()
	gitInDir(t, "", "clone", remote, dir)
	configureGitIdentity(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# fixture\n"), 0o600))
	gitInDir(t, dir, "add", "README.md")
	gitInDir(t, dir, "commit", "-m", "chore: init")
	gitInDir(t, dir, "push", "origin", "HEAD:main")
	gitInDir(t, dir, "tag", "v1.0.0")
	gitInDir(t, dir, "push", "origin", "v1.0.0")

	r := &Resetter{repoPath: dir, opts: Options{}}
	n, err := r.deleteAllTags()
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// The local tag is gone.
	out, err := r.gitOutput("tag", "-l")
	require.NoError(t, err)
	assert.Empty(t, out)
}

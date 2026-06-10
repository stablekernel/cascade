package reset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

func TestParseGitURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantName  string
		wantErr   bool
	}{
		{
			name:      "SSH format",
			url:       "git@github.com:stablekernel/cascade.git",
			wantOwner: "stablekernel",
			wantName:  "cascade",
		},
		{
			name:      "SSH format without .git",
			url:       "git@github.com:stablekernel/cascade",
			wantOwner: "stablekernel",
			wantName:  "cascade",
		},
		{
			name:      "HTTPS format",
			url:       "https://github.com/stablekernel/cascade.git",
			wantOwner: "stablekernel",
			wantName:  "cascade",
		},
		{
			name:      "HTTPS format without .git",
			url:       "https://github.com/stablekernel/cascade",
			wantOwner: "stablekernel",
			wantName:  "cascade",
		},
		{
			name:    "unsupported format",
			url:     "file:///local/repo",
			wantErr: true,
		},
		{
			name:    "invalid SSH format",
			url:     "git@github.com:invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, name, err := parseGitURL(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOwner, owner)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

func TestNew_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := New(Options{RepoPath: tmpDir})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestNew_EmptyPath(t *testing.T) {
	// This test just verifies that empty path uses cwd
	// It will fail if run outside a git repo, but that's expected
	cwd, _ := os.Getwd()
	if _, err := os.Stat(filepath.Join(cwd, ".git")); os.IsNotExist(err) {
		t.Skip("not running in a git repository")
	}

	r, err := New(Options{})
	require.NoError(t, err)
	assert.Equal(t, cwd, r.repoPath)
}

func TestOptions_Defaults(t *testing.T) {
	opts := Options{}

	assert.Equal(t, "", opts.RepoPath)
	assert.False(t, opts.ResetState)
	assert.False(t, opts.DryRun)
	assert.False(t, opts.Push)
	assert.Equal(t, "", opts.ConfigPath)
	assert.Equal(t, "", opts.ManifestKey)
}

func TestResult_ZeroValue(t *testing.T) {
	result := &Result{}

	assert.Equal(t, 0, result.ReleasesDeleted)
	assert.Equal(t, 0, result.TagsDeleted)
	assert.False(t, result.StateReset)
	assert.False(t, result.Pushed)
}

// TestResetState_DryRun tests that dry run doesn't modify the file
func TestResetState_DryRun(t *testing.T) {
	// Create a temp directory with a git repo structure
	tmpDir := t.TempDir()

	// Create .git directory
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755))

	// Create .github directory
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github"), 0755))

	// Create a mock git remote
	gitConfig := `[remote "origin"]
	url = git@github.com:test/repo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".git", "config"), []byte(gitConfig), 0644))

	// Create manifest.yaml with state (using manifest format with ci: wrapper)
	manifestContent := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
  state:
    dev:
      sha: abc123
      version: v1.0.0
    prod:
      sha: def456
      version: v0.9.0
`
	configPath := filepath.Join(tmpDir, ".github", "manifest.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(manifestContent), 0644))

	// Load the config file
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// Create resetter with dry run
	r := &Resetter{
		opts: Options{
			RepoPath:   tmpDir,
			ResetState: true,
			DryRun:     true,
		},
		repoPath:    tmpDir,
		repoOwner:   "test",
		repoName:    "repo",
		configPath:  configPath,
		manifestKey: config.DefaultManifestKey,
		cicdFile:    cicdFile,
	}

	// Run reset state
	err = r.resetState()
	require.NoError(t, err)

	// Verify file wasn't modified
	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "state:")
	assert.Contains(t, string(content), "abc123")
}

// TestResetState_ActualReset tests that state is actually cleared
func TestResetState_ActualReset(t *testing.T) {
	// Create a temp directory with a git repo structure
	tmpDir := t.TempDir()

	// Create .git directory
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755))

	// Create .github directory
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github"), 0755))

	// Create manifest.yaml with state
	manifestContent := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
  state:
    dev:
      sha: abc123
      version: v1.0.0
    prod:
      sha: def456
      version: v0.9.0
  latest_release:
    version: v0.9.0
    sha: def456
`
	configPath := filepath.Join(tmpDir, ".github", "manifest.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(manifestContent), 0644))

	// Load the config file
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	// Create resetter without dry run
	r := &Resetter{
		opts: Options{
			RepoPath:   tmpDir,
			ResetState: true,
			DryRun:     false,
		},
		repoPath:    tmpDir,
		repoOwner:   "test",
		repoName:    "repo",
		configPath:  configPath,
		manifestKey: config.DefaultManifestKey,
		cicdFile:    cicdFile,
	}

	// Run reset state
	err = r.resetState()
	require.NoError(t, err)

	// Verify state was cleared
	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NotContains(t, string(content), "abc123")

	// Verify config is still present (under ci: wrapper)
	assert.Contains(t, string(content), "ci:")
	assert.Contains(t, string(content), "trunk_branch: main")
}

// TestResetState_AlreadyEmpty tests handling of already-empty state
func TestResetState_AlreadyEmpty(t *testing.T) {
	tmpDir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github"), 0755))

	// Create manifest.yaml without state
	manifestContent := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
`
	configPath := filepath.Join(tmpDir, ".github", "manifest.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(manifestContent), 0644))

	// Load the config file
	cicdFile, err := config.ParseManifestFile(configPath, config.DefaultManifestKey)
	require.NoError(t, err)

	r := &Resetter{
		opts: Options{
			RepoPath:   tmpDir,
			ResetState: true,
			DryRun:     false,
		},
		repoPath:    tmpDir,
		repoOwner:   "test",
		repoName:    "repo",
		configPath:  configPath,
		manifestKey: config.DefaultManifestKey,
		cicdFile:    cicdFile,
	}

	// Should not error on empty state
	err = r.resetState()
	require.NoError(t, err)
}

// TestResetState_NilConfig tests handling of nil config
func TestResetState_NilConfig(t *testing.T) {
	r := &Resetter{
		opts: Options{
			ResetState: true,
			DryRun:     false,
		},
		repoPath:    "/tmp",
		repoOwner:   "test",
		repoName:    "repo",
		configPath:  "/tmp/.github/manifest.yaml",
		manifestKey: config.DefaultManifestKey,
		cicdFile:    nil, // No config loaded
	}

	err := r.resetState()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config not loaded")
}

// TestWriteConfig tests that config is written with manifest key wrapper
func TestWriteConfig(t *testing.T) {
	tmpDir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github"), 0755))

	configPath := filepath.Join(tmpDir, ".github", "manifest.yaml")

	// Create a minimal CICDFile
	cicdFile := &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "prod"},
		},
	}

	r := &Resetter{
		configPath:  configPath,
		manifestKey: "ci",
		cicdFile:    cicdFile,
	}

	err := r.writeConfig()
	require.NoError(t, err)

	// Verify file was written with ci: wrapper
	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "ci:")
	assert.Contains(t, string(content), "trunk_branch: main")
}

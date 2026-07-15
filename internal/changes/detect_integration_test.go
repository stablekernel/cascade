package changes

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
)

// nullSHA is the all-zero base SHA GitHub sends for a new branch's first push;
// Detect treats it as "everything at head changed".
const nullSHA = "0000000000000000000000000000000000000000"

// newDetectRepo initializes a git repository in a temp directory and changes
// the working directory to it for the duration of the test, since
// git.GetChangedFiles runs against the process working directory. It returns
// the SHAs of a four-commit history covering source, infra, and docs changes.
func newDetectRepo(t *testing.T) (base, srcChange, infraChange, docsChange string) {
	t.Helper()

	dir := t.TempDir()
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(orig))
	})

	runGit := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	commit := func(name, message string) string {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o750))
		require.NoError(t, os.WriteFile(name, []byte(message+"\n"), 0o600))
		runGit("add", name)
		runGit("commit", "-m", message)
		out, err := exec.Command("git", "rev-parse", "HEAD").Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")
	runGit("config", "commit.gpgsign", "false")

	base = commit("README.md", "chore: init")
	srcChange = commit("src/app/main.go", "feat: app change")
	infraChange = commit("infra/deploy.yaml", "feat: infra change")
	docsChange = commit("docs/guide.md", "docs: guide")
	return base, srcChange, infraChange, docsChange
}

// detectConfig declares one path-scoped build, one negation-filtered build, one
// path-scoped deploy, and one trigger-less (always-on) deploy, so Detect's
// per-build and per-deploy fan-out is observable.
func detectConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch: "main",
		Builds: []config.BuildConfig{
			{Name: "app", Triggers: []string{"src/**"}},
			{Name: "everything-but-docs", Triggers: []string{"**", "!docs/**", "!**/*.md"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "infra", Triggers: []string{"infra/**"}},
			{Name: "always", Triggers: []string{}},
		},
	}
}

func TestDetect_FansOutTriggeredBuildsAndDeploys(t *testing.T) {
	base, srcChange, infraChange, docsChange := newDetectRepo(t)
	cfg := detectConfig()

	tests := []struct {
		name         string
		baseSHA      string
		headSHA      string
		wantChanged  []string
		wantBuilds   []string
		wantDeploys  []string
		wantHasFiles bool
	}{
		{
			name:         "source change triggers path build, catch-all build, and always deploy",
			baseSHA:      base,
			headSHA:      srcChange,
			wantChanged:  []string{"src/app/main.go"},
			wantBuilds:   []string{"app", "everything-but-docs"},
			wantDeploys:  []string{"always"},
			wantHasFiles: true,
		},
		{
			name:         "infra change triggers infra deploy but not the app build",
			baseSHA:      srcChange,
			headSHA:      infraChange,
			wantChanged:  []string{"infra/deploy.yaml"},
			wantBuilds:   []string{"everything-but-docs"},
			wantDeploys:  []string{"infra", "always"},
			wantHasFiles: true,
		},
		{
			name:         "docs-only change is excluded by negation yet still counts as a change",
			baseSHA:      infraChange,
			headSHA:      docsChange,
			wantChanged:  []string{"docs/guide.md"},
			wantBuilds:   []string{},
			wantDeploys:  []string{"always"},
			wantHasFiles: true,
		},
		{
			name:         "identical SHAs mean no changes and nothing triggered",
			baseSHA:      docsChange,
			headSHA:      docsChange,
			wantChanged:  nil,
			wantBuilds:   []string{},
			wantDeploys:  []string{},
			wantHasFiles: false,
		},
		{
			name:         "multi-commit range accumulates every changed file",
			baseSHA:      base,
			headSHA:      infraChange,
			wantChanged:  []string{"infra/deploy.yaml", "src/app/main.go"},
			wantBuilds:   []string{"app", "everything-but-docs"},
			wantDeploys:  []string{"infra", "always"},
			wantHasFiles: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Detect(cfg, tt.baseSHA, tt.headSHA)
			require.NoError(t, err)

			assert.Equal(t, tt.wantHasFiles, result.HasChanges)
			assert.ElementsMatch(t, tt.wantChanged, result.ChangedFiles)
			assert.Equal(t, tt.wantBuilds, result.TriggeredBuilds)
			assert.Equal(t, tt.wantDeploys, result.TriggeredDeploys)
		})
	}
}

func TestDetect_NullBaseSHATreatsWholeTreeAsChanged(t *testing.T) {
	_, srcChange, _, _ := newDetectRepo(t)

	result, err := Detect(detectConfig(), nullSHA, srcChange)
	require.NoError(t, err)

	assert.True(t, result.HasChanges)
	assert.ElementsMatch(t, []string{"README.md", "src/app/main.go"}, result.ChangedFiles,
		"a null base means every file at head is a change")
	assert.Equal(t, []string{"app", "everything-but-docs"}, result.TriggeredBuilds)
	assert.Equal(t, []string{"always"}, result.TriggeredDeploys)
}

func TestDetect_UnknownSHASurfacesGitError(t *testing.T) {
	_, srcChange, _, _ := newDetectRepo(t)

	_, err := Detect(detectConfig(), "b8d6a54e6fbbf2a4a89ae0f0b1a76c9bb1e13a00", srcChange)
	require.Error(t, err, "an unresolvable base SHA must surface, not read as no changes")
}

func TestNewCommand_RequiresSHAFlags(t *testing.T) {
	cmd := NewCommand()
	cmd.SetArgs([]string{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base-sha")
}

func TestDetectChangesCommand_EmitsResultJSON(t *testing.T) {
	base, srcChange, _, _ := newDetectRepo(t)

	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
        triggers:
          - "src/**"
    deploys:
      - name: site
        workflow: .github/workflows/deploy.yaml
        triggers:
          - "docs/**"
`
	manifestPath := filepath.Join(t.TempDir(), "manifest.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0o600))

	// runDetectChanges writes to os.Stdout directly; capture it via a pipe.
	readEnd, writeEnd, err := os.Pipe()
	require.NoError(t, err)
	origStdout := os.Stdout
	os.Stdout = writeEnd
	defer func() { os.Stdout = origStdout }()

	cmd := NewCommand()
	cmd.SetArgs([]string{"--config", manifestPath, "--base-sha", base, "--head-sha", srcChange})
	execErr := cmd.Execute()

	require.NoError(t, writeEnd.Close())
	os.Stdout = origStdout
	var out bytes.Buffer
	_, err = out.ReadFrom(readEnd)
	require.NoError(t, err)
	require.NoError(t, execErr)

	var result DetectResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result), "output must be valid JSON: %s", out.String())
	assert.True(t, result.HasChanges)
	assert.Equal(t, []string{"src/app/main.go"}, result.ChangedFiles)
	assert.Equal(t, []string{"app"}, result.TriggeredBuilds)
	assert.Equal(t, []string{}, result.TriggeredDeploys)
}

func TestDetectChangesCommand_BadConfigPathErrors(t *testing.T) {
	cmd := NewCommand()
	cmd.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml"),
		"--base-sha", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--head-sha", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config")
}

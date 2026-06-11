package promote

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/release"
	"github.com/stretchr/testify/require"
)

// runGit runs a git command in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// TestRejoin_Integration_FullLifecycle exercises the divergence-end lifecycle
// end to end against a real git repository and a release-stub server: an env is
// diverged on a real env/<env> branch with a hotfix tag and draft, a normal
// promotion of a containing trunk SHA finalizes, and the env rejoins trunk with
// cleared fields, the integration branch deleted, the hotfix tag and draft
// removed, and another env's divergence left intact. The act/gitea e2e for this
// flow is owned by the e2e harness unit; this is the committed scratch-repo plus
// release-stub coverage.
func TestRejoin_Integration_FullLifecycle(t *testing.T) {
	// Bare origin plus a working clone, so env/* branches and tags are real
	// remote refs the cleanup can delete.
	originDir := t.TempDir()
	runGit(t, originDir, "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	runGit(t, workDir, "clone", originDir, ".")
	runGit(t, workDir, "config", "user.email", "test@example.com")
	runGit(t, workDir, "config", "user.name", "Test User")
	runGit(t, workDir, "config", "commit.gpgsign", "false")

	// base commit, then the fix commit on trunk.
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("one"), 0644))
	runGit(t, workDir, "add", "a.txt")
	runGit(t, workDir, "commit", "-m", "first")
	baseSHA := gitOut(t, workDir, "rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(workDir, "fix.txt"), []byte("patched"), 0644))
	runGit(t, workDir, "add", "fix.txt")
	runGit(t, workDir, "commit", "-m", "fix on trunk")
	trunkHead := gitOut(t, workDir, "rev-parse", "HEAD")
	runGit(t, workDir, "push", "origin", "main")

	// Create the env/test integration branch at base and a hotfix merge commit on
	// it, then push it as a real remote branch the cleanup will delete.
	runGit(t, workDir, "checkout", "-b", "env/test", baseSHA)
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "hotfix.txt"), []byte("hf"), 0644))
	runGit(t, workDir, "add", "hotfix.txt")
	runGit(t, workDir, "commit", "-m", "hotfix merge")
	mergeSHA := gitOut(t, workDir, "rev-parse", "HEAD")
	runGit(t, workDir, "push", "origin", "env/test")

	// Hotfix tag for the diverged base version, pushed to origin.
	runGit(t, workDir, "tag", "v1.4.0-rc.2.hotfix.1", mergeSHA)
	runGit(t, workDir, "push", "origin", "v1.4.0-rc.2.hotfix.1")
	runGit(t, workDir, "checkout", "main")
	runGit(t, workDir, "fetch", "origin", "--prune", "--tags")

	// Manifest: test diverged on env/test, uat diverged independently.
	configPath := filepath.Join(workDir, "manifest.yaml")
	manifest := `ci:
  config:
    environments: [dev, test, uat, prod]
  state:
    dev:
      sha: ` + trunkHead + `
      version: v1.4.0-rc.3
    test:
      sha: ` + mergeSHA + `
      version: v1.4.0-rc.2.hotfix.1
      ref: env/test
      base_sha: ` + baseSHA + `
      patches: [` + trunkHead + `]
    uat:
      sha: uatmerge
      version: v1.3.0-rc.5.hotfix.1
      ref: env/uat
      base_sha: uatbase
      patches: [patchY]
`
	require.NoError(t, os.WriteFile(configPath, []byte(manifest), 0644))

	// Release-stub server: report one draft for the hotfix tag, accept its delete.
	var deletedReleaseIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases/tags/v1.4.0-rc.2.hotfix.1"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(release.GitHubRelease{ID: 42, TagName: "v1.4.0-rc.2.hotfix.1", Draft: true})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/releases/"):
			deletedReleaseIDs = append(deletedReleaseIDs, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer server.Close()

	mgr := release.NewManagerWithURL("owner/repo", "token", server.URL)
	cleaner := newGitReleaseCleaner("origin", mgr)

	// Run the real Finalizer from inside the work tree so git operations resolve.
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workDir))
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	fin, err := NewFinalizer("manifest.yaml", "test", WithLifecycleCleaner(cleaner))
	require.NoError(t, err)
	fin.SetPromotionResult(&PromotionResult{
		Promotions: []EnvPromotion{{
			Environment: "test",
			SourceEnv:   "dev",
			SHA:         trunkHead,
			Version:     "v1.4.0-rc.3",
		}},
	})

	require.NoError(t, fin.Run())

	// Manifest: test rejoined trunk, fields cleared; uat divergence preserved.
	cicd, err := config.ParseManifestFile("manifest.yaml", config.DefaultManifestKey)
	require.NoError(t, err)
	st := cicd.State["test"]
	require.Equal(t, trunkHead, st.SHA)
	require.False(t, st.IsDiverged(), "test must have rejoined trunk")
	require.Empty(t, st.Ref)
	require.Empty(t, st.Patches)

	uat := cicd.State["uat"]
	require.True(t, uat.IsDiverged(), "uat divergence preserved")
	require.Equal(t, "env/uat", uat.Ref)

	// The env/test branch is deleted on origin.
	runGit(t, workDir, "fetch", "origin", "--prune")
	exists, err := git.BranchExists("origin", "env/test")
	require.NoError(t, err)
	require.False(t, exists, "env/test integration branch must be deleted")

	// The hotfix tag is gone from origin, and the draft release was deleted.
	remoteTags := gitOut(t, workDir, "ls-remote", "--tags", "origin")
	require.NotContains(t, remoteTags, "v1.4.0-rc.2.hotfix.1", "hotfix tag must be deleted on origin")
	require.NotEmpty(t, deletedReleaseIDs, "hotfix draft release must be deleted")
}

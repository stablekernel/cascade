package harness

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGiteaHotfixPrimitives exercises the branch, commit, pull request, and
// release primitives that the hotfix scenarios depend on. Each subtest creates
// its own repository against a single shared Gitea container so the (slow)
// container start cost is paid once.
func TestGiteaHotfixPrimitives(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	gitea, err := NewGiteaContainer(ctx, "", nil)
	require.NoError(t, err)
	defer func() { _ = gitea.Terminate(ctx) }()

	// seedRepo creates a repo with one commit on main and returns it plus the
	// main HEAD SHA.
	seedRepo := func(t *testing.T, name string) (*Repo, string) {
		t.Helper()
		repo, err := gitea.CreateRepo(ctx, name)
		require.NoError(t, err)
		sha, err := gitea.CreateCommit(ctx, repo, "feat: seed", map[string]string{
			"README.md": "# " + name + "\n",
		})
		require.NoError(t, err)
		require.Len(t, sha, 40)
		return repo, sha
	}

	t.Run("CreateAndGetBranch", func(t *testing.T) {
		repo, headSHA := seedRepo(t, "branch-get")

		require.NoError(t, gitea.CreateBranch(ctx, repo, "hotfix/x", headSHA))

		got, err := gitea.GetBranchSHA(ctx, repo, "hotfix/x")
		require.NoError(t, err)
		assert.Len(t, got, 40)
		assert.Equal(t, headSHA, got)
	})

	t.Run("ListBranches", func(t *testing.T) {
		repo, headSHA := seedRepo(t, "branch-list")

		require.NoError(t, gitea.CreateBranch(ctx, repo, "release/1.0", headSHA))

		// Gitea's branch listing is eventually consistent: a freshly created
		// branch is immediately resolvable by name but can take a moment to
		// appear in the list endpoint.
		require.Eventually(t, func() bool {
			branches, err := gitea.ListBranches(ctx, repo)
			if err != nil {
				return false
			}
			return contains(branches, "main") && contains(branches, "release/1.0")
		}, 10*time.Second, 250*time.Millisecond)
	})

	t.Run("CreateCommitOnBranch", func(t *testing.T) {
		repo, headSHA := seedRepo(t, "commit-branch")

		require.NoError(t, gitea.CreateBranch(ctx, repo, "feature/y", headSHA))

		newSHA, err := gitea.CreateCommitOnBranch(ctx, repo, "feature/y", "feat: work", map[string]string{
			"src/app.go": "package app\n",
		})
		require.NoError(t, err)
		assert.Len(t, newSHA, 40)
		assert.NotEqual(t, headSHA, newSHA)

		branchSHA, err := gitea.GetBranchSHA(ctx, repo, "feature/y")
		require.NoError(t, err)
		assert.Equal(t, newSHA, branchSHA)

		// main must be untouched.
		mainSHA, err := gitea.GetBranchSHA(ctx, repo, "main")
		require.NoError(t, err)
		assert.Equal(t, headSHA, mainSHA)

		// File is reachable on the feature branch.
		content, err := gitea.GetFileContentOnBranch(ctx, repo, "src/app.go", "feature/y")
		require.NoError(t, err)
		assert.Equal(t, "package app\n", content)
	})

	t.Run("CreateAndMergePR", func(t *testing.T) {
		repo, headSHA := seedRepo(t, "pr-merge")

		require.NoError(t, gitea.CreateBranch(ctx, repo, "hotfix/fix", headSHA))
		_, err := gitea.CreateCommitOnBranch(ctx, repo, "hotfix/fix", "fix: patch", map[string]string{
			"patch.txt": "patched\n",
		})
		require.NoError(t, err)

		index, err := gitea.CreatePR(ctx, repo, "hotfix/fix", "main", "Hotfix", "body", []string{"hotfix"})
		require.NoError(t, err)
		assert.Positive(t, index)

		open, err := gitea.ListOpenPRs(ctx, repo, "main", "hotfix")
		require.NoError(t, err)
		assert.Contains(t, open, index)

		// A non-matching label must filter the PR out.
		none, err := gitea.ListOpenPRs(ctx, repo, "main", "nonexistent")
		require.NoError(t, err)
		assert.NotContains(t, none, index)

		require.NoError(t, gitea.MergePR(ctx, repo, index, "squash"))

		// After merge the source branch can be deleted.
		require.NoError(t, gitea.DeleteBranch(ctx, repo, "hotfix/fix"))
	})

	t.Run("DeleteBranch", func(t *testing.T) {
		repo, headSHA := seedRepo(t, "branch-delete")

		require.NoError(t, gitea.CreateBranch(ctx, repo, "throwaway", headSHA))

		// Wait for the new branch to surface in the (eventually consistent)
		// list endpoint before deleting it.
		require.Eventually(t, func() bool {
			branches, err := gitea.ListBranches(ctx, repo)
			return err == nil && contains(branches, "throwaway")
		}, 10*time.Second, 250*time.Millisecond)

		require.NoError(t, gitea.DeleteBranch(ctx, repo, "throwaway"))

		require.Eventually(t, func() bool {
			branches, err := gitea.ListBranches(ctx, repo)
			return err == nil && !contains(branches, "throwaway")
		}, 10*time.Second, 250*time.Millisecond)
	})

	t.Run("CreateAndGetRelease", func(t *testing.T) {
		repo, headSHA := seedRepo(t, "release-get")

		// A release needs a tag to point at.
		require.NoError(t, gitea.CreateTag(ctx, repo, "v1.0.0", headSHA))

		id, err := gitea.CreateRelease(ctx, repo, "v1.0.0", "Release 1.0.0", "notes", true, false)
		require.NoError(t, err)
		assert.Positive(t, id)

		releases, err := gitea.GetReleases(ctx, repo)
		require.NoError(t, err)

		var found *GiteaRelease
		for i := range releases {
			if releases[i].ID == id {
				found = &releases[i]
				break
			}
		}
		require.NotNil(t, found, fmt.Sprintf("release %d not present in %+v", id, releases))
		assert.Equal(t, "v1.0.0", found.TagName)
		assert.Equal(t, "Release 1.0.0", found.Name)
		assert.True(t, found.IsDraft)
		assert.False(t, found.IsPrerelease)
	})
}

// contains reports whether s appears in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

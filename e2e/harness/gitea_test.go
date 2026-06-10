package harness

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGiteaContainer_Start(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	gitea, err := NewGiteaContainer(ctx, "", nil)
	require.NoError(t, err)
	defer func() { _ = gitea.Terminate(ctx) }()

	assert.NotEmpty(t, gitea.URL())
	assert.NotEmpty(t, gitea.AdminToken())
}

func TestGiteaContainer_CreateRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	gitea, err := NewGiteaContainer(ctx, "", nil)
	require.NoError(t, err)
	defer func() { _ = gitea.Terminate(ctx) }()

	repo, err := gitea.CreateRepo(ctx, "test-repo")
	require.NoError(t, err)
	assert.Equal(t, "test-repo", repo.Name)
	assert.NotEmpty(t, repo.CloneURL)
}

func TestGiteaContainer_CreateCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	gitea, err := NewGiteaContainer(ctx, "", nil)
	require.NoError(t, err)
	defer func() { _ = gitea.Terminate(ctx) }()

	repo, err := gitea.CreateRepo(ctx, "commit-test")
	require.NoError(t, err)

	sha, err := gitea.CreateCommit(ctx, repo, "feat: add main.go", map[string]string{
		"src/main.go": "package main\n\nfunc main() {}\n",
	})
	require.NoError(t, err)
	assert.Len(t, sha, 40) // Full SHA
}

func TestGiteaContainer_CreateTag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	gitea, err := NewGiteaContainer(ctx, "", nil)
	require.NoError(t, err)
	defer func() { _ = gitea.Terminate(ctx) }()

	repo, err := gitea.CreateRepo(ctx, "tag-test")
	require.NoError(t, err)

	sha, err := gitea.CreateCommit(ctx, repo, "feat: initial", map[string]string{
		"README.md": "# Test\n",
	})
	require.NoError(t, err)

	err = gitea.CreateTag(ctx, repo, "v1.0.0", sha)
	require.NoError(t, err)

	tags, err := gitea.GetTags(ctx, repo)
	require.NoError(t, err)
	assert.Contains(t, tags, "v1.0.0")
}

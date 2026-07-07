package harness

import (
	"context"
	"testing"
	"time"

	"github.com/stablekernel/cascade/internal/taggrammar"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsRCTagForBase locks the release-cleanup reaper predicate. The default
// grammar must reap exactly the tags it reaped before the grammar was threaded
// through (byte-identical), and a custom grammar must reap its own pre-release
// shape rather than the hardcoded "-rc." shape.
func TestIsRCTagForBase(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		base string
		spec taggrammar.Spec
		want bool
	}{
		// Default grammar (rc / "."): byte-identical to the pre-grammar reaper.
		{"default rc zero", "v1.2.3-rc.0", "v1.2.3", taggrammar.Default(), true},
		{"default rc multi digit", "v1.2.3-rc.10", "v1.2.3", taggrammar.Default(), true},
		{"default bare release", "v1.2.3", "v1.2.3", taggrammar.Default(), false},
		{"default empty suffix", "v1.2.3-rc.", "v1.2.3", taggrammar.Default(), false},
		{"default different base", "v1.2.4-rc.0", "v1.2.3", taggrammar.Default(), false},
		{"default nested hotfix rejected", "v1.2.3-rc.4.hotfix.1", "v1.2.3", taggrammar.Default(), false},
		{"default foreign token", "v1.2.3-beta0", "v1.2.3", taggrammar.Default(), false},
		{"default unrelated", "release-1", "v1.2.3", taggrammar.Default(), false},

		// Custom grammar (beta / ""): reaps v<base>-beta<digits>.
		{"custom beta zero", "v0.2.0-beta0", "v0.2.0", betaSpec(), true},
		{"custom beta multi digit", "v0.2.0-beta12", "v0.2.0", betaSpec(), true},
		{"custom bare release", "v0.2.0", "v0.2.0", betaSpec(), false},
		{"custom rejects default rc", "v0.2.0-rc.0", "v0.2.0", betaSpec(), false},
		{"custom empty suffix", "v0.2.0-beta", "v0.2.0", betaSpec(), false},
		{"custom different base", "v0.3.0-beta0", "v0.2.0", betaSpec(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isRCTagForBase(tt.tag, tt.base, tt.spec))
		})
	}
}

func betaSpec() taggrammar.Spec {
	s := taggrammar.Default()
	s.PreReleaseToken = "beta"
	s.PreReleaseSeparator = ""
	return s
}

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

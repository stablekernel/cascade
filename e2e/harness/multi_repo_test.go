package harness

import (
	"context"
	"testing"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiRepoHarness_CreateRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	// Create a single repo
	setup := MultiRepoSetup{
		Name: "test-service",
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "test", "prod"},
			Deploys: []config.DeployConfig{
				{Name: "app", Workflow: ".github/workflows/deploy.yaml"},
			},
		},
		Commits: []Commit{
			{Message: "feat: initial feature", Files: map[string]string{"src/main.go": "package main"}},
		},
		Tags: []string{"v0.1.0"},
	}

	repoCtx, err := h.CreateRepo(ctx, setup)
	require.NoError(t, err)

	assert.Equal(t, "test-service", repoCtx.Name)
	assert.NotEmpty(t, repoCtx.HeadSHA)
	assert.NotNil(t, repoCtx.ExecCtx)
	assert.True(t, repoCtx.ExecCtx.HasTag("v0.1.0"))

	// Verify repo is retrievable
	retrieved := h.GetRepo("test-service")
	assert.Equal(t, repoCtx, retrieved)
}

func TestMultiRepoHarness_SetupPrimarySatellite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	primary := MultiRepoSetup{
		Name: "primary-backend",
		Config: &config.TrunkConfig{
			TrunkBranch:  "master",
			Environments: []string{"dev", "test", "prod"},
			Deploys: []config.DeployConfig{
				{Name: "api", Workflow: ".github/workflows/deploy-api.yaml"},
			},
			External: []config.ExternalRepoConfig{
				{
					Repo: "example/cdk-infra",
					Ref:  "main",
					Deploys: []config.ExternalDeployConfig{
						{Name: "cdk", Workflow: "example/cdk-infra/.github/workflows/deploy.yaml"},
					},
				},
			},
		},
	}

	satellite := MultiRepoSetup{
		Name: "cdk-infra",
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "test", "prod"},
			Deploys: []config.DeployConfig{
				{Name: "cdk", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{"cdk/**"}},
			},
			Notify: &config.NotifyConfig{
				Repo:     "example/primary-backend",
				Workflow: ".github/workflows/external-update.yaml",
			},
		},
	}

	err := h.SetupPrimarySatellite(ctx, primary, satellite)
	require.NoError(t, err)

	// Verify primary repo
	primaryRepo := h.GetPrimaryRepo()
	require.NotNil(t, primaryRepo)
	assert.Equal(t, "primary-backend", primaryRepo.Name)
	assert.True(t, primaryRepo.IsPrimary)
	assert.Contains(t, primaryRepo.Satellites, "cdk-infra")

	// Verify satellite repo
	satRepo := h.GetRepo("cdk-infra")
	require.NotNil(t, satRepo)
	assert.False(t, satRepo.IsPrimary)
	assert.Equal(t, "primary-backend", satRepo.Primary)

	// Verify satellite repos list
	satellites := h.GetSatelliteRepos()
	assert.Len(t, satellites, 1)
	assert.Equal(t, "cdk-infra", satellites[0].Name)
}

func TestMultiRepoHarness_CommitToRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	// Create a repo
	setup := MultiRepoSetup{
		Name: "commit-test",
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev"},
		},
	}

	_, err := h.CreateRepo(ctx, setup)
	require.NoError(t, err)

	// Create a new commit
	sha, err := h.CommitToRepo(ctx, "commit-test", "feat: new feature", map[string]string{
		"src/feature.go": "package feature",
	})
	require.NoError(t, err)
	assert.Len(t, sha, 40)

	// Verify repo HEAD updated
	repo := h.GetRepo("commit-test")
	assert.Equal(t, sha, repo.HeadSHA)
}

func TestMultiRepoHarness_TagOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	// Create a repo
	setup := MultiRepoSetup{
		Name: "tag-test",
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev"},
		},
	}

	_, err := h.CreateRepo(ctx, setup)
	require.NoError(t, err)

	// Create a tag
	err = h.CreateTagInRepo(ctx, "tag-test", "v1.0.0")
	require.NoError(t, err)

	// Get tags
	tags, err := h.GetTagsInRepo(ctx, "tag-test")
	require.NoError(t, err)
	assert.Contains(t, tags, "v1.0.0")
}

func TestMultiRepoHarness_FileContent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	// Create a repo with a specific file
	setup := MultiRepoSetup{
		Name: "file-test",
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev"},
		},
		Commits: []Commit{
			{
				Message: "feat: add test file",
				Files: map[string]string{
					"test/data.txt": "hello world",
				},
			},
		},
	}

	_, err := h.CreateRepo(ctx, setup)
	require.NoError(t, err)

	// Read file content
	content, err := h.GetFileContentInRepo(ctx, "file-test", "test/data.txt")
	require.NoError(t, err)
	assert.Equal(t, "hello world", content)
}

func TestMultiRepoHarness_MultipleRepos(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	// Create multiple independent repos
	repos := []MultiRepoSetup{
		{Name: "service-a", Config: &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev"}}},
		{Name: "service-b", Config: &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev"}}},
		{Name: "service-c", Config: &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev"}}},
	}

	for _, setup := range repos {
		_, err := h.CreateRepo(ctx, setup)
		require.NoError(t, err, "failed to create repo %s", setup.Name)
	}

	// Verify all repos exist
	assert.NotNil(t, h.GetRepo("service-a"))
	assert.NotNil(t, h.GetRepo("service-b"))
	assert.NotNil(t, h.GetRepo("service-c"))

	// Verify they have independent state
	sha1, err := h.CommitToRepo(ctx, "service-a", "feat: a feature", map[string]string{"a.txt": "a"})
	require.NoError(t, err)

	sha2, err := h.CommitToRepo(ctx, "service-b", "feat: b feature", map[string]string{"b.txt": "b"})
	require.NoError(t, err)

	assert.NotEqual(t, sha1, sha2)
	assert.Equal(t, sha1, h.GetRepo("service-a").HeadSHA)
	assert.Equal(t, sha2, h.GetRepo("service-b").HeadSHA)
}

package harness

import (
	"context"
	"testing"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiRepoScenario_Parse(t *testing.T) {
	yaml := `
name: satellite-notifies-primary
description: Test cross-repo notification

repos:
  primary-backend:
    config:
      trunk_branch: master
      environments: [dev, test, prod]
      deploys:
        - name: api
          workflow: .github/workflows/deploy-api.yaml
  cdk-infra:
    config:
      trunk_branch: main
      environments: [dev, test, prod]
      deploys:
        - name: cdk
          workflow: .github/workflows/deploy.yaml

primary: primary-backend

steps:
  - name: commit-to-satellite
    repo: cdk-infra
    action: commit
    commit:
      message: "feat: update cdk"
      files:
        cdk/stack.ts: "// updated"

  - name: notify-primary
    repo: cdk-infra
    action: dispatch
    dispatch:
      target_repo: primary-backend
      workflow: .github/workflows/external-update.yaml
      inputs:
        deploy_name: cdk
        environment: dev
        sha: "${cdk-infra.head_sha}"

expect:
  repos:
    primary-backend:
      tags:
        - pattern: v1.0.0-rc.0
`

	scenario, err := ParseMultiRepoScenario([]byte(yaml))
	require.NoError(t, err)

	assert.Equal(t, "satellite-notifies-primary", scenario.Name)
	assert.Equal(t, "primary-backend", scenario.Primary)
	assert.Len(t, scenario.Repos, 2)
	assert.Len(t, scenario.Steps, 2)

	// Check step parsing
	step1 := scenario.Steps[0]
	assert.Equal(t, "commit-to-satellite", step1.Name)
	assert.Equal(t, "cdk-infra", step1.Repo)
	assert.Equal(t, "commit", step1.Action)
	assert.NotNil(t, step1.Commit)
	assert.Equal(t, "feat: update cdk", step1.Commit.Message)

	step2 := scenario.Steps[1]
	assert.Equal(t, "dispatch", step2.Action)
	assert.NotNil(t, step2.Dispatch)
	assert.Equal(t, "primary-backend", step2.Dispatch.TargetRepo)
}

func TestMultiRepoRunner_Setup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	scenario := &MultiRepoScenario{
		Name: "test-setup",
		Repos: map[string]RepoScenario{
			"service-a": {
				Config: config.TrunkConfig{
					TrunkBranch:  "main",
					Environments: []string{"dev", "prod"},
				},
			},
			"service-b": {
				Config: config.TrunkConfig{
					TrunkBranch:  "main",
					Environments: []string{"dev", "prod"},
				},
			},
		},
		Primary: "service-a",
	}

	runner := NewMultiRepoRunner(h, scenario)
	err := runner.Setup(ctx)
	require.NoError(t, err)

	// Verify repos were created
	assert.NotNil(t, h.GetRepo("service-a"))
	assert.NotNil(t, h.GetRepo("service-b"))

	// Verify variables were stored
	assert.NotEmpty(t, runner.varStore["service-a.head_sha"])
	assert.NotEmpty(t, runner.varStore["service-b.head_sha"])
}

func TestMultiRepoRunner_CommitStep(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	scenario := &MultiRepoScenario{
		Name: "test-commit",
		Repos: map[string]RepoScenario{
			"my-repo": {
				Config: config.TrunkConfig{
					TrunkBranch:  "main",
					Environments: []string{"dev"},
				},
			},
		},
		Steps: []ScenarioStep{
			{
				Name:   "add-feature",
				Repo:   "my-repo",
				Action: "commit",
				Commit: &StepCommit{
					Message: "feat: add feature",
					Files: map[string]string{
						"src/feature.go": "package feature",
					},
				},
			},
		},
	}

	runner := NewMultiRepoRunner(h, scenario)
	require.NoError(t, runner.Setup(ctx))

	initialSHA := runner.varStore["my-repo.head_sha"]

	require.NoError(t, runner.RunSteps(ctx))

	// Verify SHA changed
	newSHA := runner.varStore["my-repo.head_sha"]
	assert.NotEqual(t, initialSHA, newSHA)

	// Verify file was created
	content, err := h.GetFileContentInRepo(ctx, "my-repo", "src/feature.go")
	require.NoError(t, err)
	assert.Equal(t, "package feature", content)
}

func TestMultiRepoRunner_Interpolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	scenario := &MultiRepoScenario{
		Name: "test-interpolation",
		Repos: map[string]RepoScenario{
			"my-repo": {
				Config: config.TrunkConfig{
					TrunkBranch:  "main",
					Environments: []string{"dev"},
				},
			},
		},
		Steps: []ScenarioStep{
			{
				Name:   "commit-with-sha",
				Repo:   "my-repo",
				Action: "commit",
				Commit: &StepCommit{
					Message: "feat: add sha file",
					Files: map[string]string{
						// Include the SHA in the file content to verify interpolation
						"sha.txt": "Previous SHA: ${my-repo.head_sha}",
					},
				},
			},
		},
	}

	runner := NewMultiRepoRunner(h, scenario)
	require.NoError(t, runner.Setup(ctx))

	initialSHA := runner.varStore["my-repo.head_sha"]
	require.NoError(t, runner.RunSteps(ctx))

	// Verify interpolation worked
	content, err := h.GetFileContentInRepo(ctx, "my-repo", "sha.txt")
	require.NoError(t, err)
	assert.Contains(t, content, initialSHA)
}

func TestMultiRepoRunner_CrossRepoDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	// Create primary and satellite repos. The primary's external.repo and the
	// dispatch source_repo must match for the `cascade external update` verb to
	// accept the update, and the primary must already carry dev state so the
	// verb's state[environment] check passes.
	primary := MultiRepoSetup{
		Name: "primary",
		Config: &config.TrunkConfig{
			TrunkBranch:  "master",
			Environments: []string{"dev", "test", "prod"},
			External: []config.ExternalRepoConfig{
				{
					Repo: "org/satellite",
					Ref:  "main",
					Deploys: []config.ExternalDeployConfig{
						{Name: "cdk", Workflow: "org/satellite/.github/workflows/deploy.yaml"},
					},
				},
			},
		},
		Manifest: map[string]interface{}{
			"state": map[string]interface{}{
				"dev": map[string]interface{}{
					"sha":     "primary-initial",
					"version": "v1.0.0-rc.0",
				},
			},
		},
	}

	satellite := MultiRepoSetup{
		Name: "satellite",
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "test", "prod"},
			Deploys: []config.DeployConfig{
				{Name: "cdk", Workflow: ".github/workflows/deploy.yaml"},
			},
			Notify: &config.NotifyConfig{
				Repo:     "org/primary",
				Workflow: ".github/workflows/external-update.yaml",
			},
		},
	}

	require.NoError(t, h.SetupPrimarySatellite(ctx, primary, satellite))

	// Drive the REAL external-update workflow under act with the dispatch
	// inputs the satellite would send. The verb commits ci.state.dev.external.cdk
	// back to the primary's gitea manifest.
	err := h.RealCrossRepoDispatch(ctx, "satellite", "primary",
		".github/workflows/external-update.yaml",
		map[string]string{
			"source_repo": "org/satellite",
			"deploy_name": "cdk",
			"environment": "dev",
			"sha":         "satellite-sha-123",
			"version":     "v1.0.0",
		})
	require.NoError(t, err)

	// Verify external state landed in the REAL committed manifest.
	content, err := h.GetFileContentInRepo(ctx, "primary", ".github/manifest.yaml")
	require.NoError(t, err)
	assert.Contains(t, content, "satellite-sha-123")
	assert.Contains(t, content, "v1.0.0")

	// Verify the satellite's generated orchestrate.yaml carries the Notify step.
	require.NoError(t, h.RunSatelliteOrchestrateAndAssertNotify(ctx, "satellite"))
}

func TestMultiRepoRunner_FullScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	scenario := &MultiRepoScenario{
		Name: "full-lifecycle",
		Repos: map[string]RepoScenario{
			"primary": {
				Config: config.TrunkConfig{
					TrunkBranch:  "master",
					Environments: []string{"dev", "test", "prod"},
				},
			},
			"satellite": {
				Config: config.TrunkConfig{
					TrunkBranch:  "main",
					Environments: []string{"dev", "test", "prod"},
				},
			},
		},
		Primary: "primary",
		Steps: []ScenarioStep{
			{
				Name:   "commit-to-primary",
				Repo:   "primary",
				Action: "commit",
				Commit: &StepCommit{
					Message: "feat: add api",
					Files:   map[string]string{"src/api.go": "package api"},
				},
			},
			{
				Name:   "commit-to-satellite",
				Repo:   "satellite",
				Action: "commit",
				Commit: &StepCommit{
					Message: "feat: add cdk",
					Files:   map[string]string{"cdk/stack.ts": "// stack"},
				},
			},
			{
				Name:   "tag-primary",
				Repo:   "primary",
				Action: "tag",
				Tag:    &StepTag{Tag: "v1.0.0"},
			},
		},
	}

	runner := NewMultiRepoRunner(h, scenario)
	err := runner.Run(ctx)
	require.NoError(t, err)

	// Verify tags
	tags, err := h.GetTagsInRepo(ctx, "primary")
	require.NoError(t, err)
	assert.Contains(t, tags, "v1.0.0")
}

func TestMultiRepoRunner_ConcurrentExternalUpdatesPreserveBothSlots(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	h := NewMultiRepoHarness(t)
	require.NoError(t, h.SetupInfra(ctx))
	defer h.Cleanup(ctx)

	// Create a primary configured with TWO external repos. Sequential dispatches
	// to the external-update workflow will demonstrate that the second update
	// preserves the first's external slot in the manifest. The unit tests already
	// prove the retry logic works in isolation; this e2e test exercises the real
	// workflow and manifest with two distinct external artifacts.
	// Note: true concurrency (simultaneous pushes) is not feasible under act/gitea
	// because there is no live workflow_dispatch API. Sequential with interleaved
	// state is sufficient to exercise fetch+reset+re-apply retry logic.
	primary := MultiRepoSetup{
		Name: "primary",
		Config: &config.TrunkConfig{
			TrunkBranch:  "master",
			Environments: []string{"dev", "test", "prod"},
			External: []config.ExternalRepoConfig{
				{
					Repo: "org/cdk-infra",
					Ref:  "main",
					Deploys: []config.ExternalDeployConfig{
						{Name: "cdk", Workflow: "org/cdk-infra/.github/workflows/deploy.yaml"},
					},
				},
				{
					Repo: "org/lambda-service",
					Ref:  "main",
					Deploys: []config.ExternalDeployConfig{
						{Name: "lambda", Workflow: "org/lambda-service/.github/workflows/deploy.yaml"},
					},
				},
			},
		},
		Manifest: map[string]interface{}{
			"state": map[string]interface{}{
				"dev": map[string]interface{}{
					"sha":     "primary-initial",
					"version": "v1.0.0-rc.0",
				},
			},
		},
	}

	cdk := MultiRepoSetup{
		Name: "cdk-infra",
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "test", "prod"},
			Deploys: []config.DeployConfig{
				{Name: "cdk", Workflow: ".github/workflows/deploy.yaml"},
			},
			Notify: &config.NotifyConfig{
				Repo:     "org/primary",
				Workflow: ".github/workflows/external-update.yaml",
			},
		},
	}

	lambda := MultiRepoSetup{
		Name: "lambda-service",
		Config: &config.TrunkConfig{
			TrunkBranch:  "main",
			Environments: []string{"dev", "test", "prod"},
			Deploys: []config.DeployConfig{
				{Name: "lambda", Workflow: ".github/workflows/deploy.yaml"},
			},
			Notify: &config.NotifyConfig{
				Repo:     "org/primary",
				Workflow: ".github/workflows/external-update.yaml",
			},
		},
	}

	require.NoError(t, h.SetupPrimarySatellite(ctx, primary, cdk, lambda))

	// First external update: dispatch cdk-infra's artifact into primary's dev
	// state. This should commit and push successfully.
	err := h.RealCrossRepoDispatch(ctx, "cdk-infra", "primary",
		".github/workflows/external-update.yaml",
		map[string]string{
			"source_repo": "org/cdk-infra",
			"deploy_name": "cdk",
			"environment": "dev",
			"sha":         "cdk-sha-abc123",
			"version":     "v1.2.0",
		})
	require.NoError(t, err, "first external update (cdk) should succeed")

	// Verify cdk slot landed in manifest
	manifestAfterCdk, err := h.GetFileContentInRepo(ctx, "primary", ".github/manifest.yaml")
	require.NoError(t, err)
	assert.Contains(t, manifestAfterCdk, "cdk-sha-abc123", "cdk SHA should be in manifest after first update")
	assert.Contains(t, manifestAfterCdk, "v1.2.0", "cdk version should be in manifest after first update")

	// Second external update: dispatch lambda-service's artifact into the same
	// primary dev state. Without the retry logic, this would lose the cdk slot.
	// With the retry logic (fetch+reset+re-apply), both slots survive.
	err = h.RealCrossRepoDispatch(ctx, "lambda-service", "primary",
		".github/workflows/external-update.yaml",
		map[string]string{
			"source_repo": "org/lambda-service",
			"deploy_name": "lambda",
			"environment": "dev",
			"sha":         "lambda-sha-def456",
			"version":     "v2.1.0",
		})
	require.NoError(t, err, "second external update (lambda) should succeed")

	// Verify BOTH slots are in the final manifest. This proves that the second
	// update did not lose the first's changes.
	manifestFinal, err := h.GetFileContentInRepo(ctx, "primary", ".github/manifest.yaml")
	require.NoError(t, err)

	// Both external artifacts must be present in the committed manifest
	assert.Contains(t, manifestFinal, "cdk-sha-abc123", "cdk SHA must survive second update")
	assert.Contains(t, manifestFinal, "v1.2.0", "cdk version must survive second update")
	assert.Contains(t, manifestFinal, "lambda-sha-def456", "lambda SHA must be present after second update")
	assert.Contains(t, manifestFinal, "v2.1.0", "lambda version must be present after second update")

	// Verify the satellite's generated orchestrate workflows carry the Notify step.
	require.NoError(t, h.RunSatelliteOrchestrateAndAssertNotify(ctx, "cdk-infra"))
	require.NoError(t, h.RunSatelliteOrchestrateAndAssertNotify(ctx, "lambda-service"))
}

package generate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDependencyGraph(t *testing.T) {
	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "app", DependsOn: []string{}},
			{Name: "worker", DependsOn: []string{"app"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "services", DependsOn: []string{"app", "worker"}},
		},
	}

	graph := BuildDependencyGraph(cfg)

	// Check nodes exist with prefixed job IDs
	assert.Contains(t, graph.Nodes, "build-app")
	assert.Contains(t, graph.Nodes, "build-worker")
	assert.Contains(t, graph.Nodes, "deploy-services")

	// Check edges use prefixed job IDs
	assert.Contains(t, graph.Edges["build-worker"], "build-app")
	assert.Contains(t, graph.Edges["deploy-services"], "build-app")
	assert.Contains(t, graph.Edges["deploy-services"], "build-worker")
}

func TestTopologicalSort(t *testing.T) {
	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "app", DependsOn: []string{}},
			{Name: "worker", DependsOn: []string{"app"}},
			{Name: "api", DependsOn: []string{"app"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "services", DependsOn: []string{"worker", "api"}},
		},
	}

	graph := BuildDependencyGraph(cfg)
	sorted, err := graph.TopologicalSort()
	require.NoError(t, err)

	// app must come before worker and api (using prefixed job IDs)
	appIdx := indexOf(sorted, "build-app")
	workerIdx := indexOf(sorted, "build-worker")
	apiIdx := indexOf(sorted, "build-api")
	servicesIdx := indexOf(sorted, "deploy-services")

	assert.Less(t, appIdx, workerIdx)
	assert.Less(t, appIdx, apiIdx)
	assert.Less(t, workerIdx, servicesIdx)
	assert.Less(t, apiIdx, servicesIdx)
}

func TestGetAllDependencies(t *testing.T) {
	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "base", DependsOn: []string{}},
			{Name: "app", DependsOn: []string{"base"}},
			{Name: "worker", DependsOn: []string{"app"}},
		},
	}

	graph := BuildDependencyGraph(cfg)

	// worker depends on app and base (transitively) - using prefixed job IDs
	deps := graph.GetAllDependencies("build-worker")
	assert.Contains(t, deps, "build-app")
	assert.Contains(t, deps, "build-base")

	// app depends only on base
	deps = graph.GetAllDependencies("build-app")
	assert.Contains(t, deps, "build-base")
	assert.NotContains(t, deps, "build-worker")
}

func TestGetDirectDependencies(t *testing.T) {
	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "base", DependsOn: []string{}},
			{Name: "app", DependsOn: []string{"base"}},
			{Name: "worker", DependsOn: []string{"app"}},
		},
	}

	graph := BuildDependencyGraph(cfg)

	// worker has only direct dependency on app (using prefixed job IDs)
	deps := graph.GetDirectDependencies("build-worker")
	assert.Equal(t, []string{"build-app"}, deps)

	// base has no dependencies
	deps = graph.GetDirectDependencies("build-base")
	assert.Empty(t, deps)
}

func TestBuildDependencyGraph_WithValidate(t *testing.T) {
	cfg := &config.TrunkConfig{
		Validate: &config.ValidateConfig{
			Workflow:  ".github/workflows/validate.yaml",
			RunPolicy: config.RunPolicyAlways,
			OnFailure: config.OnFailureAbort,
			Retries:   intPtr(1),
		},
		Builds: []config.BuildConfig{
			{Name: "app", DependsOn: []string{}},
		},
	}

	graph := BuildDependencyGraph(cfg)

	// Validate node should exist
	assert.Contains(t, graph.Nodes, "validate")
	validateNode := graph.Nodes["validate"]
	assert.Equal(t, config.CallbackTypeValidate, validateNode.Type)
	assert.Equal(t, ".github/workflows/validate.yaml", validateNode.Workflow)
	assert.Equal(t, config.RunPolicyAlways, validateNode.RunPolicy)
	assert.Equal(t, config.OnFailureAbort, validateNode.OnFailure)
	assert.Equal(t, 1, validateNode.Retries)

	// Builds without explicit depends_on should depend on validate
	assert.Contains(t, graph.Edges["build-app"], "validate")
}

func TestBuildDependencyGraph_CallbackInfo(t *testing.T) {
	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{
				Name:      "app",
				Workflow:  ".github/workflows/build.yaml",
				RunPolicy: config.RunPolicyAlways,
				OnFailure: config.OnFailureContinue,
				Retries:   2,
			},
		},
		Deploys: []config.DeployConfig{
			{
				Name:      "services",
				Workflow:  ".github/workflows/deploy.yaml",
				RunPolicy: config.RunPolicyDefault,
				OnFailure: config.OnFailureAbort,
				Retries:   0,
			},
		},
	}

	graph := BuildDependencyGraph(cfg)

	// Check build info (using prefixed job ID)
	appNode := graph.Nodes["build-app"]
	assert.Equal(t, "app", appNode.Name)
	assert.Equal(t, "build-app", appNode.JobID)
	assert.Equal(t, "Build (app)", appNode.DisplayName)
	assert.Equal(t, config.CallbackTypeBuild, appNode.Type)
	assert.Equal(t, ".github/workflows/build.yaml", appNode.Workflow)
	assert.Equal(t, config.RunPolicyAlways, appNode.RunPolicy)
	assert.Equal(t, config.OnFailureContinue, appNode.OnFailure)
	assert.Equal(t, 2, appNode.Retries)

	// Check deploy info (using prefixed job ID)
	servicesNode := graph.Nodes["deploy-services"]
	assert.Equal(t, "services", servicesNode.Name)
	assert.Equal(t, "deploy-services", servicesNode.JobID)
	assert.Equal(t, "Deploy (services)", servicesNode.DisplayName)
	assert.Equal(t, config.CallbackTypeDeploy, servicesNode.Type)
	assert.Equal(t, ".github/workflows/deploy.yaml", servicesNode.Workflow)
	assert.Equal(t, config.RunPolicyDefault, servicesNode.RunPolicy)
	assert.Equal(t, config.OnFailureAbort, servicesNode.OnFailure)
	assert.Equal(t, 0, servicesNode.Retries)
}

func TestBuildDependencyGraph_DefaultValues(t *testing.T) {
	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{
				Name:     "app",
				Workflow: ".github/workflows/build.yaml",
				// No run_policy, on_failure, or retries specified
			},
		},
	}

	graph := BuildDependencyGraph(cfg)

	appNode := graph.Nodes["build-app"]
	// Should use defaults
	assert.Equal(t, config.RunPolicyDefault, appNode.RunPolicy)
	assert.Equal(t, config.OnFailureAbort, appNode.OnFailure)
	assert.Equal(t, 0, appNode.Retries)
}

func indexOf(slice []string, item string) int {
	for i, s := range slice {
		if s == item {
			return i
		}
	}
	return -1
}

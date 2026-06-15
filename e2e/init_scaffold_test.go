package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/e2e/harness"
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/scaffold"
)

// TestInitScaffoldOrchestratesAndPromotes proves that a project scaffolded by
// cascade init produces a pipeline that runs end to end, not just one that
// parses. It renders the real two-environment [dev prod] topology with
// scaffold.Scaffold, drives orchestrate and two promotes through act and gitea
// against the exact manifest the scaffold wrote, and asserts the build and
// deploy callbacks run and state advances dev -> release -> prod.
//
// The scaffold package's SelfCheck already proves the rendered manifest parses,
// validates, and generates. This test closes the remaining gap: it proves the
// scaffolded manifest drives a pipeline that actually executes, catching
// manifest-shape drift a static self-check cannot see. The harness Config is
// unmarshaled from the live scaffold manifest, so a change to the scaffold's
// manifest shape is exercised here automatically rather than silently diverging
// from a hand-copied fixture.
//
// The callback workflow bodies are supplied through SetupWorkflows rather than
// reusing the scaffold's own build.yaml and deploy.yaml. The scaffold stubs use
// actions/checkout, which is correct on real GitHub but cannot run in this
// harness: act resolves a reusable callback's actions/checkout by cloning the
// action from the per-scenario gitea, which rejects the unauthenticated clone.
// The substitute callbacks mirror the scaffold contract the generator depends
// on (the build emits an artifact_id output; both accept the environment, sha,
// and dry_run inputs the generated workflows pass through) while staying inside
// the harness's no-checkout constraint, the same pattern the existing runtime
// scenarios use.
func TestInitScaffoldOrchestratesAndPromotes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E tests")
	}

	const project = "cascade-init-demo"
	envs := []string{"dev", "prod"}

	// Render the real scaffold output. This is the exact manifest a user gets
	// from cascade init for the two-env topology.
	files, err := scaffold.Scaffold(project, "main", envs)
	require.NoError(t, err, "scaffold render failed")

	manifestBody, ok := files[config.DefaultManifestFile]
	require.True(t, ok, "scaffold did not produce %s", config.DefaultManifestFile)

	// Parse the scaffolded manifest's ci.config block into the harness Config so
	// the scenario runs against exactly what the scaffold wrote, with no second
	// hand-maintained copy of the configuration to drift out of sync.
	cfg, err := configFromScaffoldedManifest(manifestBody)
	require.NoError(t, err, "parsing scaffolded manifest into harness config")

	require.Equal(t, "main", cfg.TrunkBranch, "scaffolded trunk branch")
	require.Equal(t, envs, cfg.Environments, "scaffolded environments")
	require.Len(t, cfg.Builds, 1, "scaffolded builds")
	require.Len(t, cfg.Deploys, 1, "scaffolded deploys")
	require.Equal(t, "build", cfg.Builds[0].Name, "scaffolded build name")
	require.Equal(t, "deploy", cfg.Deploys[0].Name, "scaffolded deploy name")

	// act keys a job by the reusable callback's inner job id, not the caller's
	// generated job id. The scaffold names both the manifest callback and the
	// inner job "build" and "deploy", so the callbacks below keep those inner job
	// ids and the assertions reference them directly.
	const buildJob = "build"
	const deployJob = "deploy"

	scenario := &harness.MultiStepScenario{
		Name:        "Init Scaffold Two-Env Orchestrate and Promote",
		Description: "init-scaffolded [dev prod] topology orchestrates and promotes through act and gitea",
		Config:      cfg,
		SetupWorkflows: map[string]string{
			cfg.Builds[0].Workflow:  initBuildCallback,
			cfg.Deploys[0].Workflow: initDeployCallback,
		},
		Steps: []harness.Step{
			{
				Name:   "Initial feature commit",
				Action: "commit",
				Commit: &harness.CommitStep{
					Message: "feat: initial feature",
					Files: map[string]string{
						"src/app.ts": "export const version = \"0.1.0\";\n",
					},
				},
			},
			{
				Name:   "Orchestrate dev after initial commit",
				Action: "orchestrate",
				Expect: &harness.StepExpect{
					State: map[string]*harness.StateExpect{
						"dev": {
							SHA:     "commit1",
							Version: "v0.1.0-rc.0",
						},
					},
					Jobs: map[string]string{
						buildJob: "success",
					},
					Releases: []harness.ReleaseExpectStep{
						{Tag: "v0.1.0-rc.0", Prerelease: true, Draft: true},
					},
					Tags: &harness.TagsExpect{Exist: []string{"v0.1.0-rc.0"}},
				},
			},
			{
				Name:   "Promote dev to release",
				Action: "promote",
				Promote: &harness.PromoteStep{
					Mode: "default",
				},
				Expect: &harness.StepExpect{
					State: map[string]*harness.StateExpect{
						"release": {
							SHA:     "commit1",
							Version: "v0.1.0",
						},
						"dev": {Unchanged: true},
					},
					Releases: []harness.ReleaseExpectStep{
						{Tag: "v0.1.0", Prerelease: false, Draft: false, Latest: true},
					},
					Tags: &harness.TagsExpect{
						Exist:   []string{"v0.1.0"},
						Deleted: []string{"v0.1.0-rc.0"},
					},
				},
			},
			{
				Name:   "Promote release to prod",
				Action: "promote",
				Promote: &harness.PromoteStep{
					Mode: "default",
				},
				Expect: &harness.StepExpect{
					State: map[string]*harness.StateExpect{
						"prod": {
							SHA:     "commit1",
							Version: "v0.1.0",
							Deploys: map[string]*harness.DeployExpect{
								cfg.Deploys[0].Name: {SHA: "commit1"},
							},
						},
					},
					Jobs: map[string]string{
						deployJob: "success",
					},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	err = harness.RunMultiStepScenario(ctx, t, scenario)
	require.NoError(t, err, "init scaffold scenario failed")
}

// configFromScaffoldedManifest unmarshals the ci.config block of a scaffolded
// cascade manifest into the e2e harness Config. The scaffolded manifest is the
// real cascade manifest shape (ci -> config), and harness.Config mirrors that
// config block, so a direct unmarshal binds the scenario to the live scaffold
// output without a second hand-maintained configuration.
func configFromScaffoldedManifest(manifest string) (harness.Config, error) {
	var doc struct {
		CI struct {
			Config harness.Config `yaml:"config"`
		} `yaml:"ci"`
	}
	if err := yaml.Unmarshal([]byte(manifest), &doc); err != nil {
		return harness.Config{}, err
	}
	return doc.CI.Config, nil
}

// initBuildCallback mirrors the scaffold build stub's contract (the workflow_call
// inputs and the artifact_id output the generated orchestrate workflow wires up)
// without actions/checkout, which act cannot resolve for a reusable callback
// against the per-scenario gitea. Its inner job id is build, matching the
// scaffolded build name, so act keys it as build.
const initBuildCallback = `name: Build cascade-init-demo
on:
  workflow_call:
    inputs:
      environment:
        type: string
        required: true
      sha:
        type: string
        required: true
      dry_run:
        type: boolean
        required: false
        default: false
    outputs:
      artifact_id:
        value: ${{ jobs.build.outputs.artifact_id }}
jobs:
  build:
    runs-on: ubuntu-latest
    outputs:
      artifact_id: ${{ steps.placeholder.outputs.artifact_id }}
    steps:
      - id: placeholder
        run: |
          echo "build env=${{ inputs.environment }} sha=${{ inputs.sha }}"
          echo "artifact_id=placeholder-${{ inputs.sha }}" >> "$GITHUB_OUTPUT"
`

// initDeployCallback mirrors the scaffold deploy stub's contract (the
// workflow_call inputs the generated promote workflow passes through) without
// actions/checkout. Its inner job id is deploy, matching the scaffolded deploy
// name, so act keys it as deploy.
const initDeployCallback = `name: Deploy cascade-init-demo
on:
  workflow_call:
    inputs:
      environment:
        type: string
        required: true
      sha:
        type: string
        required: true
      dry_run:
        type: boolean
        required: false
        default: false
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - run: echo "deploy env=${{ inputs.environment }} sha=${{ inputs.sha }}"
`

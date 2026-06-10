package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"gopkg.in/yaml.v3"
)

// RepoContext tracks per-repo state in multi-repo scenarios
type RepoContext struct {
	Name       string              // Logical name (e.g., "primary-backend", "cdk-infra")
	Repo       *Repo               // Gitea repo reference
	Config     *config.TrunkConfig // CICD configuration
	HeadSHA    string              // Current HEAD SHA
	ExecCtx    *ExecutionContext   // Per-repo execution context
	IsPrimary  bool                // Whether this is the primary repo
	Satellites []string            // Names of satellite repos (if primary)
	Primary    string              // Name of primary repo (if satellite)
}

// MultiRepoHarness extends Harness for cross-repo testing
type MultiRepoHarness struct {
	t           *testing.T
	networkName string
	network     interface{} // *testcontainers.DockerNetwork
	gitea       *GiteaContainer
	act         *ActRunner
	repos       map[string]*RepoContext // name -> repo context
	primaryRepo string                  // name of primary repo
}

// NewMultiRepoHarness creates a harness for multi-repo testing
func NewMultiRepoHarness(t *testing.T) *MultiRepoHarness {
	return &MultiRepoHarness{
		t:     t,
		repos: make(map[string]*RepoContext),
	}
}

// SetupInfra starts Gitea and Act containers for multi-repo testing
func (h *MultiRepoHarness) SetupInfra(ctx context.Context) error {
	// Create base harness for infrastructure setup
	base := New(h.t)
	if err := base.SetupInfra(ctx); err != nil {
		return fmt.Errorf("failed to setup infrastructure: %w", err)
	}

	h.gitea = base.gitea
	h.act = base.act
	h.networkName = base.networkName
	h.network = base.network

	return nil
}

// MultiRepoSetup defines setup for a single repo in multi-repo scenario
type MultiRepoSetup struct {
	Name       string                 // Logical name for the repo
	Config     *config.TrunkConfig    // CICD configuration
	Commits    []Commit               // Initial commits
	Tags       []string               // Initial tags
	Manifest   map[string]interface{} // Initial manifest state
	IsPrimary  bool                   // Whether this is the primary
	Satellites []string               // Satellite repo names (if primary)
	Primary    string                 // Primary repo name (if satellite)
}

// CreateRepo creates a single repo with the given setup
func (h *MultiRepoHarness) CreateRepo(ctx context.Context, setup MultiRepoSetup) (*RepoContext, error) {
	if h.gitea == nil {
		return nil, fmt.Errorf("infrastructure not initialized, call SetupInfra first")
	}

	// Create unique repo name in Gitea
	giteaRepoName := strings.ReplaceAll(setup.Name, "/", "-")
	repo, err := h.gitea.CreateRepo(ctx, giteaRepoName)
	if err != nil {
		return nil, fmt.Errorf("failed to create repo %s: %w", setup.Name, err)
	}

	repoCtx := &RepoContext{
		Name:       setup.Name,
		Repo:       repo,
		Config:     setup.Config,
		ExecCtx:    NewExecutionContext(),
		IsPrimary:  setup.IsPrimary,
		Satellites: setup.Satellites,
		Primary:    setup.Primary,
	}

	// Create initial files based on config
	initialFiles := make(map[string]string)

	// Add CICD config file
	if setup.Config != nil {
		// Wrap in ci: structure for manifest
		manifest := map[string]interface{}{
			"ci": map[string]interface{}{
				"config": setup.Config,
			},
		}

		// Merge in any provided manifest state
		if setup.Manifest != nil {
			if ci, ok := manifest["ci"].(map[string]interface{}); ok {
				for k, v := range setup.Manifest {
					if k == "ci" {
						if ciState, ok := v.(map[string]interface{}); ok {
							for ck, cv := range ciState {
								ci[ck] = cv
							}
						}
					} else {
						ci[k] = v
					}
				}
			}
		}

		configYAML, err := yaml.Marshal(manifest)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal config: %w", err)
		}
		initialFiles[".github/cicd.yaml"] = string(configYAML)

		// Create stub workflow files for builds and deploys, tagged with the
		// repo name so multi-repo scenarios running in parallel don't collide
		// on shared act container names.
		repoTag := scenarioTagFromTestName(setup.Name)
		for _, build := range setup.Config.Builds {
			if build.Workflow != "" && !strings.Contains(build.Workflow, "/") {
				initialFiles[build.Workflow] = generateStubWorkflow(build.Name, repoTag)
			}
		}
		for _, deploy := range setup.Config.Deploys {
			if deploy.Workflow != "" && !strings.Contains(deploy.Workflow, "/") {
				initialFiles[deploy.Workflow] = generateStubWorkflow(deploy.Name, repoTag)
			}
		}
	}

	// Create mock setup-cli action
	initialFiles[".github/actions/setup-cli/action.yaml"] = mockSetupCLIAction

	// Create mock manage-release action
	initialFiles[".github/actions/manage-release/action.yaml"] = mockManageReleaseAction

	// Create initial commit with all files
	if len(initialFiles) > 0 {
		sha, err := h.gitea.CreateCommit(ctx, repo, "chore: initial setup", initialFiles)
		if err != nil {
			return nil, fmt.Errorf("failed to create initial commit: %w", err)
		}
		repoCtx.HeadSHA = sha
		repoCtx.ExecCtx.RecordCommit("initial", sha)
		repoCtx.ExecCtx.RecordCommitMessage(sha, "chore: initial setup")
	}

	// Create additional commits
	for i, commit := range setup.Commits {
		sha, err := h.gitea.CreateCommit(ctx, repo, commit.Message, commit.Files)
		if err != nil {
			return nil, fmt.Errorf("failed to create commit %d: %w", i, err)
		}
		repoCtx.HeadSHA = sha
		repoCtx.ExecCtx.RecordCommit(fmt.Sprintf("commit-%d", i), sha)
		repoCtx.ExecCtx.RecordCommitMessage(sha, commit.Message)
	}

	// Create initial tags
	for _, tag := range setup.Tags {
		if err := h.gitea.CreateTag(ctx, repo, tag, repoCtx.HeadSHA); err != nil {
			return nil, fmt.Errorf("failed to create tag %s: %w", tag, err)
		}
		repoCtx.ExecCtx.RecordTag(tag, true)
	}

	// Store in harness
	h.repos[setup.Name] = repoCtx
	if setup.IsPrimary {
		h.primaryRepo = setup.Name
	}

	return repoCtx, nil
}

// SetupPrimarySatellite creates primary + satellite repos with proper configuration
func (h *MultiRepoHarness) SetupPrimarySatellite(ctx context.Context, primary MultiRepoSetup, satellites ...MultiRepoSetup) error {
	// Ensure primary is marked correctly
	primary.IsPrimary = true
	if primary.Satellites == nil {
		primary.Satellites = make([]string, len(satellites))
		for i, sat := range satellites {
			primary.Satellites[i] = sat.Name
		}
	}

	// Create primary repo first
	_, err := h.CreateRepo(ctx, primary)
	if err != nil {
		return fmt.Errorf("failed to create primary repo: %w", err)
	}

	// Create satellite repos
	for _, sat := range satellites {
		sat.Primary = primary.Name
		_, err := h.CreateRepo(ctx, sat)
		if err != nil {
			return fmt.Errorf("failed to create satellite repo %s: %w", sat.Name, err)
		}
	}

	return nil
}

// GetRepo returns a repo context by name
func (h *MultiRepoHarness) GetRepo(name string) *RepoContext {
	return h.repos[name]
}

// GetPrimaryRepo returns the primary repo context
func (h *MultiRepoHarness) GetPrimaryRepo() *RepoContext {
	if h.primaryRepo == "" {
		return nil
	}
	return h.repos[h.primaryRepo]
}

// GetSatelliteRepos returns all satellite repo contexts
func (h *MultiRepoHarness) GetSatelliteRepos() []*RepoContext {
	var satellites []*RepoContext
	for _, repo := range h.repos {
		if !repo.IsPrimary && repo.Primary != "" {
			satellites = append(satellites, repo)
		}
	}
	return satellites
}

// Gitea returns the underlying Gitea container
func (h *MultiRepoHarness) Gitea() *GiteaContainer {
	return h.gitea
}

// Act returns the underlying Act runner
func (h *MultiRepoHarness) Act() *ActRunner {
	return h.act
}

// Cleanup terminates all containers
func (h *MultiRepoHarness) Cleanup(ctx context.Context) {
	if h.act != nil {
		_ = h.act.Terminate(ctx)
	}
	if h.gitea != nil {
		_ = h.gitea.Terminate(ctx)
	}
}

// CommitToRepo creates a commit in a specific repo
func (h *MultiRepoHarness) CommitToRepo(ctx context.Context, repoName string, message string, files map[string]string) (string, error) {
	repo := h.repos[repoName]
	if repo == nil {
		return "", fmt.Errorf("repo %s not found", repoName)
	}

	sha, err := h.gitea.CreateCommit(ctx, repo.Repo, message, files)
	if err != nil {
		return "", fmt.Errorf("failed to create commit: %w", err)
	}

	repo.HeadSHA = sha
	repo.ExecCtx.RecordCommit("latest", sha)
	repo.ExecCtx.RecordCommitMessage(sha, message)

	return sha, nil
}

// CreateTagInRepo creates a tag in a specific repo
func (h *MultiRepoHarness) CreateTagInRepo(ctx context.Context, repoName string, tag string) error {
	repo := h.repos[repoName]
	if repo == nil {
		return fmt.Errorf("repo %s not found", repoName)
	}

	if err := h.gitea.CreateTag(ctx, repo.Repo, tag, repo.HeadSHA); err != nil {
		return fmt.Errorf("failed to create tag: %w", err)
	}

	repo.ExecCtx.RecordTag(tag, true)
	return nil
}

// GetTagsInRepo returns all tags in a specific repo
func (h *MultiRepoHarness) GetTagsInRepo(ctx context.Context, repoName string) ([]string, error) {
	repo := h.repos[repoName]
	if repo == nil {
		return nil, fmt.Errorf("repo %s not found", repoName)
	}

	return h.gitea.GetTags(ctx, repo.Repo)
}

// GetFileContentInRepo returns file content from a specific repo
func (h *MultiRepoHarness) GetFileContentInRepo(ctx context.Context, repoName string, filepath string) (string, error) {
	repo := h.repos[repoName]
	if repo == nil {
		return "", fmt.Errorf("repo %s not found", repoName)
	}

	return h.gitea.GetFileContent(ctx, repo.Repo, filepath)
}

// RunWorkflowInRepo executes a workflow in a specific repo
func (h *MultiRepoHarness) RunWorkflowInRepo(ctx context.Context, repoName string, opts RunOpts) (*ExtendedWorkflowResult, error) {
	repo := h.repos[repoName]
	if repo == nil {
		return nil, fmt.Errorf("repo %s not found", repoName)
	}

	if h.act == nil {
		// Return simulated success if no Act runner
		return &ExtendedWorkflowResult{
			Conclusion: "success",
			Jobs:       make(map[string]*JobResultExtended),
		}, nil
	}

	// Clone the repo into act container and run workflow
	return h.act.RunWorkflow(ctx, opts)
}

// SimulateCrossRepoDispatch simulates workflow_dispatch from one repo to another
// This is used to test satellite -> primary notification flows
func (h *MultiRepoHarness) SimulateCrossRepoDispatch(ctx context.Context, sourceRepo, targetRepo, workflow string, inputs map[string]string) error {
	source := h.repos[sourceRepo]
	if source == nil {
		return fmt.Errorf("source repo %s not found", sourceRepo)
	}

	target := h.repos[targetRepo]
	if target == nil {
		return fmt.Errorf("target repo %s not found", targetRepo)
	}

	// Validate the dispatch makes sense
	if target.Config != nil && !target.Config.IsPrimary() {
		// Allow dispatch to non-primary for flexibility, but log warning
		h.t.Logf("Warning: dispatching to non-primary repo %s", targetRepo)
	}

	// Run the external-update workflow (or specified workflow) in target repo
	opts := RunOpts{
		WorkflowPath: workflow,
		Event:        "workflow_dispatch",
		Inputs:       inputs,
	}

	_, err := h.RunWorkflowInRepo(ctx, targetRepo, opts)
	if err != nil {
		return fmt.Errorf("failed to run workflow in target repo: %w", err)
	}

	// If this is an external-update dispatch, update the target's external state
	if strings.Contains(workflow, "external-update") {
		if err := h.updateExternalState(ctx, targetRepo, inputs); err != nil {
			return fmt.Errorf("failed to update external state: %w", err)
		}
	}

	return nil
}

// updateExternalState updates the external state in the target repo's manifest
func (h *MultiRepoHarness) updateExternalState(ctx context.Context, repoName string, inputs map[string]string) error {
	repo := h.repos[repoName]
	if repo == nil {
		return fmt.Errorf("repo %s not found", repoName)
	}

	deployName := inputs["deploy_name"]
	env := inputs["environment"]
	sha := inputs["sha"]
	version := inputs["version"]

	if deployName == "" || env == "" {
		return nil // Not enough info to update state
	}

	// Record in execution context
	repo.ExecCtx.RecordExternalDeployState(env, deployName, sha, version)

	return nil
}

// Mock action content for setup-cli
const mockSetupCLIAction = `name: 'Setup CLI (Mock)'
description: 'Mock setup-cli action for E2E tests'
inputs:
  token:
    description: 'GitHub token (unused in mock)'
    required: false
  version:
    description: 'CLI version (unused in mock)'
    required: false
    default: 'local'
runs:
  using: 'composite'
  steps:
    - name: Install CLI from repo
      shell: bash
      run: |
        if [ -f "$GITHUB_WORKSPACE/.github/bin/cascade" ]; then
          sudo cp "$GITHUB_WORKSPACE/.github/bin/cascade" /usr/local/bin/cascade
          sudo chmod +x /usr/local/bin/cascade
          cascade version || echo "CLI version check completed"
        else
          echo "ERROR: CLI binary not found"
          exit 1
        fi
`

// Mock action content for manage-release
const mockManageReleaseAction = `name: 'Manage Release (Mock)'
description: 'Mock manage-release action for E2E tests - handles RC cleanup'
inputs:
  repo:
    description: 'Repository'
    required: true
  action:
    description: 'Action to perform'
    required: true
  tag:
    description: 'Release tag'
    required: true
  create_tag:
    description: 'Create tag'
    required: false
  environment:
    description: 'Environment'
    required: false
  sha:
    description: 'SHA'
    required: false
  changelog:
    description: 'Changelog'
    required: false
  token:
    description: 'Token'
    required: false
runs:
  using: 'composite'
  steps:
    - name: Mock release management
      shell: bash
      run: |
        echo "Mock manage-release: ${{ inputs.action }} tag ${{ inputs.tag }}"
        if [[ "${{ inputs.create_tag }}" == "true" ]] || [[ "${{ inputs.action }}" == "publish" ]]; then
          TAG="${{ inputs.tag }}"
          SHA="${{ inputs.sha }}"
          if [[ -z "$SHA" ]]; then SHA="HEAD"; fi
          git tag -f "$TAG" "$SHA" 2>/dev/null || git tag "$TAG" "$SHA"
          git push origin "$TAG" --force 2>/dev/null || true
        fi

        # Clean up RC tags when publishing a final release
        if [[ "${{ inputs.action }}" == "publish" ]]; then
          TAG="${{ inputs.tag }}"
          # Only clean up if this is a final release (not an RC itself)
          if [[ ! "$TAG" =~ -rc\.[0-9]+$ ]]; then
            echo "Cleaning up RC tags for $TAG..."
            for rc_tag in $(git tag -l "${TAG}-rc.*"); do
              echo "Deleting RC tag: $rc_tag"
              git push origin --delete "$rc_tag" 2>/dev/null || true
              git tag -d "$rc_tag" 2>/dev/null || true
            done
          fi
        fi
`

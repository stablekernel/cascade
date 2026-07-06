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
	// base is the single-repo Harness created during SetupInfra. Its act/gitea
	// bound helpers (GenerateWorkflows, ensureCLIBinary, localizeWorkflows,
	// waitForBranchHead, ...) are reused per repo by re-pointing base.repo. We
	// keep one instance so the sync.Once CLI build happens exactly once.
	base *Harness
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
	// Keep the base Harness so its act/gitea-bound single-repo helpers can be
	// reused for real per-repo workflow generation and runs. base.gitea/base.act
	// already equal h.gitea/h.act, so re-pointing base.repo is all that is needed
	// to target a given repo.
	h.base = base

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
		// Build the manifest the CLI expects: top-level `ci:` wrapping both the
		// pipeline `config:` and the initial `state:`. The single-repo harness
		// only wraps `ci.config`, but the `cascade external update` verb reads
		// `ci.state.<env>` and fails if it is absent, so the multi-repo manifest
		// must carry the scenario's state block under `ci.state` as well.
		ci := map[string]interface{}{
			"config": setup.Config,
		}
		if state := extractManifestState(setup.Manifest); state != nil {
			ci["state"] = state
		}
		manifest := map[string]interface{}{"ci": ci}

		configYAML, err := yaml.Marshal(manifest)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal config: %w", err)
		}
		// Write to .github/manifest.yaml (the CLI's auto-detected default). The
		// previous .github/cicd.yaml path was never read by generate-workflow or
		// the external update verb.
		initialFiles[".github/manifest.yaml"] = string(configYAML)

		// Create stub workflow files for local builds and deploys, tagged with
		// the repo name so multi-repo scenarios running in parallel don't collide
		// on shared act container names. generate-workflow reads each local
		// reusable workflow at its literal path (e.g. .github/workflows/deploy.yaml)
		// to discover inputs/outputs, so a stub must exist there; the previous
		// guard skipped any path containing a slash, which dropped every
		// .github/workflows/*.yaml stub and broke generation. External (cross-repo)
		// deploys live under config.External and are intentionally not stubbed
		// here - they are referenced, not run, in the primary's repo.
		repoTag := scenarioTagFromTestName(setup.Name)
		for _, build := range setup.Config.Builds {
			if isLocalWorkflowPath(build.Workflow) {
				initialFiles[build.Workflow] = generateStubWorkflow(build.Name, repoTag)
			}
		}
		for _, deploy := range setup.Config.Deploys {
			if isLocalWorkflowPath(deploy.Workflow) {
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

	// Generate real workflows for this repo via `cascade generate-workflow`,
	// reusing the single-repo machinery bound to the shared act/gitea. This
	// produces orchestrate.yaml (carrying the Notify step for satellites) and,
	// for a primary with external repos, external-update.yaml. Generation clones
	// into the shared /tmp/repo and is destructive, so it must run while this
	// repo is the active target; later steps re-clone via
	// prepareRepoInActContainer before running anything.
	if setup.Config != nil && h.base != nil && h.act != nil {
		h.base.repo = repoCtx.Repo
		if err := h.base.GenerateWorkflows(ctx, false); err != nil {
			return nil, fmt.Errorf("failed to generate workflows for %s: %w", setup.Name, err)
		}
		// Refresh HEAD: GenerateWorkflows pushed a workflows commit.
		if head, err := h.gitea.getHeadSHA(ctx, repoCtx.Repo); err == nil && head != "" {
			repoCtx.HeadSHA = head
		}
	}

	// Store in harness
	h.repos[setup.Name] = repoCtx
	if setup.IsPrimary {
		h.primaryRepo = setup.Name
	}

	return repoCtx, nil
}

// isLocalWorkflowPath reports whether a build/deploy workflow reference is a
// local reusable workflow in this repo (under .github/workflows/) that needs a
// stub file, as opposed to an empty value or a cross-repo `owner/repo/...`
// reference.
func isLocalWorkflowPath(workflow string) bool {
	return workflow != "" && strings.HasPrefix(workflow, ".github/")
}

// extractManifestState pulls the `state` submap out of a scenario's Manifest
// block so it can be placed under `ci.state` in the generated manifest. The
// scenario YAML shapes the block as either `{state: {...}}` (the common case)
// or `{ci: {state: {...}}}`; both are handled. It returns nil when no state is
// present.
func extractManifestState(manifest map[string]interface{}) interface{} {
	if manifest == nil {
		return nil
	}
	if state, ok := manifest["state"]; ok {
		return state
	}
	if ci, ok := manifest["ci"].(map[string]interface{}); ok {
		if state, ok := ci["state"]; ok {
			return state
		}
	}
	return nil
}

// prepareRepoInActContainer re-clones the given repo fresh into the shared
// /tmp/repo and copies the cascade CLI binary into /tmp/repo/.github/bin. This
// mirrors the clone+copy that GenerateWorkflows performs (minus the generate),
// and is required before running any workflow under act because /tmp/repo holds
// whichever repo was generated or run last in the single shared act container.
func (h *MultiRepoHarness) prepareRepoInActContainer(ctx context.Context, repoCtx *RepoContext) error {
	if h.act == nil {
		return fmt.Errorf("act runner not initialized")
	}
	if h.base == nil {
		return fmt.Errorf("base harness not initialized")
	}

	externalURL := h.act.GiteaURL()
	externalHost := strings.TrimPrefix(strings.TrimPrefix(externalURL, "http://"), "https://")
	cloneURL := fmt.Sprintf("http://%s:%s@%s/%s/%s.git",
		AdminUsername, AdminPassword,
		externalHost,
		AdminUsername, repoCtx.Repo.Name)

	cloneCmd := []string{
		"bash", "-c",
		fmt.Sprintf("rm -rf /tmp/repo && git clone %s /tmp/repo", cloneURL),
	}
	exitCode, _, err := h.act.Container().Exec(ctx, cloneCmd)
	if err != nil || exitCode != 0 {
		return fmt.Errorf("failed to clone %s into act container (exit %d): %w", repoCtx.Name, exitCode, err)
	}

	// Copy the (already-built) CLI binary into the repo so the mock setup-cli
	// action can install it onto PATH inside job containers. The copy streams
	// the cached binary bytes (not the on-disk file) to avoid the archive/tar
	// size-mismatch race (#336).
	if _, err := h.base.ensureCLIBinary(ctx); err != nil {
		return err
	}
	if err := copyCLIToContainer(ctx, h.act.Container(), cliBinaryBytes, "/usr/local/bin/cascade"); err != nil {
		return err
	}
	copyToRepoCmd := []string{
		"bash", "-c",
		"mkdir -p /tmp/repo/.github/bin && cp /usr/local/bin/cascade /tmp/repo/.github/bin/cascade",
	}
	if _, _, err := h.act.Container().Exec(ctx, copyToRepoCmd); err != nil {
		return fmt.Errorf("failed to stage CLI in repo: %w", err)
	}

	// Recreate the master branch alias the generated workflows reference via
	// `@master` action refs, matching GenerateWorkflows.
	masterCmd := []string{
		"bash", "-c",
		"cd /tmp/repo && git branch master 2>/dev/null || git branch -f master HEAD",
	}
	_, _, _ = h.act.Container().Exec(ctx, masterCmd)

	return nil
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

// RealCrossRepoDispatch performs the REAL primary-side half of a satellite ->
// primary notification. The satellite's generated Notify step would dispatch
// the target repo's external-update.yaml with these inputs; gitea does not
// implement the GitHub Actions workflow_dispatch API, so that live network hop
// is a no-op under act (see the network-hop residue note below). The harness
// bridges it by running the target's external-update.yaml directly under act
// with the same inputs. That workflow checks out the target repo from gitea,
// runs `cascade external update`, and commits+pushes ci.state.<env>.external.<name>
// back to gitea. Assertions then read that real manifest.
//
// Network-hop residue: the satellite's generated Notify step calls
// github.rest.actions.createWorkflowDispatch against GITHUB_API_URL, which in
// e2e points at gitea. Gitea does not implement that API, so the live cross-repo
// dispatch is a no-op under act. We assert the generated Notify step CONTENT
// (RunSatelliteOrchestrateAndAssertNotify) plus the real verb effect here; the
// live dispatch network hop is real-GitHub-only residue.
func (h *MultiRepoHarness) RealCrossRepoDispatch(ctx context.Context, sourceRepo, targetRepo, workflow string, inputs map[string]string) error {
	source := h.repos[sourceRepo]
	if source == nil {
		return fmt.Errorf("source repo %s not found", sourceRepo)
	}

	target := h.repos[targetRepo]
	if target == nil {
		return fmt.Errorf("target repo %s not found", targetRepo)
	}

	if target.Config != nil && !target.Config.IsPrimary() {
		h.t.Logf("Warning: dispatching to non-primary repo %s", targetRepo)
	}

	// Re-clone the target (primary) into /tmp/repo so the external-update run
	// operates on the right repo regardless of what was generated/run last.
	if err := h.prepareRepoInActContainer(ctx, target); err != nil {
		return fmt.Errorf("prepare primary %s for external-update: %w", targetRepo, err)
	}
	h.base.repo = target.Repo

	opts := RunOpts{
		WorkflowPath: workflow,
		Event:        "workflow_dispatch",
		Inputs:       inputs,
	}
	result, err := h.act.RunWorkflowFromRepo(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to run external-update workflow in %s: %w", targetRepo, err)
	}
	if result.Conclusion != "success" {
		return fmt.Errorf("external-update workflow in %s concluded %q: %s\n%s",
			targetRepo, result.Conclusion, result.Error, result.Logs)
	}

	// The verb committed+pushed the manifest back to gitea. Refresh the target's
	// recorded HEAD so subsequent reads/operations see the new tip.
	if head, err := h.gitea.getHeadSHA(ctx, target.Repo); err == nil && head != "" {
		target.HeadSHA = head
	}

	return nil
}

// RunSatelliteOrchestrateAndAssertNotify asserts that the satellite's generated
// orchestrate.yaml carries the real Notify step that dispatches the primary's
// external-update workflow. We assert the generated CONTENT rather than running
// the satellite orchestrate to green: the Notify step's createWorkflowDispatch
// call targets the GitHub Actions API (GITHUB_API_URL), which points at gitea in
// e2e and is not implemented there, so the live dispatch is a documented no-op
// (the network hop is bridged by RealCrossRepoDispatch). Asserting the generated
// step is the maximal observable subset under gitea/act and is far cheaper than
// a full orchestrate run.
func (h *MultiRepoHarness) RunSatelliteOrchestrateAndAssertNotify(ctx context.Context, satelliteName string) error {
	sat := h.repos[satelliteName]
	if sat == nil {
		return fmt.Errorf("satellite repo %s not found", satelliteName)
	}
	if sat.Config == nil || sat.Config.Notify == nil {
		return nil // Not a notifying satellite; nothing to assert.
	}

	content, err := h.gitea.GetFileContent(ctx, sat.Repo, ".github/workflows/orchestrate.yaml")
	if err != nil {
		return fmt.Errorf("read generated orchestrate.yaml for %s: %w", satelliteName, err)
	}

	// The generated Notify step targets the primary repo named in notify.repo.
	repoParts := strings.SplitN(sat.Config.Notify.Repo, "/", 2)
	wantOwner, wantName := "", ""
	if len(repoParts) == 2 {
		wantOwner, wantName = repoParts[0], repoParts[1]
	}
	wantWorkflow := sat.Config.Notify.GetWorkflow()

	required := []string{
		"Notify Primary Repo",
		"createWorkflowDispatch",
		fmt.Sprintf("workflow_id: '%s'", wantWorkflow),
	}
	if wantOwner != "" {
		required = append(required, fmt.Sprintf("owner: '%s'", wantOwner))
	}
	if wantName != "" {
		required = append(required, fmt.Sprintf("repo: '%s'", wantName))
	}

	for _, want := range required {
		if !strings.Contains(content, want) {
			return fmt.Errorf("satellite %s orchestrate.yaml missing notify content %q", satelliteName, want)
		}
	}

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

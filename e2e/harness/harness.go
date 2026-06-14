package harness

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/network"
	"gopkg.in/yaml.v3"
)

// Parallel scenarios used to race on a shared `/tmp/cascade-binary`: one test
// would start `go build` (truncating + rewriting the file) while another was
// reading it via CopyFileToContainer, producing a corrupt or zero-byte binary
// inside the act container. The CLI then either failed silently or generated
// an empty workflow set, leaving `.github/workflows/orchestrate.yaml` absent
// and downstream orchestrate steps to fail with "No such file or directory".
// Build once per process under sync.Once and reuse a stable, package-private
// path; all scenarios share the same source so the binary is identical.
var (
	cliBinaryOnce sync.Once
	cliBinaryPath string
	cliBinaryErr  error
)

// normalizeCallbackStubPath returns the canonical path where a callback stub
// workflow should be placed so it matches the path the generator will reference.
// Cross-repo external refs (containing "@") are skipped - they are not stubs.
// All other paths are placed under .github/workflows/ using the base filename.
func normalizeCallbackStubPath(workflow string) string {
	if strings.Contains(workflow, "@") {
		return ""
	}
	return ".github/workflows/" + filepath.Base(workflow)
}

// Harness orchestrates E2E test execution
type Harness struct {
	t           *testing.T
	networkName string
	network     *testcontainers.DockerNetwork
	gitea       *GiteaContainer
	act         *ActRunner
	repo        *Repo
}

// New creates a new test harness
func New(t *testing.T) *Harness {
	return &Harness{t: t}
}

// SetupInfra starts Gitea and Act containers
func (h *Harness) SetupInfra(ctx context.Context) error {
	var err error

	// Create shared network for container communication
	// network.New generates a unique network name automatically
	h.network, err = network.New(ctx)
	if err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}
	h.networkName = h.network.Name

	h.gitea, err = NewGiteaContainer(ctx, h.networkName, h.network)
	if err != nil {
		return fmt.Errorf("failed to start gitea: %w", err)
	}

	// Use the gitea container's network alias + internal port so both the act
	// container AND act's spawned job containers can reach Gitea via the
	// shared docker network. This replaces the previous host.docker.internal
	// approach, which only resolves automatically on Docker Desktop and
	// requires `--add-host=host.docker.internal:host-gateway` plumbing on
	// Linux runners (and even with that, act job containers' connectivity
	// stayed flaky in CI). Network alias resolution works uniformly.
	giteaURL := "http://gitea:3000"
	giteaToken := h.gitea.AdminToken()

	h.act, err = NewActRunner(ctx, giteaURL, giteaToken, h.networkName, h.network)
	if err != nil {
		_ = h.gitea.Terminate(ctx)
		_ = h.network.Remove(ctx)
		return fmt.Errorf("failed to start act: %w", err)
	}

	return nil
}

// StageRepoFromConfig creates a repo with the given config for multi-step scenarios
// StageRepoFromConfig creates the test repo, writes the manifest and the stub
// callback workflows derived from config, then runs workflow generation. The
// optional setupWorkflows map seeds additional reusable callback workflow files
// (keyed by repository path) into the same setup commit before generation, and
// overrides any auto-generated stub at the same path. Scenarios use it to supply
// a callback body the generic stub cannot express (for example a deploy that
// fails only under the Rollback workflow); files staged via a later step's
// commit.files would land after generation and never be read by the generator.
func (h *Harness) StageRepoFromConfig(ctx context.Context, config Config, setupWorkflows map[string]string) error {
	var err error

	// Create repo
	h.repo, err = h.gitea.CreateRepo(ctx, "test-repo")
	if err != nil {
		return fmt.Errorf("failed to create repo: %w", err)
	}

	// Create manifest.yaml from scenario config
	// The CLI expects the structure: ci: -> config: -> trunk_branch, environments, etc.
	if config.TrunkBranch != "" || len(config.Environments) > 0 {
		// Wrap config in "ci.config" structure as expected by the CLI
		manifest := map[string]interface{}{
			"ci": map[string]interface{}{
				"config": config,
			},
		}
		configYAML, err := yaml.Marshal(manifest)
		if err != nil {
			return fmt.Errorf("failed to marshal config: %w", err)
		}

		// Create initial files including manifest and stub workflow files
		files := map[string]string{
			".github/manifest.yaml": string(configYAML),
		}

		// Create stub workflow files for builds and deploys.
		// These are reusable workflows that the CLI reads to discover inputs/outputs.
		// We tag each stub's `name:` with a per-scenario suffix so act's
		// auto-generated job container hash diverges across parallel scenarios.
		// otherwise multiple scenarios with the same build/deploy names race on
		// /act-Build-app-app-app-<hash> at the host docker level.
		scenarioTag := scenarioTagFromTestName(h.t.Name())
		for _, build := range config.Builds {
			if build.Workflow != "" {
				if p := normalizeCallbackStubPath(build.Workflow); p != "" {
					files[p] = generateStubWorkflow(build.Name, scenarioTag)
				}
			}
		}
		for _, deploy := range config.Deploys {
			if deploy.Workflow != "" {
				if p := normalizeCallbackStubPath(deploy.Workflow); p != "" {
					files[p] = generateStubWorkflow(deploy.Name, scenarioTag)
				}
			}
		}
		if config.Publish != nil && config.Publish.Workflow != "" {
			if p := normalizeCallbackStubPath(config.Publish.Workflow); p != "" {
				files[p] = generatePublishStubWorkflow(scenarioTag)
			}
		}
		// A custom changelog workflow is a reusable workflow invoked as a
		// job-level uses:. Stub it so the generated changelog job resolves and
		// exposes a changelog output for the release step to consume.
		if wf, ok := config.Changelog["workflow"].(string); ok && wf != "" {
			if p := normalizeCallbackStubPath(wf); p != "" {
				files[p] = generateChangelogStubWorkflow(scenarioTag)
			}
		}
		// A top-level validate callback is a reusable workflow the generated
		// orchestrate.yaml invokes as a job-level uses:. Stub it so the generator
		// can read the referenced workflow at generation time and emit the validate
		// gate. Without a seeded stub the generator fails reading validate.yaml,
		// since the file would otherwise only arrive via a later step commit.
		if wf, ok := config.Validate["workflow"].(string); ok && wf != "" {
			if p := normalizeCallbackStubPath(wf); p != "" {
				files[p] = generateValidateStubWorkflow(scenarioTag)
			}
		}

		// Seed scenario-supplied reusable callback workflows last so they override
		// any auto-generated stub at the same path. These bodies express behavior
		// the generic stub cannot (for example a deploy that exits non-zero only
		// under the Rollback workflow) and must be present before generation reads
		// the referenced workflows.
		for path, body := range setupWorkflows {
			files[path] = body
		}

		// Create mock setup-cli action that installs CLI from repo
		// The generated workflows reference stablekernel/cascade/.github/actions/setup-cli
		// In the test environment, we copy the pre-built CLI from the repo to PATH
		files[".github/actions/setup-cli/action.yaml"] = `name: 'Setup CLI (Mock)'
description: 'Mock setup-cli action for E2E tests - installs CLI from repo'
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
          echo "Installing cascade CLI from repo..."
          sudo cp "$GITHUB_WORKSPACE/.github/bin/cascade" /usr/local/bin/cascade
          sudo chmod +x /usr/local/bin/cascade
          echo "CLI installed successfully"
          cascade version || echo "CLI version check completed"
        else
          echo "ERROR: CLI binary not found at $GITHUB_WORKSPACE/.github/bin/cascade"
          ls -la "$GITHUB_WORKSPACE/.github/" || true
          exit 1
        fi
`

		// Create mock manage-release action that creates real tags and cleans up RC tags
		files[".github/actions/manage-release/action.yaml"] = `name: 'Manage Release (Mock)'
description: 'Mock manage-release action for E2E tests - creates real tags and handles RC cleanup'
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
  previous_tag:
    description: 'Previous tag'
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
        echo "  Environment: ${{ inputs.environment }}"
        echo "  SHA: ${{ inputs.sha }}"
        echo "  Create tag: ${{ inputs.create_tag }}"

        # Create and push tag if requested
        if [[ "${{ inputs.create_tag }}" == "true" ]] || [[ "${{ inputs.action }}" == "create-draft" ]] || [[ "${{ inputs.action }}" == "publish" ]]; then
          TAG="${{ inputs.tag }}"
          SHA="${{ inputs.sha }}"
          if [[ -z "$SHA" ]]; then
            SHA="HEAD"
          fi
          echo "Creating tag $TAG at $SHA..."
          git tag -f "$TAG" "$SHA" 2>/dev/null || git tag "$TAG" "$SHA"
          git push origin "$TAG" --force 2>/dev/null || echo "Tag push skipped (may already exist)"
          echo "Tag $TAG created/updated"
        fi

        # Clean up RC tags when publishing a final release
        if [[ "${{ inputs.action }}" == "publish" ]]; then
          TAG="${{ inputs.tag }}"
          # Only clean up if this is a final release (not an RC itself)
          if [[ ! "$TAG" =~ -rc\.[0-9]+$ ]]; then
            echo "Cleaning up RC tags for $TAG..."
            # Get base version (e.g., v1.0.0 from v1.0.0)
            BASE_VERSION="$TAG"
            # Find and delete all RC tags matching this base version
            for rc_tag in $(git tag -l "${BASE_VERSION}-rc.*"); do
              echo "Deleting RC tag: $rc_tag"
              git push origin --delete "$rc_tag" 2>/dev/null || echo "Failed to delete remote $rc_tag"
              git tag -d "$rc_tag" 2>/dev/null || echo "Failed to delete local $rc_tag"
            done
          fi
        fi
`

		_, err = h.gitea.CreateCommit(ctx, h.repo, "chore: add cicd config", files)
		if err != nil {
			return fmt.Errorf("failed to create config file: %w", err)
		}

		// Generate workflows from config
		if err := h.GenerateWorkflows(ctx); err != nil {
			return fmt.Errorf("failed to generate workflows: %w", err)
		}
	}

	return nil
}

// scenarioTagFromTestName converts a Go subtest name (e.g.
// "TestMultiStepScenarios/No_Environment_Happy_Path") into a short, sed-safe
// suffix we can append to a workflow's `name:` field. Used to give parallel
// scenarios distinct workflow names so act's container-name hash diverges and
// concurrent runs don't collide on /act-<workflow>-<job>-<hash>.
func scenarioTagFromTestName(name string) string {
	// Replace path separators and any other risky characters with underscores.
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	tag := b.String()
	if len(tag) > 40 {
		tag = tag[len(tag)-40:]
	}
	return tag
}

// generateStubWorkflow creates a minimal reusable workflow for testing.
// scenarioTag is appended to the workflow's `name:` field so parallel
// scenarios with identical build/deploy names produce divergent act job
// container hashes (preventing /act-<workflow>-<job>-<hash> collisions on
// the shared host docker socket).
// generatePublishStubWorkflow creates a minimal workflow_dispatch stub for the
// publish callback. Unlike build/deploy stubs (which are workflow_call), the
// publish workflow is invoked via `gh workflow run` (dispatch) from the promote
// finalize job. The stub accepts the standard publish inputs but does nothing;
// in the e2e harness the dispatch is a no-op because GITHUB_SERVER_URL points
// to gitea.
func generatePublishStubWorkflow(scenarioTag string) string {
	displayName := "Publish"
	if scenarioTag != "" {
		displayName = fmt.Sprintf("Publish [scenario-%s]", scenarioTag)
	}
	return fmt.Sprintf(`name: %s
on:
  workflow_dispatch:
    inputs:
      build_name:
        type: string
        required: true
      old_version:
        type: string
        required: true
      new_version:
        type: string
        required: true
      sha:
        type: string
        required: true
      artifact_id:
        type: string
        required: false
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - run: echo "Stub publish for ${{ inputs.build_name }} ${{ inputs.new_version }}"
`, displayName)
}

func generateStubWorkflow(name, scenarioTag string) string {
	displayName := name
	if scenarioTag != "" {
		displayName = fmt.Sprintf("%s [scenario-%s]", name, scenarioTag)
	}
	// Declare the inputs a real promote/orchestrate deploy callback accepts so the
	// generator discovers them and threads the matching preflight outputs through
	// each deploy's with: block. image_tag drives the source_image_tag passthrough
	// and image_digest drives the source_image_digest passthrough in the generated
	// promote workflow.
	return fmt.Sprintf(`name: %s
on:
  workflow_call:
    inputs:
      environment:
        required: false
        type: string
      sha:
        required: false
        type: string
      image_tag:
        required: false
        type: string
      image_digest:
        required: false
        type: string
jobs:
  %s:
    runs-on: ubuntu-latest
    steps:
      - run: echo "Running %s"
`, displayName, name, name)
}

// generateChangelogStubWorkflow returns a reusable workflow_call stub for a
// custom changelog workflow. It declares the inputs the generator threads
// (changelog_base_sha, head_sha, repo) and a changelog output so the generated
// changelog job and the downstream release step resolve correctly.
func generateChangelogStubWorkflow(scenarioTag string) string {
	displayName := "Changelog"
	if scenarioTag != "" {
		displayName = fmt.Sprintf("Changelog [scenario-%s]", scenarioTag)
	}
	return fmt.Sprintf(`name: %s
on:
  workflow_call:
    inputs:
      changelog_base_sha:
        type: string
        required: false
      head_sha:
        type: string
        required: false
      repo:
        type: string
        required: false
    outputs:
      changelog:
        description: Generated changelog markdown
        value: ${{ jobs.changelog.outputs.changelog }}
jobs:
  changelog:
    runs-on: ubuntu-latest
    outputs:
      changelog: ${{ steps.gen.outputs.changelog }}
    steps:
      - id: gen
        run: echo "changelog=- custom changelog entry" >> "$GITHUB_OUTPUT"
`, displayName)
}

// generateValidateStubWorkflow returns a reusable workflow_call stub for a
// top-level validate callback. The generated orchestrate.yaml invokes it as a
// job-level uses: and gates the build jobs on needs.validate.result, so the stub
// declares the environment/sha inputs the generator threads and an inner job
// that always succeeds, giving the gate a real job to wait on.
func generateValidateStubWorkflow(scenarioTag string) string {
	displayName := "validate"
	if scenarioTag != "" {
		displayName = fmt.Sprintf("validate [scenario-%s]", scenarioTag)
	}
	return fmt.Sprintf(`name: %s
on:
  workflow_call:
    inputs:
      environment:
        required: false
        type: string
      sha:
        required: false
        type: string
jobs:
  runvalidate:
    runs-on: ubuntu-latest
    steps:
      - run: echo "validate"
`, displayName)
}

// GenerateWorkflows generates GitHub Actions workflows from cicd-config.yaml
func (h *Harness) GenerateWorkflows(ctx context.Context) error {
	if h.repo == nil {
		return fmt.Errorf("repo not initialized")
	}
	if h.act == nil {
		return fmt.Errorf("act runner not initialized")
	}

	// Clone the repo into the act container using the external URL
	// This ensures all containers (including act job containers) can access Gitea
	// via host.docker.internal which resolves to the Docker host
	externalURL := h.act.GiteaURL()                                                            // e.g., http://host.docker.internal:32768
	externalHost := strings.TrimPrefix(strings.TrimPrefix(externalURL, "http://"), "https://") // host.docker.internal:32768
	cloneURL := fmt.Sprintf("http://%s:%s@%s/%s/%s.git",
		AdminUsername, AdminPassword,
		externalHost,
		AdminUsername, h.repo.Name)

	cloneCmd := []string{
		"bash", "-c",
		fmt.Sprintf("rm -rf /tmp/repo && git clone %s /tmp/repo", cloneURL),
	}

	exitCode, _, err := h.act.Container().Exec(ctx, cloneCmd)
	if err != nil || exitCode != 0 {
		return fmt.Errorf("failed to clone repo (exit code %d): %w", exitCode, err)
	}

	// Build CLI once for the whole test process and copy into this scenario's
	// act container.
	binaryPath, err := h.ensureCLIBinary(ctx)
	if err != nil {
		return err
	}

	if err := h.act.Container().CopyFileToContainer(ctx, binaryPath, "/usr/local/bin/cascade", 0755); err != nil {
		return fmt.Errorf("failed to copy CLI to container: %w", err)
	}

	// Also copy the CLI into the repo so job containers can access it
	// The mock setup-cli action will copy this to PATH
	copyToRepoCmd := []string{
		"bash", "-c",
		"mkdir -p /tmp/repo/.github/bin && cp /usr/local/bin/cascade /tmp/repo/.github/bin/cascade",
	}
	_, _, _ = h.act.Container().Exec(ctx, copyToRepoCmd)

	// Create a 'master' branch pointing to HEAD to satisfy act's local repository ref matching
	// The generated workflows reference actions @master, so we need that ref to exist
	masterCmd := []string{
		"bash", "-c",
		"cd /tmp/repo && git branch master 2>/dev/null || git branch -f master HEAD",
	}
	_, _, _ = h.act.Container().Exec(ctx, masterCmd) // Ignore errors - branch might already exist

	// Run generate-workflow command
	// CLI auto-detects .github/manifest.yaml with ci: key at top level
	genCmd := []string{
		"bash", "-c",
		"cd /tmp/repo && /usr/local/bin/cascade generate-workflow -f",
	}

	exitCode, reader, err := h.act.Container().Exec(ctx, genCmd)
	if err != nil {
		return fmt.Errorf("failed to execute generate-workflow: %w", err)
	}
	// Always drain the generate-workflow output. Previously it was only read on
	// a non-zero exit, so a run that exited 0 but wrote no workflow (e.g. the
	// "No changes"/skip path) left no trace and the missing orchestrate.yaml
	// only surfaced much later as a `cat: ... No such file` in the orchestrate
	// step. A different scenario per parallel dispatch (#25). Keep the output
	// for diagnostics.
	var genOutput bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&genOutput, reader)
	}
	if exitCode != 0 {
		return fmt.Errorf("generate-workflow failed (exit %d): %s", exitCode, genOutput.String())
	}

	// Verify the orchestrate workflow was actually produced. generate-workflow
	// can exit 0 without emitting .github/workflows/orchestrate.yaml; if we
	// commit and push that empty set, the downstream orchestrate step runs act
	// against a non-existent `-W` path and act (with --detect-event) reports no
	// jobs while still exiting success. The run then masquerades as a passing
	// scenario with 0 jobs (#25). Fail here, at the source, with the generate
	// output and a workflow-dir listing so the real cause is obvious.
	if err := h.assertOrchestrateGenerated(ctx, genOutput.String()); err != nil {
		return err
	}

	// Post-process generated workflows to use local paths instead of external
	// references:
	//   - 'stablekernel/cascade/.github/actions/X@ref' → './.github/actions/X'
	//   - 'uses: build.yaml' → 'uses: ./build.yaml'
	//
	// Earlier this was a fire-and-forget shell sed loop with three blank
	// receivers. Under heavy CI parallelism (28 scenarios × act + gitea
	// containers) the exec would occasionally fail or no-op and leave
	// un-localized refs in the workflow files. The act job container would
	// then try to fetch `stablekernel/cascade@latest` from the test
	// gitea (where that repo doesn't exist), failing with `authentication
	// required` (cf. #78). Now we check the exit code, verify no
	// `stablekernel/cascade` references remain, and retry on transient
	// failure.
	if err := h.localizeWorkflows(ctx); err != nil {
		return fmt.Errorf("localize workflows: %w", err)
	}

	// Suffix each workflow's top-level `name:` with a unique scenario tag so
	// act's auto-generated job container hash (built from workflow+job+name)
	// differs per scenario. Without this, scenarios running under t.Parallel()
	// produce identical workflows → identical /act-<workflow>-<job>-<hash>
	// names → Docker rejects the second creation. Touch BOTH the generated
	// .github/workflows/*.yaml and any top-level reusable workflows like
	// build.yaml / deploy.yaml referenced by `uses: ./<file>`. The ^name:
	// anchor matches only workflow-level fields (job/step `name:` are
	// indented), so other content is unaffected.
	scenarioTag := scenarioTagFromTestName(h.t.Name())
	namespaceCmd := []string{
		"bash", "-c",
		fmt.Sprintf(
			`cd /tmp/repo && find . -type f -name '*.yaml' -not -path './.github/actions/*' -not -name 'manifest.yaml' -exec sed -i 's|^name: \(.*\)|name: \1 [scenario-%s]|' {} +`,
			scenarioTag,
		),
	}
	_, _, _ = h.act.Container().Exec(ctx, namespaceCmd)

	// Configure git in the container and commit workflows and CLI binary.
	// Echo the pushed HEAD on a stable marker line so we can read it back and
	// verify gitea has observed the push before returning (see below).
	commitCmd := []string{
		"bash", "-c",
		"cd /tmp/repo && " +
			"git config user.email 'test@test.local' && " +
			"git config user.name 'Test' && " +
			"git add .github/workflows/*.yaml .github/bin/ && " +
			"git commit -m 'chore: generate workflows and add CLI binary' && " +
			"git push && " +
			"echo PUSHED_SHA=$(git rev-parse HEAD)",
	}

	// Request a demultiplexed stream. Without tcexec.Multiplexed the reader
	// carries Docker's hijacked-attach framing (an 8-byte header per chunk:
	// stream type, padding, then a big-endian uint32 length), so the
	// PUSHED_SHA marker line arrives prefixed with binary header bytes and
	// parsePushedSHA cannot match it. Multiplexed() strips that framing and
	// merges stdout/stderr into clean text.
	exitCode, reader, err = h.act.Container().Exec(ctx, commitCmd, tcexec.Multiplexed())
	var output bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&output, reader)
	}
	if err != nil || exitCode != 0 {
		return fmt.Errorf("failed to commit workflows (exit %d): %w\nOutput: %s", exitCode, err, output.String())
	}

	pushedSHA := parsePushedSHA(output.String())
	if pushedSHA == "" {
		return fmt.Errorf("could not determine pushed workflows SHA from push output:\n%s", output.String())
	}

	// Close the lost-commit race. gitea serves its Contents API from a layer
	// that can lag a raw `git push` to the same branch: the very next runner
	// action (executeCommit) writes through that Contents API and, if it still
	// holds the pre-push branch head, parents its commit on the stale head and
	// moves the ref there - silently discarding this workflows commit. Block
	// until gitea's API reports the pushed SHA as the branch head so every
	// subsequent Contents-API write is parented on it.
	if err := h.waitForBranchHead(ctx, pushedSHA); err != nil {
		return fmt.Errorf("workflows push did not converge in gitea: %w", err)
	}

	return nil
}

// pushedSHAMarker is the prefix GenerateWorkflows echoes after a successful
// push so the pushed HEAD can be recovered from the (otherwise free-form) git
// output.
const pushedSHAMarker = "PUSHED_SHA="

// parsePushedSHA extracts the SHA echoed on the `PUSHED_SHA=<sha>` marker line.
// It returns "" when no well-formed marker is present.
func parsePushedSHA(output string) string {
	for _, line := range strings.Split(output, "\n") {
		// Locate the marker anywhere in the line rather than only as a
		// leading prefix. Docker's multiplexed exec stream frames each chunk
		// with an 8-byte binary header (stream type, padding, big-endian
		// length); if that framing reaches us the marker is preceded by
		// header bytes that may themselves be printable (the length byte can
		// land in the ASCII range), so a leading-prefix check fails. Slicing
		// from the marker recovers the SHA whether or not the stream was
		// demultiplexed upstream.
		idx := strings.Index(line, pushedSHAMarker)
		if idx < 0 {
			continue
		}
		sha := strings.TrimSpace(line[idx+len(pushedSHAMarker):])
		if isHexSHA(sha) {
			return sha
		}
	}
	return ""
}

// isHexSHA reports whether s is a non-empty hex string of a plausible git
// object length (full 40-char or abbreviated, >= 7).
func isHexSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// branchHeadPollAttempts and branchHeadPollInterval bound the verify-after-push
// poll. ~10 x 500ms covers the observed gitea API staleness window with margin
// while still failing fast on a genuinely lost push.
const (
	branchHeadPollAttempts = 10
	branchHeadPollInterval = 500 * time.Millisecond
)

// waitForBranchHead polls gitea's branch API until the main branch head equals
// wantSHA, bounding the wait by branchHeadPollAttempts. It returns a clear
// error (including the last head it observed) if the branch never converges, so
// a genuinely lost push is reported as such rather than masquerading as a
// missing-file failure downstream.
func (h *Harness) waitForBranchHead(ctx context.Context, wantSHA string) error {
	if h.gitea == nil || h.repo == nil {
		return nil
	}

	var lastSHA string
	var lastErr error
	for attempt := 0; attempt < branchHeadPollAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(branchHeadPollInterval):
			}
		}
		head, err := h.gitea.getHeadSHA(ctx, h.repo)
		if err != nil {
			lastErr = err
			continue
		}
		lastSHA = head
		if head == wantSHA {
			return nil
		}
	}

	if lastErr != nil && lastSHA == "" {
		return fmt.Errorf("branch head never readable after %d attempts (want %s): %w",
			branchHeadPollAttempts, wantSHA, lastErr)
	}
	return fmt.Errorf("branch head did not reach pushed SHA after %d attempts (want %s, last observed %s)",
		branchHeadPollAttempts, wantSHA, lastSHA)
}

// assertOrchestrateGenerated confirms that .github/workflows/orchestrate.yaml
// exists and is non-empty in the act container's /tmp/repo immediately after
// generate-workflow. On failure it returns the generate output plus a listing
// of the workflows directory so the missing-file moment is captured with
// context rather than surfacing later as an opaque `cat: ... No such file`.
func (h *Harness) assertOrchestrateGenerated(ctx context.Context, genOutput string) error {
	present, exitCode, err := h.probeOrchestrateWorkflow(ctx)
	if present {
		return nil
	}
	// A docker-exec transport error (err != nil) means the probe itself did not
	// run, not that the file is absent. Surface it as a distinct, retryable
	// condition so a flaky exec is not misreported as a generation failure.
	if err != nil {
		return fmt.Errorf("workflow probe transport error (exit=%d): %w", exitCode, err)
	}
	return fmt.Errorf(
		"generate-workflow exited 0 but did not produce %s (exit=%d)\ngenerate output:\n%s\nworkflows dir:\n%s",
		orchestrateWorkflowPath, exitCode, strings.TrimSpace(genOutput),
		strings.TrimSpace(h.workflowsDirListing(ctx)),
	)
}

// orchestrateWorkflowPath is the generated workflow whose presence gates every
// orchestrate run.
const orchestrateWorkflowPath = ".github/workflows/orchestrate.yaml"

// probeOrchestrateWorkflow reports whether orchestrate.yaml exists and is
// non-empty in /tmp/repo. The returned error is a docker-exec transport error
// (the probe could not be run), which callers treat as retryable and distinct
// from a clean "file absent" result (present=false, err=nil).
func (h *Harness) probeOrchestrateWorkflow(ctx context.Context) (present bool, exitCode int, err error) {
	checkCmd := []string{
		"bash", "-c",
		"cd /tmp/repo && test -s " + orchestrateWorkflowPath,
	}
	exitCode, _, err = h.act.Container().Exec(ctx, checkCmd)
	if err != nil {
		return false, exitCode, err
	}
	return exitCode == 0, exitCode, nil
}

// workflowsDirListing returns a best-effort `ls -la` of the workflows dir for
// diagnostics. It never errors; transport failures yield an empty listing.
func (h *Harness) workflowsDirListing(ctx context.Context) string {
	lsCmd := []string{
		"bash", "-c",
		"cd /tmp/repo && ls -la .github/workflows/ 2>&1 || true",
	}
	var listing bytes.Buffer
	if _, lsReader, lsErr := h.act.Container().Exec(ctx, lsCmd); lsErr == nil && lsReader != nil {
		_, _ = io.Copy(&listing, lsReader)
	}
	return listing.String()
}

// ensureCLIBinary builds the cascade binary once per test process and
// returns its path. Cross-compiled for linux/amd64 since it runs inside the
// act container regardless of the host OS.
func (h *Harness) ensureCLIBinary(ctx context.Context) (string, error) {
	cliBinaryOnce.Do(func() {
		projectRoot, err := h.getProjectRoot()
		if err != nil {
			cliBinaryErr = fmt.Errorf("failed to find project root: %w", err)
			return
		}

		f, err := os.CreateTemp("", "cascade-*")
		if err != nil {
			cliBinaryErr = fmt.Errorf("failed to allocate temp binary path: %w", err)
			return
		}
		path := f.Name()
		_ = f.Close()

		buildCmd := exec.CommandContext(ctx, "go", "build", "-o", path, "./cmd/cascade")
		buildCmd.Dir = projectRoot
		buildCmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
		if output, err := buildCmd.CombinedOutput(); err != nil {
			_ = os.Remove(path)
			cliBinaryErr = fmt.Errorf("failed to build CLI: %w\nOutput: %s", err, output)
			return
		}
		cliBinaryPath = path
	})
	return cliBinaryPath, cliBinaryErr
}

// localizeWorkflows rewrites generated workflows so external action refs
// (`stablekernel/cascade/.github/actions/X@ref`) point at the local
// mock actions. A second pass adds the `./` prefix act needs for any bare
// local reusable-workflow ref (`uses: build.yaml` -> `uses: ./build.yaml`).
// That pass is idempotent: it skips values that are already `./`-qualified,
// already a `.github/...` path, or a cross-repo `owner/repo/...@ref`, so an
// already-normalized `uses: ./.github/workflows/build.yaml` is left unchanged
// rather than gaining a second `./` prefix. Each pass is checked for success
// and verified by grepping for any remaining `stablekernel/...` ref; the
// operation retries on transient failure.
//
// The `.github/workflows/*.yaml` glob is intentionally broad: it covers every
// generated workflow, including cascade-hotfix.yaml (whose plan/apply/finalize
// jobs reference the same setup-cli action ref). No hotfix-specific localization
// is required beyond this glob. The companion `name:` namespacing in
// GenerateWorkflows only appends a per-scenario suffix to the workflow-level
// `name:` line, so workflow_files assertions over hotfix job/trigger content
// remain stable as long as they do not assert on the top-level name line.
// actionLocalizeSedExpr rewrites external composite-action refs
// (`stablekernel/cascade/.github/actions/X@ref`) to the local mock path
// (`./.github/actions/X`).
const actionLocalizeSedExpr = `s|stablekernel/cascade/\.github/actions/\([^@]*\)@[^[:space:]]*|./.github/actions/\1|g`

// usesLocalizeSedExpr prefixes a bare local reusable-workflow ref
// (`uses: build.yaml`) with `./` so act can resolve it. It is idempotent: the
// capture group requires a bare filename (no leading `.`, `/`, or `@`, and no
// embedded `/`), so an already-qualified `uses: ./.github/workflows/build.yaml`
// or a cross-repo `uses: owner/repo/...@ref` is left unchanged rather than
// gaining a second `./` prefix.
const usesLocalizeSedExpr = `s|uses: \([^./@][^/@]*\.yaml\)|uses: ./\1|g`

func (h *Harness) localizeWorkflows(ctx context.Context) error {
	const maxAttempts = 3
	const retryDelay = 200 * time.Millisecond

	rewrite := []string{
		"bash", "-c",
		"set -e; cd /tmp/repo && " +
			"for f in .github/workflows/*.yaml; do " +
			"  sed -i '" + actionLocalizeSedExpr + "' \"$f\"; " +
			"  sed -i '" + usesLocalizeSedExpr + "' \"$f\"; " +
			"done",
	}
	// grep -l exits 0 on match (= un-localized ref still present, which we
	// treat as a failure to localize). Negation gives us 0 = clean.
	verify := []string{
		"bash", "-c",
		"cd /tmp/repo && ! grep -l 'stablekernel/cascade' .github/workflows/*.yaml",
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		exitCode, _, err := h.act.Container().Exec(ctx, rewrite)
		if err != nil || exitCode != 0 {
			lastErr = fmt.Errorf("localize sed (attempt %d) failed: exit=%d err=%v", attempt, exitCode, err)
		} else {
			vExit, _, vErr := h.act.Container().Exec(ctx, verify)
			if vErr == nil && vExit == 0 {
				return nil
			}
			lastErr = fmt.Errorf("localize verify (attempt %d) found un-localized refs: exit=%d err=%v", attempt, vExit, vErr)
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
		}
	}
	return lastErr
}

// SyncRepoToActContainer pulls latest changes from Gitea into /tmp/repo
func (h *Harness) SyncRepoToActContainer(ctx context.Context) error {
	if h.act == nil || h.repo == nil {
		return nil
	}

	// Pull latest changes and update master branch.
	//
	// `git fetch` without an explicit refspec can occasionally fail or no-op
	// under heavy parallel load against the per-scenario gitea; bind the fetch
	// to origin/main explicitly and chain the reset so a partial sync surfaces
	// as a non-zero exit instead of silently resetting to a stale tree (which
	// would drop the just-pushed orchestrate.yaml (#25).
	//
	// The fetch+reset and the orchestrate.yaml presence check are retried as a
	// unit: a transient gitea read or a momentarily stale ref can yield a tree
	// without the workflow even though GenerateWorkflows already verified the
	// push converged. Re-fetching resolves those; a persistent miss after the
	// bound is a real lost-commit and is reported with this call site's own
	// message rather than the generation-phase one.
	//
	// After the load-bearing `git fetch origin main && git reset --hard
	// origin/main` (which still gates the lost-commit detection and bounded
	// retry below), pull the env/* integration branches and tags too, mirroring
	// the generated hotfix workflow's fetch step (internal/generate/hotfix.go,
	// writeFetchEnvBranches). Hotfix plan/apply/finalize jobs inspect
	// env/<env> branches and hotfix tags, so they must be present in /tmp/repo.
	// This extra fetch is chained with `|| true`: env/* branches and tags do not
	// exist in most scenarios, and their absence must never fail the sync or
	// regress the main-branch convergence guarantee.
	syncCmd := []string{
		"bash", "-c",
		"cd /tmp/repo && git fetch origin main && git reset --hard origin/main && " +
			"(git fetch origin '+refs/heads/env/*:refs/remotes/origin/env/*' --tags 2>/dev/null || true) && " +
			"(git branch -f master HEAD 2>/dev/null || true)",
	}

	const maxAttempts = 5
	const retryDelay = 500 * time.Millisecond

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
		}

		exitCode, reader, err := h.act.Container().Exec(ctx, syncCmd)
		if err != nil {
			lastErr = fmt.Errorf("fetch/reset transport error: %w", err)
			continue
		}
		if exitCode != 0 {
			var output bytes.Buffer
			if reader != nil {
				_, _ = io.Copy(&output, reader)
			}
			lastErr = fmt.Errorf("fetch/reset failed (exit %d): %s", exitCode, strings.TrimSpace(output.String()))
			continue
		}

		present, probeExit, probeErr := h.probeOrchestrateWorkflow(ctx)
		if present {
			return nil
		}
		if probeErr != nil {
			lastErr = fmt.Errorf("workflow probe transport error (exit=%d): %w", probeExit, probeErr)
			continue
		}
		lastErr = fmt.Errorf("%s absent after fetch/reset (exit=%d)", orchestrateWorkflowPath, probeExit)
	}

	// Distinct from the generation-phase message: this is a sync/lost-commit
	// failure, not a generate-workflow failure. Misattributing it (the prior
	// behavior) sent diagnosis down the wrong path.
	return fmt.Errorf(
		"orchestrate workflow still missing after %d repo-sync attempts: %w\nworkflows dir:\n%s",
		maxAttempts, lastErr, strings.TrimSpace(h.workflowsDirListing(ctx)),
	)
}

// getProjectRoot finds the project root directory containing cmd/cascade
func (h *Harness) getProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	for {
		// Look for the main project root (has cmd/cascade, not e2e's go.mod)
		if _, err := os.Stat(filepath.Join(dir, "cmd", "cascade")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (cmd/cascade)")
		}
		dir = parent
	}
}

// Cleanup terminates all containers and removes the scenario's docker network.
//
// Network removal is synchronous and verified, not fire-and-forget: a single
// scenario allocates a /16 (or /24) from the daemon's address pool, and across
// the serial scenario suite a leaked network drains that pool until a late
// scenario cannot allocate one and dies at setup ("all predefined address
// pools have been fully subnetted"). The retry layer defers Cleanup per
// attempt, so every attempt - including a failed one - releases its network
// here. Because the act and gitea containers detach from the network during
// their own teardown, Remove can briefly observe an "active endpoints" error;
// a short bounded retry lets that detach settle so the count returns to
// baseline rather than growing monotonically.
func (h *Harness) Cleanup() {
	ctx := context.Background()
	if h.act != nil {
		_ = h.act.Terminate(ctx)
	}
	if h.gitea != nil {
		_ = h.gitea.Terminate(ctx)
	}
	if h.network != nil {
		h.removeNetwork(ctx)
	}
}

// removeNetwork removes the scenario network, waiting for and checking the
// result, and logs (rather than swallows) a terminal failure so a genuine leak
// is visible in the test output.
//
// act runs each job by spawning a NESTED container over the docker socket and
// attaching it to this network. Those job containers are act's children, not
// testcontainers-managed, so terminating the act runner does not reap them;
// one can outlive the runner and hold the network open, failing Remove with an
// "active endpoints" error. So before each Remove attempt we force-remove any
// container still attached to the network, then retry briefly to let the
// detach settle. This keeps the network count flat across the suite instead of
// leaking one network per scenario whose job container lingered.
func (h *Harness) removeNetwork(ctx context.Context) {
	const attempts = 5
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			h.disconnectNetworkContainers(ctx)
		}
		if err = h.network.Remove(ctx); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	if h.t != nil {
		h.t.Logf("warning: failed to remove docker network %q after %d attempts: %v",
			h.networkName, attempts, err)
	}
}

// disconnectNetworkContainers force-removes every container still attached to
// the scenario network so it can be removed. It targets act's nested job
// containers, which are not testcontainers-managed and so survive the act
// runner's termination. Best effort: a container that is already gone or a
// docker hiccup must not abort cleanup.
func (h *Harness) disconnectNetworkContainers(ctx context.Context) {
	out, err := exec.CommandContext(ctx, "docker", "network", "inspect",
		"--format", "{{range .Containers}}{{.Name}} {{end}}", h.networkName).Output()
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(out)) {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	}
}

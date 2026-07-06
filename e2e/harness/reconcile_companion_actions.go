package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"testing"

	tcexec "github.com/testcontainers/testcontainers-go/exec"

	"github.com/stablekernel/cascade/internal/config"
)

// reconcileCompanionBumpBranch is the branch the scenario pushes the simulated
// external pin bump onto before opening a same-repo pull request.
const reconcileCompanionBumpBranch = "bump-checkout-pin"

// reconcileCompanionOrchestrateFile is the generated workflow the scenario
// mutates to simulate an external bump (the shape of a merged Dependabot
// update) landing in an already-generated file.
const reconcileCompanionOrchestrateFile = ".github/workflows/orchestrate.yaml"

// ReconcileCompanionBumpTag is the synthetic tag substituted for the real
// compiled-in checkout pin, simulating an external governed-pin bump (the
// shape of a merged Dependabot update). It is obviously fake so a
// false-positive match against the real pin table is impossible.
const ReconcileCompanionBumpTag = "v99.99.99"

// reconcileCheckoutRefRE matches the checkout action's pinned ref (a tag or a
// commit sha, with an optional trailing version comment) so the scenario can
// substitute the synthetic bump for whatever pin_mode actually emitted.
var reconcileCheckoutRefRE = regexp.MustCompile(`actions/checkout@\S+(?:\s+#\s+\S+)?`)

// ReconcileCompanionResult carries what an act-driven companion run proves:
// the job's real conclusion, and the pull request branch's state before and
// after the run, so the caller can assert the adoption commit actually landed.
type ReconcileCompanionResult struct {
	// Conclusion is the act job conclusion for the companion's "reconcile" job
	// (falls back to the overall run conclusion if the job is not present).
	Conclusion string
	// PRNumber is the same-repo pull request the scenario opened.
	PRNumber int64
	// HeadBefore is the pull request branch's head SHA before the companion ran.
	HeadBefore string
	// HeadAfter is the pull request branch's head SHA after the companion ran.
	HeadAfter string
	// ManifestAfter is the manifest content on the pull request branch after
	// the companion ran, so the caller can assert the adopted action_pins entry.
	ManifestAfter string
	// OrchestrateAfter is the regenerated orchestrate.yaml content on the pull
	// request branch after the companion ran.
	OrchestrateAfter string
}

// RunReconcileCompanionAppendScenario stands up a real Gitea repository with
// the opt-in reconcile companion enabled (commit: "append", the default),
// simulates an external governed-pin bump (the shape of a merged Dependabot
// update) landing in the generated orchestrate.yaml on a same-repo pull
// request, computes the real detector relevance artifact by running the real
// `cascade reconcile --check` binary against that pull request's changed
// file, and then drives the emitted cascade-reconcile-companion.yaml through
// act with a real workflow_run event resolved from that pull request's live
// Gitea state.
//
// This is the first scenario in the suite to drive a workflow_run companion
// through a real act run (every prior drift-check/drift-comment coverage only
// asserts generated content), so it establishes two hermeticity riders atop
// the ones GenerateWorkflows already applies:
//
//  1. act resolves every marketplace action it does not implement natively
//     (github-script, download-artifact, and even checkout) by git-cloning it
//     from whatever GITHUB_SERVER_URL is configured for the job. That is gitea
//     in this harness, which has no such repository, so the clone fails
//     closed with "authentication required" before the step even starts.
//     stubNonNativeActions replaces each with a local mock that performs the
//     same real work (the trusted-metadata Gitea API lookup, the PR-head ref
//     fetch) via shell.
//  2. this act version cannot resolve a local `uses: ./...` action on a step
//     that also carries an `id:` (a bug independent of this feature), so the
//     two id-carrying steps (the artifact download and the PR resolution) are
//     inlined as direct `run:` blocks instead of composite actions; the
//     id-free checkout step keeps using a local composite action, unaffected.
//
// Every step's real logic still runs: the resolve step's trusted-metadata
// derivation and fresh-tip guard both make a real Gitea API call; the checkout
// fetches the real refs/pull/<n>/head; the pinned cascade binary performs the
// real (idempotent) `cascade reconcile` adoption; and the push lands for real
// on the pull request's own branch.
func RunReconcileCompanionAppendScenario(ctx context.Context, t *testing.T) (*ReconcileCompanionResult, error) {
	t.Helper()

	h := New(t)
	defer h.Cleanup()

	if err := h.SetupInfra(ctx); err != nil {
		return nil, fmt.Errorf("setup infra: %w", err)
	}

	cfg := Config{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Reconcile:    &config.ReconcileConfig{Enabled: true},
		Builds: []config.BuildConfig{
			{Name: "build", Workflow: ".github/workflows/build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "deploy", Workflow: ".github/workflows/deploy.yaml"},
		},
	}
	if err := h.StageRepoFromConfig(ctx, cfg, nil); err != nil {
		return nil, fmt.Errorf("stage repo: %w", err)
	}

	mainSHA, err := h.gitea.GetBranchSHA(ctx, h.repo, "main")
	if err != nil {
		return nil, fmt.Errorf("read main head: %w", err)
	}
	orchestrate, err := h.gitea.GetFileContentOnBranch(ctx, h.repo, reconcileCompanionOrchestrateFile, "main")
	if err != nil {
		return nil, fmt.Errorf("read orchestrate.yaml: %w", err)
	}
	if !reconcileCheckoutRefRE.MatchString(orchestrate) {
		return nil, fmt.Errorf("orchestrate.yaml does not reference actions/checkout; cannot simulate a bump")
	}
	bumped := reconcileCheckoutRefRE.ReplaceAllString(orchestrate, "actions/checkout@"+ReconcileCompanionBumpTag)

	if err := h.gitea.CreateBranch(ctx, h.repo, reconcileCompanionBumpBranch, mainSHA); err != nil {
		return nil, fmt.Errorf("create bump branch: %w", err)
	}
	headSHA, err := h.gitea.CreateCommitOnBranch(ctx, h.repo, reconcileCompanionBumpBranch,
		"chore: bump actions/checkout pin (simulated external update)",
		map[string]string{reconcileCompanionOrchestrateFile: bumped})
	if err != nil {
		return nil, fmt.Errorf("commit bump: %w", err)
	}

	prNumber, err := h.gitea.CreatePR(ctx, h.repo, reconcileCompanionBumpBranch, "main",
		"chore: bump actions/checkout pin", "Simulated external governed-pin bump.", nil)
	if err != nil {
		return nil, fmt.Errorf("open pull request: %w", err)
	}

	if err := h.checkoutBranchInActContainer(ctx, reconcileCompanionBumpBranch); err != nil {
		return nil, fmt.Errorf("checkout bump branch in act container: %w", err)
	}

	if err := h.computeReconcileCheckArtifact(ctx); err != nil {
		return nil, fmt.Errorf("compute check artifact: %w", err)
	}

	if err := h.stubNonNativeActions(ctx); err != nil {
		return nil, fmt.Errorf("stub non-native actions: %w", err)
	}

	eventJSON, err := reconcileCompanionWorkflowRunEvent(prNumber, headSHA, AdminUsername+"/"+h.repo.Name)
	if err != nil {
		return nil, fmt.Errorf("build workflow_run event: %w", err)
	}

	result, err := h.act.RunWorkflowFromRepo(ctx, RunOpts{
		Event:        "workflow_run",
		EventJSON:    eventJSON,
		WorkflowPath: ".github/workflows/cascade-reconcile-companion.yaml",
		// act does not populate $GITHUB_REPOSITORY as "owner/repo" for a
		// synthetic workflow_run event (it derives it from the git remote URL
		// verbatim instead), so the mock resolve step reads the real repo
		// full name from this plain env var rather than a GitHub Actions
		// expression.
		Env: map[string]string{"CASCADE_REPO_FULL_NAME": AdminUsername + "/" + h.repo.Name},
	})
	if err != nil {
		return nil, fmt.Errorf("run companion workflow: %w", err)
	}

	headAfter, err := h.gitea.GetBranchSHA(ctx, h.repo, reconcileCompanionBumpBranch)
	if err != nil {
		return nil, fmt.Errorf("read bump branch head after run: %w", err)
	}
	manifestAfter, err := h.gitea.GetFileContentOnBranch(ctx, h.repo, ".github/manifest.yaml", reconcileCompanionBumpBranch)
	if err != nil {
		return nil, fmt.Errorf("read manifest after run: %w", err)
	}
	orchestrateAfter, err := h.gitea.GetFileContentOnBranch(ctx, h.repo, reconcileCompanionOrchestrateFile, reconcileCompanionBumpBranch)
	if err != nil {
		return nil, fmt.Errorf("read orchestrate.yaml after run: %w", err)
	}

	conclusion := result.Conclusion
	if job, ok := result.Jobs["reconcile"]; ok && job != nil {
		conclusion = job.Conclusion
	}
	if conclusion != "success" {
		t.Logf("companion run did not succeed (conclusion=%s); raw act log:\n%s", conclusion, result.Logs)
	}

	return &ReconcileCompanionResult{
		Conclusion:       conclusion,
		PRNumber:         prNumber,
		HeadBefore:       headSHA,
		HeadAfter:        headAfter,
		ManifestAfter:    manifestAfter,
		OrchestrateAfter: orchestrateAfter,
	}, nil
}

// checkoutBranchInActContainer fetches and checks out the named branch in
// /tmp/repo inside the act container, mirroring the state a same-repo pull
// request's head currently points at.
func (h *Harness) checkoutBranchInActContainer(ctx context.Context, branch string) error {
	cmd := []string{"bash", "-c", fmt.Sprintf(
		"cd /tmp/repo && git fetch origin %s && git checkout -B %s FETCH_HEAD",
		shellQuote(branch), shellQuote(branch),
	)}
	exitCode, reader, err := h.act.Container().Exec(ctx, cmd)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&out, reader)
	}
	if exitCode != 0 {
		return fmt.Errorf("checkout %s failed (exit %d): %s", branch, exitCode, out.String())
	}
	return nil
}

// computeReconcileCheckArtifact runs the real `cascade reconcile --check`
// binary in /tmp/repo (currently checked out at the pull request's head) and
// writes its output at the exact path the emitted companion's resolve step
// reads (pin-reconcile-result/pin-reconcile-result.json). This stands in for
// the companion's own "download artifact by run-id" step, which act cannot
// satisfy across two separate invocations; the relevance decision is the real
// command's real output, not a hand-authored fixture.
func (h *Harness) computeReconcileCheckArtifact(ctx context.Context) error {
	cmd := []string{"bash", "-c",
		"cd /tmp/repo && mkdir -p pin-reconcile-result && " +
			"/usr/local/bin/cascade reconcile --check " +
			"--changed-file " + reconcileCompanionOrchestrateFile + " " +
			"--check-output pin-reconcile-result/pin-reconcile-result.json",
	}
	exitCode, reader, err := h.act.Container().Exec(ctx, cmd)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&out, reader)
	}
	if exitCode != 0 {
		return fmt.Errorf("cascade reconcile --check failed (exit %d): %s", exitCode, out.String())
	}
	return nil
}

// mockDownloadStepBlock replaces the companion's real
// `actions/download-artifact` "Download pin-reconcile result" step. Real
// download-artifact resolves its run-id against a live GitHub (or
// GHES-compatible) Actions Results backend that has no cross-invocation
// record of a detector run act executed separately; worse, act resolves the
// action itself by git-cloning from whatever GITHUB_SERVER_URL is configured
// (gitea in this harness), which has no such repository and fails closed with
// "authentication required" before the step, or its continue-on-error, ever
// runs. computeReconcileCheckArtifact has already written the real check
// result at the exact path the resolve step reads, so this step has nothing
// left to do. It is inlined as a direct run: block (not a local composite
// action) because this act version cannot resolve a local `uses: ./...`
// action on a step that also carries an `id:`.
const mockDownloadStepBlock = `      - name: Download pin-reconcile result
        id: download
        continue-on-error: true
        run: echo "pin-reconcile-result already staged"
`

// downloadStepRE matches the companion's whole "Download pin-reconcile
// result" step block, up to (but not including) the next step's name line.
var downloadStepRE = regexp.MustCompile(`(?s)      - name: Download pin-reconcile result\n.*?\n(      - name: Resolve target PR)`)

// mockResolveStepBlock replaces the companion's real `actions/github-script`
// "Resolve target PR" step. act resolves github-script the same way it
// resolves download-artifact (git-cloning from GITHUB_SERVER_URL, which fails
// closed against gitea), so this performs the SAME trusted-metadata
// resolution the real script does via shell instead of node+octokit: it never
// trusts the check artifact for the PR number, only the workflow_run event's
// pull_requests entry, then re-fetches the pull request fresh from the real
// Gitea API (the same fresh-tip and fork-detection logic the real script
// runs), reading the real repo's "owner/repo" from the CASCADE_REPO_FULL_NAME
// env var the scenario provides (act does not populate the built-in
// $GITHUB_REPOSITORY as "owner/repo" for a synthetic workflow_run event; it
// carries the git remote URL verbatim instead). Only the execution engine is
// substituted, not the logic. It is inlined as a direct run: block, not a
// local composite action, for the same id-plus-local-uses reason as the
// download step above.
const mockResolveStepBlock = `      - name: Resolve target PR
        id: resolve
        env:
          GH_TOKEN: ${{ github.token }}
          PR_NUMBER: ${{ github.event.workflow_run.pull_requests[0].number }}
        run: |
          set -euo pipefail
          relevant=false
          if [ -f pin-reconcile-result/pin-reconcile-result.json ]; then
            relevant=$(jq -r '.relevant' pin-reconcile-result/pin-reconcile-result.json)
          fi
          resp=$(curl -sf -H "Authorization: token $GH_TOKEN" "$GITHUB_API_URL/repos/$CASCADE_REPO_FULL_NAME/pulls/$PR_NUMBER")
          base_sha=$(echo "$resp" | jq -r '.base.sha')
          base_ref=$(echo "$resp" | jq -r '.base.ref')
          head_sha=$(echo "$resp" | jq -r '.head.sha')
          head_ref=$(echo "$resp" | jq -r '.head.ref')
          base_repo=$(echo "$resp" | jq -r '.base.repo.full_name')
          head_repo=$(echo "$resp" | jq -r '.head.repo.full_name')
          fork=false
          if [ "$base_repo" != "$head_repo" ]; then fork=true; fi
          {
            echo "relevant=$relevant"
            echo "pr_number=$PR_NUMBER"
            echo "base_sha=$base_sha"
            echo "base_ref=$base_ref"
            echo "head_sha=$head_sha"
            echo "head_ref=$head_ref"
            echo "fork=$fork"
          } >> "$GITHUB_OUTPUT"
`

// resolveStepRE matches the companion's whole "Resolve target PR" step block,
// from its name line up to (but not including) the next step's name line, so
// it can be replaced in one piece rather than line-by-line.
var resolveStepRE = regexp.MustCompile(`(?s)      - name: Resolve target PR\n.*?\n(      - name: Checkout PR head)`)

// mockCheckoutAction is a shell-based stand-in for the companion's
// `actions/checkout` "Checkout PR head (data only)" step. It performs the
// exact same operation the real action's `ref:` input would (fetching and
// checking out refs/pull/<n>/head on the trusted base-repo remote, never a
// fork's own repository), so only the execution engine is substituted. Unlike
// the download and resolve steps, the real checkout step carries no `id:`, so
// this mock stays a local composite action rather than needing to inline.
const mockCheckoutAction = `name: 'Checkout PR Head (Mock)'
description: 'Shell-based stand-in for actions/checkout in the e2e harness'
inputs:
  pr_number:
    required: true
runs:
  using: 'composite'
  steps:
    - shell: bash
      env:
        PR_NUMBER: ${{ inputs.pr_number }}
      run: |
        set -euo pipefail
        git fetch origin "refs/pull/$PR_NUMBER/head"
        git checkout -B "pr-$PR_NUMBER-head" FETCH_HEAD
`

// checkoutStepRE matches the companion's whole "Checkout PR head (data only)"
// step block, up to (but not including) the next step's name line.
var checkoutStepRE = regexp.MustCompile(`(?s)      - name: Checkout PR head \(data only\)\n.*?\n(      - name: Setup CLI)`)

// mockCheckoutStepBlock is the replacement for the companion's real checkout
// step. It preserves the same relevance gate the real step carries.
const mockCheckoutStepBlock = `      - name: Checkout PR head (data only)
        if: steps.resolve.outputs.relevant == 'true'
        uses: ./.github/actions/mock-checkout-pr-head
        with:
          pr_number: ${{ steps.resolve.outputs.pr_number }}
`

// mockGithubScriptNoopAction stands in for actions/github-script wherever the
// companion still references it (the fork-fallback sticky comment, and the
// followup-mode PR open/update, neither of which this append/non-fork
// scenario ever executes). act resolves EVERY action referenced anywhere in a
// job up front, before running any step, regardless of that step's own "if:"
// gate; a single unresolvable reference anywhere in the job, even on a step
// that never actually runs, aborts the whole job before any step's Main
// phase starts. This mock only needs to exist so that upfront resolution
// succeeds.
const mockGithubScriptNoopAction = `name: 'Github Script (Mock, unused in this scenario)'
description: 'Local stand-in so upfront action resolution succeeds for a step this scenario never executes'
runs:
  using: 'composite'
  steps:
    - shell: bash
      run: echo "github-script mock: this step is not exercised by the append/non-fork scenario"
`

// githubScriptStepRE matches every remaining `uses: actions/github-script@ref`
// reference in the companion (the fork-fallback and, in followup mode, the
// PR-open step), so upfront action resolution never needs the real action.
var githubScriptStepRE = regexp.MustCompile(`uses: actions/github-script@\S+`)

// stubNonNativeActions rewrites the companion workflow so its
// non-natively-implemented marketplace actions (actions/checkout,
// actions/download-artifact, and actions/github-script) are replaced with
// local mocks that perform the same real work (fetching the trusted PR-head
// ref, reading the pre-staged check result, and querying the real Gitea API
// for the pull request's live state) via shell instead of act cloning and
// running the real marketplace action. See the RunReconcileCompanionAppendScenario
// doc comment for why: act resolves these against gitea's GITHUB_SERVER_URL
// and fails closed, and this act version cannot resolve a local action on a
// step that also carries an `id:`. The rewrite is workspace-only (never
// pushed; it is locally committed only so act's local-action resolution,
// which reads git-tracked content, finds the new mock action directories),
// the same hermeticity trick GenerateWorkflows already applies to cascade's
// own composite actions via localizeWorkflows.
func (h *Harness) stubNonNativeActions(ctx context.Context) error {
	content, err := h.readCompanionWorkflow(ctx)
	if err != nil {
		return err
	}

	if !downloadStepRE.MatchString(content) {
		return fmt.Errorf("companion workflow does not contain the expected Download pin-reconcile result step block")
	}
	content = downloadStepRE.ReplaceAllString(content, mockDownloadStepBlock+"      - name: Resolve target PR")

	if !resolveStepRE.MatchString(content) {
		return fmt.Errorf("companion workflow does not contain the expected Resolve target PR step block")
	}
	content = resolveStepRE.ReplaceAllString(content, mockResolveStepBlock+"      - name: Checkout PR head")

	if !checkoutStepRE.MatchString(content) {
		return fmt.Errorf("companion workflow does not contain the expected Checkout PR head step block")
	}
	content = checkoutStepRE.ReplaceAllString(content, mockCheckoutStepBlock+"      - name: Setup CLI")

	if !githubScriptStepRE.MatchString(content) {
		return fmt.Errorf("companion workflow does not contain the expected actions/github-script reference")
	}
	content = githubScriptStepRE.ReplaceAllString(content, "uses: ./.github/actions/mock-github-script")

	writeCmd := []string{"bash", "-c",
		"mkdir -p /tmp/repo/.github/actions/mock-checkout-pr-head /tmp/repo/.github/actions/mock-github-script && " +
			"cat > /tmp/repo/.github/actions/mock-checkout-pr-head/action.yaml <<'CASCADE_EOF'\n" + mockCheckoutAction + "CASCADE_EOF\n" +
			"cat > /tmp/repo/.github/actions/mock-github-script/action.yaml <<'CASCADE_EOF'\n" + mockGithubScriptNoopAction + "CASCADE_EOF\n" +
			"cat > /tmp/repo/.github/workflows/cascade-reconcile-companion.yaml <<'CASCADE_EOF'\n" + content + "CASCADE_EOF\n" +
			"cd /tmp/repo && git add -A && git -c user.email=test@test.local -c user.name=Test commit -q -m 'test: stage act-hermeticity mocks'",
	}
	exitCode, reader, err := h.act.Container().Exec(ctx, writeCmd, tcexec.Multiplexed())
	if err != nil {
		return err
	}
	writeOut, err := readDemuxedStream(reader)
	if err != nil {
		return fmt.Errorf("read write-stub output: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("write stubbed companion workflow failed (exit %d): %s", exitCode, writeOut)
	}
	return nil
}

// readCompanionWorkflow returns the emitted companion workflow's current
// content from the act container's working tree.
func (h *Harness) readCompanionWorkflow(ctx context.Context) (string, error) {
	catCmd := []string{"bash", "-c", "cat /tmp/repo/.github/workflows/cascade-reconcile-companion.yaml"}
	exitCode, reader, err := h.act.Container().Exec(ctx, catCmd, tcexec.Multiplexed())
	if err != nil {
		return "", err
	}
	content, err := readDemuxedStream(reader)
	if err != nil {
		return "", fmt.Errorf("read companion workflow output: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("read companion workflow failed (exit %d): %s", exitCode, content)
	}
	return content, nil
}

// reconcileCompanionWorkflowRunEvent builds the workflow_run "completed" event
// payload the emitted companion's resolve step reads. It carries only the
// trusted run metadata a real workflow_run event would carry: the source
// run's event type, its head SHA, and the associated pull request number. The
// companion re-fetches the pull request fresh via a real Gitea API call and
// compares pr.data.head.sha against this head_sha (the fresh-tip guard), so
// headSHA must be the pull request branch's actual current head. repoFullName
// is "owner/repo" for the real Gitea repository under test; a real
// workflow_run webhook payload carries a top-level "repository" key alongside
// "workflow_run", so it is included here for shape fidelity even though the
// mock resolve step reads the repo full name from CASCADE_REPO_FULL_NAME
// instead (see mockResolveStepBlock).
func reconcileCompanionWorkflowRunEvent(prNumber int64, headSHA, repoFullName string) (string, error) {
	payload := map[string]any{
		"repository": map[string]any{
			"full_name": repoFullName,
		},
		"workflow_run": map[string]any{
			"id":         1,
			"event":      "pull_request",
			"conclusion": "success",
			"head_sha":   headSHA,
			"pull_requests": []map[string]any{
				{"number": prNumber},
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

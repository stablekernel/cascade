package generate

import (
	"fmt"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// reconcileCheckArtifact is the name of the data-only relevance artifact the
// pull_request detector uploads and the workflow_run companion downloads. It
// never carries the target PR number; the companion derives that only from
// trusted workflow_run run metadata.
const reconcileCheckArtifact = "pin-reconcile-result"

// reconcileCheckArtifactFile is the JSON file cascade reconcile --check writes,
// matching internal/pinreconcile's defaultCheckArtifactPath.
const reconcileCheckArtifactFile = "pin-reconcile-result.json"

// reconcileCheckWorkflowName is the workflow name the pull_request detector
// runs under. The companion subscribes to completed runs of this exact name,
// so the two generated files must agree on it.
const reconcileCheckWorkflowName = "Cascade Reconcile Check"

// reconcileCommitMessage is the subject line of the adoption commit the
// companion pushes onto (or alongside) the triggering PR.
const reconcileCommitMessage = "chore: reconcile governed action pins"

// reconcileCommentMarker is the hidden HTML marker embedded in the fork
// fallback comment so a later run finds and updates the same sticky comment
// instead of posting a new one.
const reconcileCommentMarker = "<!-- cascade-reconcile -->"

// reconcileFollowupBranchExpr is the cascade-owned branch name a "followup"
// commit (or an append-mode fork fallback, were it to open a PR) would use.
// It is deterministic per PR number so a repeat run updates the same branch
// and PR rather than accumulating one per run.
const reconcileFollowupBranchExpr = "cascade-reconcile/pr-${{ steps.resolve.outputs.pr_number }}"

// ReconcileGenerator emits the opt-in emitted pin-reconcile companion (#443).
//
// A pull_request job runs strictly read-only: it computes the PR's changed
// workflow files, runs `cascade reconcile --check` (a pinned release binary,
// never a `go run` off the repository's own source), and uploads the result as
// a data-only artifact. It carries no secrets and no write permission, so a
// fork PR cannot abuse it.
//
// A workflow_run companion (GenerateCompanion) subscribes to completions of
// that detector, resolves the target PR from trusted workflow_run metadata
// only, and, when the artifact reports relevance, fetches the PR's head files
// as data, runs the pinned cascade binary to adopt the bump into the manifest,
// and pushes (or opens a followup PR) with the trigger-capable state token.
type ReconcileGenerator struct {
	config  *config.TrunkConfig
	baseDir string
	ownRepo bool
}

// ReconcileOption customizes a ReconcileGenerator. Options are the additive,
// variadic tail of NewReconcileGenerator so new behavior never changes the
// two-argument signature callers already depend on.
type ReconcileOption func(*ReconcileGenerator)

// WithOwnRepo switches the generator into own-repo mode, which emits cascade's
// self-heal companion for its own repository rather than the companion a
// downstream user adopts. The own-repo companion installs the latest
// non-prerelease cascade release, scans both the workflows and composite-action
// trees for a moved governed pin, and commits the regenerated workflows plus the
// updated pin manifest back onto the triggering branch.
func WithOwnRepo() ReconcileOption {
	return func(g *ReconcileGenerator) { g.ownRepo = true }
}

// NewReconcileGenerator creates a new reconcile companion workflow generator.
// Optional behavior is supplied through the variadic ReconcileOption tail.
func NewReconcileGenerator(cfg *config.TrunkConfig, baseDir string, opts ...ReconcileOption) *ReconcileGenerator {
	g := &ReconcileGenerator{config: cfg, baseDir: baseDir}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Enabled reports whether the reconcile companion should be emitted.
func (g *ReconcileGenerator) Enabled() bool {
	return g.config.Reconcile != nil && g.config.Reconcile.Enabled
}

// getCLIRef returns the Git ref for the cascade self-action. The default
// (cli_version unset or "latest") resolves to an immutable release tag, so
// consumers never run an unpinned mutable ref; "beta" opts in to "master".
func (g *ReconcileGenerator) getCLIRef() string {
	return cliSetupRef(g.config)
}

// Generate creates the pull_request reconcile-detector workflow content.
func (g *ReconcileGenerator) Generate() (string, error) {
	if !g.Enabled() {
		return "", fmt.Errorf("cannot generate reconcile workflow: reconcile is not enabled")
	}

	var sb strings.Builder
	g.writeHeader(&sb)
	g.writeCheckTrigger(&sb)
	g.writeCheckJob(&sb)
	return sb.String(), nil
}

func (g *ReconcileGenerator) writeHeader(sb *strings.Builder) {
	sb.WriteString(GeneratedFileMarker + "\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n\n", g.getManifestFilePath())
}

// writeCheckTrigger emits the pull_request trigger and the read-only
// top-level permissions. A fork PR gets a read-only token and no secrets, so
// this job cannot push or comment; it captures relevance as an artifact
// instead.
func (g *ReconcileGenerator) writeCheckTrigger(sb *strings.Builder) {
	fmt.Fprintf(sb, "name: %s\n\n", reconcileCheckWorkflowName)
	sb.WriteString("on:\n")
	sb.WriteString("  pull_request:\n")
	sb.WriteString("\npermissions:\n")
	sb.WriteString("  contents: read\n")
	sb.WriteString("\nconcurrency:\n")
	sb.WriteString("  group: \"cascade-reconcile-check-${{ github.event.pull_request.number }}\"\n")
	sb.WriteString("  cancel-in-progress: true\n")
}

func (g *ReconcileGenerator) writeCheckJob(sb *strings.Builder) {
	sb.WriteString("\njobs:\n")
	sb.WriteString("  reconcile-check:\n")
	sb.WriteString("    name: Pin Reconcile Detector\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	// Re-state read-only permissions at job scope so the job is read-only
	// regardless of any future change to the workflow default.
	sb.WriteString("    permissions:\n")
	sb.WriteString("      contents: read\n")
	sb.WriteString("    steps:\n")

	// Full history is required so the detector can diff base..head to find the
	// changed workflow files a governed pin bump would land in.
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")
	sb.WriteString("\n")

	// github.token is the built-in Actions token, sufficient to authenticate
	// gh release download against the public stablekernel/cascade repository.
	writeSetupCLIStep(sb, setupCLIStep{
		ref:     g.getCLIRef(),
		version: g.config.GetCLIVersion(),
		token:   "${{ github.token }}",
	})
	sb.WriteString("\n")

	g.writeChangedFilesStep(sb)
	g.writeCheckStep(sb)

	sb.WriteString("      - name: Upload pin-reconcile result\n")
	sb.WriteString("        if: always()\n")
	writeActionUses(sb, g.config, "        ", actionUploadArtifact)
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          name: %s\n", reconcileCheckArtifact)
	fmt.Fprintf(sb, "          path: %s\n", reconcileCheckArtifactFile)
	sb.WriteString("          retention-days: 1\n")
}

// writeChangedFilesStep lists the PR's changed workflow files (the only files
// a governed pin bump lands in) into changed-files.txt for the next step to
// feed to `cascade reconcile --check`. pull_request event fields are
// attacker-influenceable on a fork PR, so the SHAs are routed through env: and
// referenced as quoted shell variables rather than interpolated into the
// script.
func (g *ReconcileGenerator) writeChangedFilesStep(sb *strings.Builder) {
	sb.WriteString("      - name: List changed workflow files\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          BASE_SHA: ${{ github.event.pull_request.base.sha }}\n")
	sb.WriteString("          HEAD_SHA: ${{ github.event.pull_request.head.sha }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          git diff --name-only \"$BASE_SHA\" \"$HEAD_SHA\" -- .github/workflows/ > changed-files.txt\n")
	sb.WriteString("\n")
}

// writeCheckStep runs the real `cascade reconcile --check` command (never a
// hand-rolled shell/yq scan) so the detector's relevance decision exactly
// matches what a subsequent reconcile would adopt. The manifest flags keep the
// detector consistent with the companion's write invocation on a repo with a
// non-default manifest path or key.
func (g *ReconcileGenerator) writeCheckStep(sb *strings.Builder) {
	sb.WriteString("      - name: Check for a governed pin change\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          args=()\n")
	sb.WriteString("          while IFS= read -r f; do\n")
	sb.WriteString("            [ -n \"$f\" ] && args+=(--changed-file \"$f\")\n")
	sb.WriteString("          done < changed-files.txt\n")
	fmt.Fprintf(sb, "          cascade reconcile --check --check-output %s %s \"${args[@]}\"\n", reconcileCheckArtifactFile, g.manifestFlags())
}

// manifestFlags renders the --config/--manifest-key pair every emitted
// reconcile invocation carries, so the command operates on the manifest this
// workflow was generated from rather than the default path and key.
func (g *ReconcileGenerator) manifestFlags() string {
	return fmt.Sprintf("--config %q --manifest-key %q", g.getManifestFilePath(), g.config.GetManifestKey())
}

// getManifestFilePath returns the repo-relative manifest path for use in the
// generated workflow, matching the sibling generators' resolution.
func (g *ReconcileGenerator) getManifestFilePath() string {
	return relativeManifestPath(g.config, g.baseDir)
}

// getStateTokenRef returns the token expression used to push the reconcile
// adoption commit. Users configure the full expression via the state_token
// config option, and it defaults to GITHUB_TOKEN.
func (g *ReconcileGenerator) getStateTokenRef() string {
	return resolveStateTokenRef(g.config)
}

// GenerateCompanion creates the workflow_run reconcile-companion workflow
// content. It resolves the target PR from trusted workflow_run run metadata
// only, fetches the PR's head files as data (the trusted refs/pull/<n>/head
// ref, never a direct checkout of a fork repository), obtains a pinned
// release binary, runs the real (idempotent) `cascade reconcile` command, and
// pushes the adoption commit with the trigger-capable state token, guarded
// against a stale or superseded PR head.
func (g *ReconcileGenerator) GenerateCompanion() (string, error) {
	if !g.Enabled() {
		return "", fmt.Errorf("cannot generate reconcile companion workflow: reconcile is not enabled")
	}

	if g.ownRepo {
		return g.generateOwnRepoCompanion(), nil
	}

	var sb strings.Builder
	g.writeHeader(&sb)
	g.writeCompanionTrigger(&sb)
	g.writeCompanionJob(&sb)
	return sb.String(), nil
}

// writeCompanionTrigger emits the workflow_run trigger and the locked-down
// top-level permissions for the companion.
func (g *ReconcileGenerator) writeCompanionTrigger(sb *strings.Builder) {
	sb.WriteString("name: Cascade Reconcile Companion\n\n")
	sb.WriteString("on:\n")
	sb.WriteString("  workflow_run:\n")
	fmt.Fprintf(sb, "    workflows: [%q]\n", reconcileCheckWorkflowName)
	sb.WriteString("    types: [completed]\n")
	// Default to no permissions; the single job opts into the minimum it needs.
	sb.WriteString("\npermissions: {}\n")
	// Serialize companion runs per source run so two rapid pushes cannot race
	// two reconcile jobs and double-commit before the fresh-tip guard settles.
	sb.WriteString("\nconcurrency:\n")
	sb.WriteString("  group: \"cascade-reconcile-companion-${{ github.event.workflow_run.id }}\"\n")
	sb.WriteString("  cancel-in-progress: false\n")
}

func (g *ReconcileGenerator) writeCompanionJob(sb *strings.Builder) {
	sb.WriteString("\njobs:\n")
	sb.WriteString("  reconcile:\n")
	sb.WriteString("    name: Adopt governed pin change\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	// Only act on PR-triggered source runs.
	sb.WriteString("    if: github.event.workflow_run.event == 'pull_request'\n")
	sb.WriteString("    permissions:\n")
	sb.WriteString("      contents: write\n")
	sb.WriteString("      pull-requests: write\n")
	sb.WriteString("      actions: read\n")
	sb.WriteString("    steps:\n")

	g.writeDownloadStep(sb)
	g.writeResolveStep(sb)
	g.writeCompanionCheckoutStep(sb)
	g.writeCompanionSetupCLIStep(sb)
	g.writeCompanionReconcileStep(sb)

	// Commit routing: "followup" always lands as a separate PR against a
	// cascade-owned branch (safe regardless of fork, and the recommended
	// posture for a PR that automerges without further review). The default
	// "append" mode pushes onto the PR's own head branch, but only when the PR
	// is NOT a fork: cascade has no push access to a fork's branch, so a fork
	// PR always falls back to a sticky comment instead, never an in-place push.
	if g.commitMode() == config.ReconcileCommitFollowup {
		g.writeCompanionFollowupSteps(sb)
		return
	}
	g.writeCompanionAppendStep(sb)
	g.writeCompanionForkFallbackStep(sb)
}

// commitMode returns the configured commit-routing mode, defaulting to
// "append" when unset.
func (g *ReconcileGenerator) commitMode() string {
	if g.config.Reconcile == nil || g.config.Reconcile.Commit == "" {
		return config.ReconcileCommitAppend
	}
	return g.config.Reconcile.Commit
}

func (g *ReconcileGenerator) writeDownloadStep(sb *strings.Builder) {
	sb.WriteString("      - name: Download pin-reconcile result\n")
	sb.WriteString("        id: download\n")
	sb.WriteString("        continue-on-error: true\n")
	writeActionUses(sb, g.config, "        ", actionDownloadArtifact)
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          name: %s\n", reconcileCheckArtifact)
	fmt.Fprintf(sb, "          path: %s\n", reconcileCheckArtifact)
	sb.WriteString("          run-id: ${{ github.event.workflow_run.id }}\n")
	sb.WriteString("          github-token: ${{ github.token }}\n")
	sb.WriteString("\n")
}

// writeResolveStep derives the target PR number ONLY from trusted
// workflow_run run metadata, never from the fork-controlled artifact, and
// re-fetches the PR fresh so a superseded run aborts rather than reconciling
// stale data (part of the fresh-tip loop guard). It reads the artifact's
// relevance flag strictly as data.
func (g *ReconcileGenerator) writeResolveStep(sb *strings.Builder) {
	sb.WriteString("      - name: Resolve target PR\n")
	sb.WriteString("        id: resolve\n")
	writeActionUses(sb, g.config, "        ", actionGithubScript)
	sb.WriteString("        with:\n")
	sb.WriteString("          script: |\n")
	g.writeResolveScript(sb)
}

func (g *ReconcileGenerator) writeResolveScript(sb *strings.Builder) {
	lines := []string{
		"const fs = require('fs');",
		"",
		"// Read the artifact's relevance flag (data only; never executed).",
		"let relevant = false;",
		"try {",
		fmt.Sprintf("  const raw = fs.readFileSync('%s/%s', 'utf8');", reconcileCheckArtifact, reconcileCheckArtifactFile),
		"  relevant = JSON.parse(raw).relevant === true;",
		"} catch (e) {",
		"  relevant = false;",
		"}",
		"",
		"// Resolve the target PR ONLY from trusted workflow_run metadata. The",
		"// artifact is produced by the (possibly fork) source run and is",
		"// attacker-controlled, so it must never decide which PR we touch.",
		"const run = context.payload.workflow_run;",
		"let prNumber;",
		"if (run.pull_requests && run.pull_requests.length > 0) {",
		"  prNumber = run.pull_requests[0].number;",
		"} else {",
		"  const associated = await github.rest.repos.listPullRequestsAssociatedWithCommit({",
		"    owner: context.repo.owner,",
		"    repo: context.repo.repo,",
		"    commit_sha: run.head_sha,",
		"  });",
		"  const match = associated.data.find((pr) => pr.head.sha === run.head_sha);",
		"  if (match) {",
		"    prNumber = match.number;",
		"  }",
		"}",
		"if (!Number.isInteger(prNumber) || prNumber <= 0) {",
		"  core.info('No PR resolved from workflow_run metadata; nothing to do.');",
		"  core.setOutput('relevant', 'false');",
		"  return;",
		"}",
		"",
		"// Re-fetch the PR fresh so the reconcile targets the current tip, not a",
		"// stale snapshot from workflow_run metadata (the fresh-tip loop guard).",
		"const pr = await github.rest.pulls.get({",
		"  owner: context.repo.owner,",
		"  repo: context.repo.repo,",
		"  pull_number: prNumber,",
		"});",
		"if (pr.data.head.sha !== run.head_sha) {",
		"  core.info(`Run head ${run.head_sha} is superseded by PR head ${pr.data.head.sha}; ` +",
		"    'aborting rather than reconcile stale data.');",
		"  core.setOutput('relevant', 'false');",
		"  return;",
		"}",
		"",
		"core.setOutput('pr_number', String(prNumber));",
		"core.setOutput('base_sha', pr.data.base.sha);",
		"core.setOutput('base_ref', pr.data.base.ref);",
		"core.setOutput('head_sha', pr.data.head.sha);",
		"core.setOutput('head_ref', pr.data.head.ref);",
		"core.setOutput('fork', String(pr.data.head.repo && pr.data.head.repo.full_name !== pr.data.base.repo.full_name));",
		"core.setOutput('relevant', relevant ? 'true' : 'false');",
	}
	for _, l := range lines {
		if l == "" {
			sb.WriteString("\n")
			continue
		}
		fmt.Fprintf(sb, "            %s\n", l)
	}
}

// writeCompanionCheckoutStep fetches the PR's head files as DATA via the
// trusted refs/pull/<n>/head ref on the base repo, never a direct checkout of
// a fork repository, so nothing from a fork's own configuration is executed.
// It checks out with the state token (the push identity) so the later git
// push is not blocked by branch protection.
func (g *ReconcileGenerator) writeCompanionCheckoutStep(sb *strings.Builder) {
	sb.WriteString("      - name: Checkout PR head (data only)\n")
	sb.WriteString("        if: steps.resolve.outputs.relevant == 'true'\n")
	writeActionUses(sb, g.config, "        ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          ref: refs/pull/${{ steps.resolve.outputs.pr_number }}/head\n")
	sb.WriteString("          fetch-depth: 0\n")
	fmt.Fprintf(sb, "          token: %s\n", g.getStateTokenRef())
	sb.WriteString("\n")
}

// writeCompanionSetupCLIStep obtains a PINNED release binary, never a `go
// run` off the repository's own (possibly malicious) source tree.
func (g *ReconcileGenerator) writeCompanionSetupCLIStep(sb *strings.Builder) {
	writeSetupCLIStep(sb, setupCLIStep{
		ref:     g.getCLIRef(),
		version: g.config.GetCLIVersion(),
		token:   "${{ github.token }}",
		ifExpr:  "steps.resolve.outputs.relevant == 'true'",
	})
	sb.WriteString("\n")
}

// writeCompanionReconcileStep runs the real (idempotent) `cascade reconcile`
// command against the changed workflow files, the loop-termination guard (a):
// the write round-trips through the typed command, never a hand-rolled shell
// or yq edit of the manifest.
func (g *ReconcileGenerator) writeCompanionReconcileStep(sb *strings.Builder) {
	sb.WriteString("      - name: Reconcile the governed pin change\n")
	sb.WriteString("        if: steps.resolve.outputs.relevant == 'true'\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          BASE_SHA: ${{ steps.resolve.outputs.base_sha }}\n")
	sb.WriteString("          HEAD_SHA: ${{ steps.resolve.outputs.head_sha }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          git diff --name-only \"$BASE_SHA\" \"$HEAD_SHA\" -- .github/workflows/ > changed-files.txt\n")
	sb.WriteString("          args=()\n")
	sb.WriteString("          while IFS= read -r f; do\n")
	sb.WriteString("            [ -n \"$f\" ] && args+=(--changed-file \"$f\")\n")
	sb.WriteString("          done < changed-files.txt\n")
	fmt.Fprintf(sb, "          cascade reconcile %s \"${args[@]}\"\n", g.manifestFlags())
	sb.WriteString("\n")
}

// writeCompanionAppendStep pushes the adoption commit directly onto the PR's
// own head branch (the default "append" mode), gated off entirely for a fork
// PR: cascade has no push access to a fork's branch, so this step never runs
// for one (see writeCompanionForkFallbackStep). It applies the remaining
// loop-termination guards: (b) push-only-if-nonempty (a converged tree
// pushes nothing) and (c) reconcile-against-fresh-tip, aborting on a
// non-fast-forward rather than force-pushing over commits made since this run
// started.
func (g *ReconcileGenerator) writeCompanionAppendStep(sb *strings.Builder) {
	sb.WriteString("      - name: Push the reconcile commit\n")
	sb.WriteString("        if: steps.resolve.outputs.relevant == 'true' && steps.resolve.outputs.fork != 'true'\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          HEAD_REF: ${{ steps.resolve.outputs.head_ref }}\n")
	sb.WriteString("          HEAD_SHA: ${{ steps.resolve.outputs.head_sha }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          if git diff --quiet && git diff --cached --quiet; then\n")
	sb.WriteString("            echo \"No pending reconcile changes; nothing to push.\"\n")
	sb.WriteString("            exit 0\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          git config user.name \"github-actions[bot]\"\n")
	sb.WriteString("          git config user.email \"github-actions[bot]@users.noreply.github.com\"\n")
	sb.WriteString("          git add .github\n")
	fmt.Fprintf(sb, "          git commit -m %q\n", reconcileCommitMessage)
	sb.WriteString("          git fetch origin \"$HEAD_REF\"\n")
	sb.WriteString("          FRESH_TIP=$(git rev-parse \"origin/$HEAD_REF\")\n")
	sb.WriteString("          if [ \"$FRESH_TIP\" != \"$HEAD_SHA\" ]; then\n")
	sb.WriteString("            echo \"PR head moved since this run started; aborting rather than overwrite newer commits.\"\n")
	sb.WriteString("            exit 0\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          git push origin \"HEAD:$HEAD_REF\"\n")
	sb.WriteString("\n")
}

// writeCompanionForkFallbackStep is the mandatory fallback for a fork PR
// under the default "append" mode: cascade cannot push onto a fork's own
// branch, so it posts (or updates) a sticky comment naming the governed refs
// the PR should adopt, read strictly as data from the check artifact. It
// never checks out or executes fork code and never opens a PR on the fork's
// behalf.
func (g *ReconcileGenerator) writeCompanionForkFallbackStep(sb *strings.Builder) {
	sb.WriteString("      - name: Fork fallback (sticky comment)\n")
	sb.WriteString("        if: steps.resolve.outputs.relevant == 'true' && steps.resolve.outputs.fork == 'true'\n")
	writeActionUses(sb, g.config, "        ", actionGithubScript)
	sb.WriteString("        with:\n")
	sb.WriteString("          script: |\n")
	g.writeForkFallbackScript(sb)
}

func (g *ReconcileGenerator) writeForkFallbackScript(sb *strings.Builder) {
	lines := []string{
		"const fs = require('fs');",
		fmt.Sprintf("const marker = '%s';", reconcileCommentMarker),
		"const owner = context.repo.owner;",
		"const repo = context.repo.repo;",
		"const prNumber = Number(" + "'${{ steps.resolve.outputs.pr_number }}'" + ");",
		"",
		"// Read the changed refs strictly as data from the check artifact; this",
		"// PR is a fork, so cascade never checks out or executes its code.",
		"let changed = {};",
		"try {",
		fmt.Sprintf("  const raw = fs.readFileSync('%s/%s', 'utf8');", reconcileCheckArtifact, reconcileCheckArtifactFile),
		"  changed = JSON.parse(raw).changed_refs || {};",
		"} catch (e) {",
		"  changed = {};",
		"}",
		"",
		"const lines = Object.entries(changed).map(([action, ref]) => `- \\`${action}\\`: ${ref}`);",
		"const body = [",
		"  marker,",
		"  '## Governed action pin changed',",
		"  '',",
		"  'This pull request bumps a cascade-governed action pin. Cascade cannot push',",
		"  'to a fork branch, so adopt it by hand in `action_pins`:',",
		"  '',",
		"  ...lines,",
		"].join('\\n');",
		"",
		"const comments = await github.paginate(github.rest.issues.listComments, {",
		"  owner, repo, issue_number: prNumber,",
		"});",
		"const existing = comments.find((c) => c.body && c.body.includes(marker));",
		"if (existing) {",
		"  await github.rest.issues.updateComment({ owner, repo, comment_id: existing.id, body });",
		"} else {",
		"  await github.rest.issues.createComment({ owner, repo, issue_number: prNumber, body });",
		"}",
	}
	for _, l := range lines {
		if l == "" {
			sb.WriteString("\n")
			continue
		}
		fmt.Fprintf(sb, "            %s\n", l)
	}
}

// writeCompanionFollowupSteps implements the "followup" commit mode: the
// adoption commit lands on a cascade-owned, deterministic branch (never the
// PR's own head branch, so it is safe regardless of fork), and a distinct PR
// against the same base branch is opened (or updated on a repeat run) rather
// than silently mutating a PR that may automerge without further review.
// cascade owns this branch exclusively, so, unlike the append-mode push onto
// a user's own PR branch, force-pushing it on every run is safe: each run
// starts from a fresh checkout of the current PR head, so the branch's base
// commit legitimately changes whenever the PR head does.
func (g *ReconcileGenerator) writeCompanionFollowupSteps(sb *strings.Builder) {
	sb.WriteString("      - name: Push the followup branch\n")
	sb.WriteString("        if: steps.resolve.outputs.relevant == 'true'\n")
	sb.WriteString("        id: followup_push\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          if git diff --quiet && git diff --cached --quiet; then\n")
	sb.WriteString("            echo \"No pending reconcile changes; nothing to push.\"\n")
	sb.WriteString("            echo \"pushed=false\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("            exit 0\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          git config user.name \"github-actions[bot]\"\n")
	sb.WriteString("          git config user.email \"github-actions[bot]@users.noreply.github.com\"\n")
	fmt.Fprintf(sb, "          git checkout -b %q\n", reconcileFollowupBranchExpr)
	sb.WriteString("          git add .github\n")
	fmt.Fprintf(sb, "          git commit -m %q\n", reconcileCommitMessage)
	fmt.Fprintf(sb, "          git push --force origin \"HEAD:%s\"\n", reconcileFollowupBranchExpr)
	sb.WriteString("          echo \"pushed=true\" >> \"$GITHUB_OUTPUT\"\n")
	sb.WriteString("\n")

	sb.WriteString("      - name: Open or update the followup PR\n")
	sb.WriteString("        if: steps.followup_push.outputs.pushed == 'true'\n")
	writeActionUses(sb, g.config, "        ", actionGithubScript)
	sb.WriteString("        with:\n")
	sb.WriteString("          script: |\n")
	g.writeFollowupPRScript(sb)
}

func (g *ReconcileGenerator) writeFollowupPRScript(sb *strings.Builder) {
	lines := []string{
		"const owner = context.repo.owner;",
		"const repo = context.repo.repo;",
		"const prNumber = '${{ steps.resolve.outputs.pr_number }}';",
		"const baseRef = '${{ steps.resolve.outputs.base_ref }}';",
		fmt.Sprintf("const head = '%s';", reconcileFollowupBranchExpr),
		"const title = 'chore: reconcile governed action pins';",
		"const body = `Adopts the governed action-pin change from #${prNumber}.`;",
		"",
		"const existing = await github.paginate(github.rest.pulls.list, {",
		"  owner, repo, head: `${owner}:${head}`, state: 'open',",
		"});",
		"if (existing.length > 0) {",
		"  core.info(`Followup PR #${existing[0].number} already open; the push updated it.`);",
		"  return;",
		"}",
		"await github.rest.pulls.create({ owner, repo, title, body, head, base: baseRef });",
	}
	for _, l := range lines {
		if l == "" {
			sb.WriteString("\n")
			continue
		}
		fmt.Fprintf(sb, "            %s\n", l)
	}
}

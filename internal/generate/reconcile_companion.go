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
}

// NewReconcileGenerator creates a new reconcile companion workflow generator.
func NewReconcileGenerator(cfg *config.TrunkConfig, baseDir string) *ReconcileGenerator {
	return &ReconcileGenerator{config: cfg, baseDir: baseDir}
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
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n\n", g.config.GetManifestFile())
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

	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())
	// github.token is the built-in Actions token, sufficient to authenticate
	// gh release download against the public stablekernel/cascade repository.
	sb.WriteString("          token: ${{ github.token }}\n")
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
// matches what a subsequent reconcile would adopt.
func (g *ReconcileGenerator) writeCheckStep(sb *strings.Builder) {
	sb.WriteString("      - name: Check for a governed pin change\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          args=()\n")
	sb.WriteString("          while IFS= read -r f; do\n")
	sb.WriteString("            [ -n \"$f\" ] && args+=(--changed-file \"$f\")\n")
	sb.WriteString("          done < changed-files.txt\n")
	fmt.Fprintf(sb, "          cascade reconcile --check --check-output %s \"${args[@]}\"\n", reconcileCheckArtifactFile)
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
	g.writeCompanionPushStep(sb)
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
	sb.WriteString("      - name: Setup CLI\n")
	sb.WriteString("        if: steps.resolve.outputs.relevant == 'true'\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())
	sb.WriteString("          token: ${{ github.token }}\n")
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
	sb.WriteString("          cascade reconcile \"${args[@]}\"\n")
	sb.WriteString("\n")
}

// writeCompanionPushStep pushes the adoption commit onto the PR head branch,
// applying the remaining loop-termination guards: (b) push-only-if-nonempty
// (a converged tree pushes nothing) and (c) reconcile-against-fresh-tip,
// aborting on a non-fast-forward rather than force-pushing over commits made
// since this run started.
func (g *ReconcileGenerator) writeCompanionPushStep(sb *strings.Builder) {
	sb.WriteString("      - name: Push the reconcile commit\n")
	sb.WriteString("        if: steps.resolve.outputs.relevant == 'true'\n")
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
}

package generate

import (
	"fmt"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// driftCheckMarker is the hidden HTML marker embedded in the drift comment so a
// later run finds and updates the same sticky comment instead of posting a new
// one. It matches the marker cascade uses for its own dogfood drift comment.
const driftCheckMarker = "<!-- cascade-drift-check -->"

// driftCheckArtifact is the name of the artifact the pull_request job uploads
// and the workflow_run companion downloads. It carries the verify report and the
// captured exit code as data only; it never decides which PR is commented on.
const driftCheckArtifact = "cascade-drift-result"

// driftCheckWorkflowName is the workflow name the pull_request lane runs under.
// The companion subscribes to completed runs of this exact name, so the two
// generated files must agree on it.
const driftCheckWorkflowName = "Cascade Drift Check"

// driftCommentWorkflowName is the workflow name the fork-safe comment companion
// runs under.
const driftCommentWorkflowName = "Cascade Drift Comment"

// DriftCheckGenerator emits the opt-in workflow drift-check lane (#229).
//
// It mirrors cascade's own dogfood: a pull_request job builds nothing
// dangerous, runs cascade verify against the manifest, captures the result as an
// artifact, and re-exits on drift to keep the PR gate red. The pull_request job
// is strictly read-only (contents: read, no secrets, posts no comment) so a fork
// PR cannot abuse it.
//
// When Comment is enabled it also emits a fork-safe workflow_run companion that
// runs in the base-repo context with a scoped write token, downloads the
// artifact (data only), and posts a sticky comment. The companion derives the
// target PR number ONLY from trusted workflow_run run metadata (the source run's
// pull_requests array, or a head-SHA lookup for fork PRs), never from the
// fork-controlled artifact, so a fork cannot redirect the comment at another PR.
type DriftCheckGenerator struct {
	installModeHolder
	config  *config.TrunkConfig
	baseDir string
}

// NewDriftCheckGenerator creates a new drift-check workflow generator.
func NewDriftCheckGenerator(cfg *config.TrunkConfig, baseDir string) *DriftCheckGenerator {
	return &DriftCheckGenerator{config: cfg, baseDir: baseDir}
}

// Enabled reports whether the drift-check lane should be emitted.
func (g *DriftCheckGenerator) Enabled() bool {
	return g.config.DriftCheck != nil && g.config.DriftCheck.Enabled
}

// commentEnabled reports whether the fork-safe comment companion should also be
// emitted alongside the pull_request check.
func (g *DriftCheckGenerator) commentEnabled() bool {
	return g.config.DriftCheck != nil && g.config.DriftCheck.Comment
}

// getCLIRef returns the Git ref for the cascade self-action. The default
// (cli_version unset or "latest") resolves to an immutable release tag, so
// consumers never run an unpinned mutable ref; "beta" opts in to "master".
func (g *DriftCheckGenerator) getCLIRef() string {
	return cliSetupRef(g.config)
}

// getManifestFilePath returns the repo-relative manifest path for use in the
// generated workflow, matching the sibling generators' resolution. An absolute
// manifest_file would otherwise bake the generating machine's path into the
// emitted workflow, where it cannot resolve in a checked-out repo.
func (g *DriftCheckGenerator) getManifestFilePath() string {
	return relativeManifestPath(g.config, g.baseDir)
}

// Generate creates the pull_request drift-check workflow content.
func (g *DriftCheckGenerator) Generate() (string, error) {
	if !g.Enabled() {
		return "", fmt.Errorf("cannot generate drift-check workflow: drift_check is not enabled")
	}

	var sb strings.Builder
	g.writeHeader(&sb)
	g.writeCheckTrigger(&sb)
	g.writeCheckJob(&sb)
	return sb.String(), nil
}

// GenerateComment creates the fork-safe workflow_run comment companion content.
// Callers must check commentEnabled (via the command/plan wiring) before
// emitting this file.
func (g *DriftCheckGenerator) GenerateComment() (string, error) {
	if !g.Enabled() || !g.commentEnabled() {
		return "", fmt.Errorf("cannot generate drift-comment workflow: drift_check.comment is not enabled")
	}

	var sb strings.Builder
	g.writeHeader(&sb)
	g.writeCommentTrigger(&sb)
	g.writeCommentJob(&sb)
	return sb.String(), nil
}

func (g *DriftCheckGenerator) writeHeader(sb *strings.Builder) {
	sb.WriteString(GeneratedFileMarker + "\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n\n", g.getManifestFilePath())
}

// writeCheckTrigger emits the pull_request trigger and the read-only top-level
// permissions. A fork PR gets a read-only token and no secrets, so this job
// cannot comment or write; it captures the drift result as an artifact instead.
func (g *DriftCheckGenerator) writeCheckTrigger(sb *strings.Builder) {
	fmt.Fprintf(sb, "name: %s\n\n", driftCheckWorkflowName)
	sb.WriteString("on:\n")
	sb.WriteString("  pull_request:\n")
	sb.WriteString("\npermissions:\n")
	sb.WriteString("  contents: read\n")
	sb.WriteString("\nconcurrency:\n")
	sb.WriteString("  group: \"cascade-drift-check-${{ github.event.pull_request.number }}\"\n")
	sb.WriteString("  cancel-in-progress: true\n")
}

func (g *DriftCheckGenerator) writeCheckJob(sb *strings.Builder) {
	sb.WriteString("\njobs:\n")
	sb.WriteString("  drift-check:\n")
	sb.WriteString("    name: Workflow Drift Check\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	// Re-state read-only permissions at job scope so the job is read-only
	// regardless of any future change to the workflow default.
	sb.WriteString("    permissions:\n")
	sb.WriteString("      contents: read\n")
	sb.WriteString("    steps:\n")

	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("\n")

	// github.token is the built-in Actions token, sufficient to authenticate
	// gh release download against the public stablekernel/cascade repository.
	writeSetupCLIStep(sb, setupCLIStep{
		installMode: g.installMode,
		ref:         g.getCLIRef(),
		version:     g.config.GetCLIVersion(),
		token:       "${{ github.token }}",
	})
	sb.WriteString("\n")

	// Run verify, capturing stdout/stderr and the exit code without failing the
	// step, so the result is always uploaded for the companion to comment on.
	sb.WriteString("      - name: Check for workflow drift\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          set +e\n")
	fmt.Fprintf(sb, "          cascade verify --config %s > drift-report.txt 2>&1\n", g.getManifestFilePath())
	sb.WriteString("          echo $? > drift-exit.txt\n")
	sb.WriteString("          set -e\n")
	sb.WriteString("          cat drift-report.txt\n")
	sb.WriteString("\n")

	if g.commentEnabled() {
		sb.WriteString("      - name: Upload drift result\n")
		sb.WriteString("        if: always()\n")
		writeActionUses(sb, g.config, "        ", actionUploadArtifact)
		sb.WriteString("        with:\n")
		fmt.Fprintf(sb, "          name: %s\n", driftCheckArtifact)
		sb.WriteString("          path: |\n")
		sb.WriteString("            drift-report.txt\n")
		sb.WriteString("            drift-exit.txt\n")
		sb.WriteString("          retention-days: 1\n")
		sb.WriteString("\n")
	}

	sb.WriteString("      - name: Fail on drift\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          CODE=$(cat drift-exit.txt)\n")
	sb.WriteString("          if [ \"$CODE\" != \"0\" ]; then\n")
	sb.WriteString("            echo \"Workflows are out of sync with the manifest.\"\n")
	fmt.Fprintf(sb, "            echo \"Run: cascade generate-workflow --config %s --force\"\n", g.getManifestFilePath())
	sb.WriteString("            echo \"Then commit the regenerated workflows.\"\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          exit \"$CODE\"\n")
}

// writeCommentTrigger emits the workflow_run trigger and the locked-down
// top-level permissions for the comment companion.
func (g *DriftCheckGenerator) writeCommentTrigger(sb *strings.Builder) {
	fmt.Fprintf(sb, "name: %s\n\n", driftCommentWorkflowName)
	sb.WriteString("on:\n")
	sb.WriteString("  workflow_run:\n")
	fmt.Fprintf(sb, "    workflows: [%q]\n", driftCheckWorkflowName)
	sb.WriteString("    types: [completed]\n")
	// Default to no permissions; the single job opts into the minimum it needs.
	sb.WriteString("\npermissions: {}\n")
	// Serialize companion runs per source run so two rapid pushes cannot race
	// two comment jobs and double-post before the sticky-marker lookup settles.
	sb.WriteString("\nconcurrency:\n")
	sb.WriteString("  group: \"cascade-drift-comment-${{ github.event.workflow_run.id }}\"\n")
	sb.WriteString("  cancel-in-progress: false\n")
}

func (g *DriftCheckGenerator) writeCommentJob(sb *strings.Builder) {
	sb.WriteString("\njobs:\n")
	sb.WriteString("  comment:\n")
	sb.WriteString("    name: Comment on drift\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	// Only act on PR-triggered source runs.
	sb.WriteString("    if: github.event.workflow_run.event == 'pull_request'\n")
	sb.WriteString("    permissions:\n")
	sb.WriteString("      pull-requests: write\n")
	sb.WriteString("      actions: read\n")
	sb.WriteString("    steps:\n")

	sb.WriteString("      - name: Download drift result\n")
	sb.WriteString("        id: download\n")
	sb.WriteString("        continue-on-error: true\n")
	writeActionUses(sb, g.config, "        ", actionDownloadArtifact)
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          name: %s\n", driftCheckArtifact)
	fmt.Fprintf(sb, "          path: %s\n", driftCheckArtifact)
	sb.WriteString("          run-id: ${{ github.event.workflow_run.id }}\n")
	sb.WriteString("          github-token: ${{ github.token }}\n")
	sb.WriteString("\n")

	sb.WriteString("      - name: Post or update sticky comment\n")
	sb.WriteString("        if: steps.download.outcome == 'success'\n")
	writeActionUses(sb, g.config, "        ", actionGithubScript)
	sb.WriteString("        with:\n")
	sb.WriteString("          script: |\n")
	g.writeCommentScript(sb)
}

// writeCommentScript emits the github-script body. It derives the target PR
// number ONLY from trusted workflow_run run metadata, never from the
// fork-controlled artifact, and never checks out or executes PR head code.
func (g *DriftCheckGenerator) writeCommentScript(sb *strings.Builder) {
	lines := []string{
		"const fs = require('fs');",
		fmt.Sprintf("const marker = '%s';", driftCheckMarker),
		"",
		"// Read artifact files (data only; never executed).",
		"const read = (name) => {",
		"  try {",
		fmt.Sprintf("    return fs.readFileSync(`%s/${name}`, 'utf8');", driftCheckArtifact),
		"  } catch (e) {",
		"    return '';",
		"  }",
		"};",
		"",
		"// Resolve the target PR ONLY from trusted workflow_run metadata.",
		"// The artifact is produced by the (possibly fork) source run and is",
		"// attacker-controlled, so it must never decide which PR we touch.",
		"const run = context.payload.workflow_run;",
		"let prNumber;",
		"if (run.pull_requests && run.pull_requests.length > 0) {",
		"  // Same-repo PRs populate this array directly.",
		"  prNumber = run.pull_requests[0].number;",
		"} else {",
		"  // Fork PRs leave it empty; resolve via the head SHA instead.",
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
		"  return;",
		"}",
		"",
		"const exitRaw = read('drift-exit.txt').trim();",
		"const drift = exitRaw !== '0';",
		"const report = read('drift-report.txt');",
		"",
		"// Find an existing sticky comment by the hidden marker.",
		"const comments = await github.paginate(",
		"  github.rest.issues.listComments,",
		"  { owner: context.repo.owner, repo: context.repo.repo, issue_number: prNumber }",
		");",
		"const existing = comments.find((c) => c.body && c.body.includes(marker));",
		"",
		"// Build the body in JS from the file contents. The report is plain",
		"// text from cascade verify; it is fenced, never evaluated.",
		"let body;",
		"if (drift) {",
		"  const trimmed = report.length > 60000",
		"    ? report.slice(0, 60000) + '\\n... (truncated)'",
		"    : report;",
		"  body = [",
		"    marker,",
		"    '## Workflow drift detected',",
		"    '',",
		"    'The generated workflows are out of sync with the manifest.',",
		"    '',",
		"    'To fix, run and commit the result:',",
		"    '',",
		"    '```',",
		fmt.Sprintf("    'cascade generate-workflow --config %s --force',", g.getManifestFilePath()),
		"    '```',",
		"    '',",
		"    '<details><summary>cascade verify output</summary>',",
		"    '',",
		"    '```',",
		"    trimmed,",
		"    '```',",
		"    '',",
		"    '</details>',",
		"  ].join('\\n');",
		"} else {",
		"  if (!existing) {",
		"    core.info('No drift and no existing comment; nothing to do.');",
		"    return;",
		"  }",
		"  body = [marker, 'No workflow drift detected.'].join('\\n');",
		"}",
		"",
		"if (existing) {",
		"  await github.rest.issues.updateComment({",
		"    owner: context.repo.owner,",
		"    repo: context.repo.repo,",
		"    comment_id: existing.id,",
		"    body,",
		"  });",
		"} else {",
		"  await github.rest.issues.createComment({",
		"    owner: context.repo.owner,",
		"    repo: context.repo.repo,",
		"    issue_number: prNumber,",
		"    body,",
		"  });",
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

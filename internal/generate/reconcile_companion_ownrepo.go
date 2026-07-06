package generate

import "strings"

// ownRepoCompanionWorkflowName is the workflow name cascade's own self-heal
// companion runs under.
const ownRepoCompanionWorkflowName = "Pin Reconcile"

// ownRepoSourceWorkflowName is the name of the detector workflow the own-repo
// companion subscribes to. Cascade's own reconcile detector lives in the
// "PR Validation" workflow (the workflow-drift job), so the companion keys on
// that completed run rather than the standalone "Cascade Reconcile Check" a
// downstream user emits.
const ownRepoSourceWorkflowName = "PR Validation"

// ownRepoReconcileCommitSubject is the DCO-signed subject of the adoption commit
// the own-repo companion pushes back onto the triggering branch.
const ownRepoReconcileCommitSubject = "ci: reconcile governed action pins"

// generateOwnRepoCompanion emits cascade's own self-heal companion. It differs
// from the downstream user companion in three ways and is otherwise the same
// workflow_run, trusted-metadata, same-repo-only design:
//
//  1. It installs the latest NON-prerelease cascade release from its published
//     asset, so cascade's own CI never self-installs an rc or a draft.
//  2. It scans both .github/workflows/ and .github/actions/ for a moved pin,
//     because cascade governs pins in its composite actions as well as its
//     workflows.
//  3. It runs `cascade reconcile --own-repo`, which writes the adopted ref into
//     internal/generate/action_pins.yaml and regenerates every generated
//     workflow, then commits the manifest plus the regenerated workflows.
//
// The output is drift-locked byte-for-byte against
// .github/workflows/pin-reconcile.yaml so a future hand-edit fails the suite.
func (g *ReconcileGenerator) generateOwnRepoCompanion() string {
	var sb strings.Builder

	sb.WriteString(GeneratedFileMarker + "\n")
	sb.WriteString("# Adopts an external action-pin bump back into cascade's own pin manifest and\n")
	sb.WriteString("# regenerates the workflows, so a governed pin that moved in a hand-written\n")
	sb.WriteString("# source file (a Dependabot bump, a manual edit) flows into\n")
	sb.WriteString("# internal/generate/action_pins.yaml and every generated workflow agrees again.\n")
	sb.WriteString("#\n")
	sb.WriteString("# Companion to " + ownRepoSourceWorkflowName + ", same shape as the emitted user\n")
	sb.WriteString("# companion. " + ownRepoSourceWorkflowName + " runs on pull_request, so for fork\n")
	sb.WriteString("# PRs it gets a read-only token and no secrets and cannot push. This workflow\n")
	sb.WriteString("# runs on workflow_run in the BASE repo context, resolves the target pull\n")
	sb.WriteString("# request ONLY from trusted workflow_run metadata, and reads the triggering\n")
	sb.WriteString("# run's uploaded pin-reconcile-result artifact strictly as data. It never\n")
	sb.WriteString("# executes pull request head code: it installs a PINNED cascade CLI from a\n")
	sb.WriteString("# published release asset and runs that trusted binary over the head files,\n")
	sb.WriteString("# which it treats as data.\n")
	sb.WriteString("#\n")
	sb.WriteString("# The self-heal push is same-repo only. A fork head can neither receive a push\n")
	sb.WriteString("# nor be handed the write token, so a fork pull request is skipped. The default\n")
	sb.WriteString("# token stays read-only; the branch write uses the trigger-capable state token,\n")
	sb.WriteString("# matching the act-image-repin and hotfix trunk jobs. The emitted commit keeps\n")
	sb.WriteString("# its DCO signoff and does not GPG-sign, matching the act-image-repin precedent\n")
	sb.WriteString("# (GPG signing is a local merge rule, not a CI rule).\n")
	sb.WriteString("name: " + ownRepoCompanionWorkflowName + "\n")
	sb.WriteString("\n")

	sb.WriteString("on:\n")
	sb.WriteString("  workflow_run:\n")
	sb.WriteString("    workflows: [\"" + ownRepoSourceWorkflowName + "\"]\n")
	sb.WriteString("    types: [completed]\n")
	sb.WriteString("\n")
	sb.WriteString("permissions: {}\n")
	sb.WriteString("\n")
	sb.WriteString("concurrency:\n")
	sb.WriteString("  group: pin-reconcile-${{ github.event.workflow_run.head_branch }}\n")
	sb.WriteString("  cancel-in-progress: false\n")
	sb.WriteString("\n")

	sb.WriteString("jobs:\n")
	sb.WriteString("  reconcile:\n")
	sb.WriteString("    name: Reconcile governed action pins\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    # Only act on PR-triggered source runs.\n")
	sb.WriteString("    if: github.event.workflow_run.event == 'pull_request'\n")
	sb.WriteString("    permissions:\n")
	sb.WriteString("      contents: read\n")
	sb.WriteString("      actions: read\n")
	sb.WriteString("      pull-requests: read\n")
	sb.WriteString("    env:\n")
	sb.WriteString("      GH_TOKEN: ${{ github.token }}\n")
	sb.WriteString("    steps:\n")

	g.writeOwnRepoDownloadStep(&sb)
	g.writeOwnRepoResolveStep(&sb)
	g.writeOwnRepoInstallStep(&sb)
	g.writeOwnRepoCheckoutStep(&sb)
	g.writeOwnRepoReconcileStep(&sb)
	g.writeOwnRepoCommitStep(&sb)

	return sb.String()
}

func (g *ReconcileGenerator) writeOwnRepoDownloadStep(sb *strings.Builder) {
	sb.WriteString("      - name: Download reconcile result\n")
	sb.WriteString("        id: download\n")
	sb.WriteString("        continue-on-error: true\n")
	writeActionUses(sb, g.config, "        ", actionDownloadArtifact)
	sb.WriteString("        with:\n")
	sb.WriteString("          name: pin-reconcile-result\n")
	sb.WriteString("          path: pin-reconcile-result\n")
	sb.WriteString("          run-id: ${{ github.event.workflow_run.id }}\n")
	sb.WriteString("          github-token: ${{ github.token }}\n")
	sb.WriteString("\n")
}

func (g *ReconcileGenerator) writeOwnRepoResolveStep(sb *strings.Builder) {
	sb.WriteString("      - name: Resolve target pull request and relevance\n")
	sb.WriteString("        id: resolve\n")
	writeActionUses(sb, g.config, "        ", actionGithubScript)
	sb.WriteString("        with:\n")
	sb.WriteString("          script: |\n")
	lines := []string{
		"const fs = require('fs');",
		"const owner = context.repo.owner;",
		"const repo = context.repo.repo;",
		"const run = context.payload.workflow_run;",
		"",
		"// Read the data-only relevance artifact (never executed). No",
		"// governed pin change means there is nothing to adopt.",
		"let relevant = false;",
		"try {",
		"  const raw = fs.readFileSync('pin-reconcile-result/pin-reconcile-result.json', 'utf8');",
		"  relevant = JSON.parse(raw).relevant === true;",
		"} catch (e) {",
		"  core.info(`No reconcile-result artifact to read: ${e.message}`);",
		"}",
		"if (!relevant) {",
		"  core.info('No governed pin change to adopt; nothing to do.');",
		"  core.setOutput('proceed', 'false');",
		"  return;",
		"}",
		"",
		"// Resolve the target pull request ONLY from trusted workflow_run",
		"// metadata. The artifact and the triggering run's contents are",
		"// attacker-controlled on a fork PR, so they must never decide which",
		"// branch we touch.",
		"let prNumber;",
		"if (run.pull_requests && run.pull_requests.length > 0) {",
		"  prNumber = run.pull_requests[0].number;",
		"} else {",
		"  const associated = await github.rest.repos.listPullRequestsAssociatedWithCommit({",
		"    owner, repo, commit_sha: run.head_sha,",
		"  });",
		"  const match = associated.data.find((pr) => pr.head.sha === run.head_sha);",
		"  if (match) { prNumber = match.number; }",
		"}",
		"if (!Number.isInteger(prNumber) || prNumber <= 0) {",
		"  core.info('No pull request resolved from workflow_run metadata; nothing to do.');",
		"  core.setOutput('proceed', 'false');",
		"  return;",
		"}",
		"",
		"const pr = await github.rest.pulls.get({ owner, repo, pull_number: prNumber });",
		"const head = pr.data.head;",
		"const base = pr.data.base;",
		"",
		"// Same-repo only. A fork head cannot receive a push and must never",
		"// be handed the write token, so it is skipped here.",
		"if (!head.repo || head.repo.full_name !== base.repo.full_name) {",
		"  core.info('Pull request head is on a fork; the self-heal push is same-repo only.');",
		"  core.setOutput('proceed', 'false');",
		"  return;",
		"}",
		"",
		"// head_sha guard: skip a superseded completion so a stale run cannot",
		"// rewrite a branch that already advanced.",
		"if (head.sha !== run.head_sha) {",
		"  core.info(`Run head ${run.head_sha} is superseded by branch head ${head.sha}; skipping.`);",
		"  core.setOutput('proceed', 'false');",
		"  return;",
		"}",
		"",
		"core.setOutput('proceed', 'true');",
		"core.setOutput('head_ref', head.ref);",
		"core.setOutput('head_sha', head.sha);",
		"core.setOutput('base_sha', base.sha);",
	}
	for _, l := range lines {
		if l == "" {
			sb.WriteString("\n")
			continue
		}
		sb.WriteString("            " + l + "\n")
	}
	sb.WriteString("\n")
}

// writeOwnRepoInstallStep installs the latest NON-prerelease cascade release.
// The --exclude-pre-releases and --exclude-drafts filters are the load-bearing
// difference from a naive `-L 1`: without them cascade's own CI would happily
// self-install an rc or a draft and reconcile against an unpublished binary.
func (g *ReconcileGenerator) writeOwnRepoInstallStep(sb *strings.Builder) {
	sb.WriteString("      - name: Install released cascade CLI\n")
	sb.WriteString("        if: steps.resolve.outputs.proceed == 'true'\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          set -euo pipefail\n")
	sb.WriteString("          # Install a PINNED cascade CLI from its published release asset, never\n")
	sb.WriteString("          # a binary built off pull request head. The --exclude-pre-releases and\n")
	sb.WriteString("          # --exclude-drafts filters keep cascade's own CI on a stable release,\n")
	sb.WriteString("          # never self-installing one of its own rc or draft tags.\n")
	sb.WriteString("          tag=\"$(gh release list -R stablekernel/cascade --exclude-pre-releases --exclude-drafts -L 1 --json tagName -q '.[0].tagName')\"\n")
	sb.WriteString("          if [ -z \"$tag\" ]; then\n")
	sb.WriteString("            echo \"::error::no published cascade release to install; cannot reconcile.\"\n")
	sb.WriteString("            exit 1\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          echo \"Installing cascade ${tag} from its released asset.\"\n")
	sb.WriteString("          tmp=\"$(mktemp -d)\"\n")
	sb.WriteString("          gh release download \"$tag\" \\\n")
	sb.WriteString("            -R stablekernel/cascade \\\n")
	sb.WriteString("            -p 'cascade_*_linux_amd64.tar.gz' \\\n")
	sb.WriteString("            -D \"$tmp\"\n")
	sb.WriteString("          tar -xzf \"$tmp\"/*.tar.gz -C \"$tmp\"\n")
	sb.WriteString("          install -m 0755 \"$tmp/cascade\" /usr/local/bin/cascade\n")
	sb.WriteString("          rm -rf \"$tmp\"\n")
	sb.WriteString("          cascade version\n")
	sb.WriteString("\n")
}

func (g *ReconcileGenerator) writeOwnRepoCheckoutStep(sb *strings.Builder) {
	sb.WriteString("      - name: Check out the pull request head\n")
	sb.WriteString("        if: steps.resolve.outputs.proceed == 'true'\n")
	writeActionUses(sb, g.config, "        ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          # The head branch is same-repo (guarded above). Full history lets the\n")
	sb.WriteString("          # non-fast-forward guard compare against the fresh remote tip. The\n")
	sb.WriteString("          # state token carries the branch write; the head files are read as\n")
	sb.WriteString("          # data and only the trusted released binary runs over them.\n")
	sb.WriteString("          ref: ${{ steps.resolve.outputs.head_ref }}\n")
	sb.WriteString("          fetch-depth: 0\n")
	sb.WriteString("          persist-credentials: true\n")
	sb.WriteString("          token: " + g.getStateTokenRef() + "\n")
	sb.WriteString("\n")
}

// writeOwnRepoReconcileStep runs `cascade reconcile --own-repo`, which adopts the
// moved pin into internal/generate/action_pins.yaml and regenerates the
// workflows. It diffs both the workflow and composite-action trees because
// cascade governs pins in both.
func (g *ReconcileGenerator) writeOwnRepoReconcileStep(sb *strings.Builder) {
	sb.WriteString("      - name: Reconcile the pin manifest and regenerate\n")
	sb.WriteString("        if: steps.resolve.outputs.proceed == 'true'\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          BASE_SHA: ${{ steps.resolve.outputs.base_sha }}\n")
	sb.WriteString("          HEAD_SHA: ${{ steps.resolve.outputs.head_sha }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          set -euo pipefail\n")
	sb.WriteString("          git config user.name \"cascade-bot\"\n")
	sb.WriteString("          git config user.email \"cascade-bot@users.noreply.github.com\"\n")
	sb.WriteString("          # List the changed governed source files a pin bump would land in.\n")
	sb.WriteString("          # cascade's own governed sources are the hand-written workflows AND\n")
	sb.WriteString("          # the composite actions, so diff over both. The SHAs come from trusted\n")
	sb.WriteString("          # workflow_run metadata but are still routed through env and quoted.\n")
	sb.WriteString("          git diff --name-only \"$BASE_SHA\" \"$HEAD_SHA\" \\\n")
	sb.WriteString("            -- .github/workflows/ .github/actions/ > changed-files.txt\n")
	sb.WriteString("          args=()\n")
	sb.WriteString("          while IFS= read -r f; do\n")
	sb.WriteString("            [ -n \"$f\" ] && args+=(--changed-file \"$f\")\n")
	sb.WriteString("          done < changed-files.txt\n")
	sb.WriteString("          # Own-repo mode writes the adopted ref into internal/generate/action_pins.yaml\n")
	sb.WriteString("          # and regenerates every generated workflow so they agree again.\n")
	sb.WriteString("          # --action-pins names the manifest on disk; it has no default, so it\n")
	sb.WriteString("          # must be passed explicitly here.\n")
	sb.WriteString("          cascade reconcile --own-repo --action-pins internal/generate/action_pins.yaml \"${args[@]}\"\n")
	sb.WriteString("\n")
}

// writeOwnRepoCommitStep stages an explicit manifest-first allowlist (never
// git add -A), commits DCO-only, and pushes onto the same-repo head branch under
// the push-only-if-changed and non-fast-forward loop guards.
func (g *ReconcileGenerator) writeOwnRepoCommitStep(sb *strings.Builder) {
	sb.WriteString("      - name: Commit and push the reconciled pins\n")
	sb.WriteString("        if: steps.resolve.outputs.proceed == 'true'\n")
	sb.WriteString("        env:\n")
	sb.WriteString("          HEAD_REF: ${{ steps.resolve.outputs.head_ref }}\n")
	sb.WriteString("        run: |\n")
	sb.WriteString("          set -euo pipefail\n")
	sb.WriteString("\n")
	sb.WriteString("          # Stage an explicit manifest-first pathspec allowlist, never git add -A,\n")
	sb.WriteString("          # so only the pin manifest and the regenerated workflows can ride the\n")
	sb.WriteString("          # commit. The workflows glob is included because cascade's own repo\n")
	sb.WriteString("          # commits its regenerated workflows.\n")
	sb.WriteString("          git add internal/generate/action_pins.yaml '.github/workflows/*.yaml'\n")
	sb.WriteString("\n")
	sb.WriteString("          # Guard (b): push only when the reconcile actually changed tracked files.\n")
	sb.WriteString("          if git diff --cached --quiet; then\n")
	sb.WriteString("            echo \"Reconcile produced no change; the branch already agrees with the manifest.\"\n")
	sb.WriteString("            exit 0\n")
	sb.WriteString("          fi\n")
	sb.WriteString("\n")
	sb.WriteString("          git commit -s -m \"" + ownRepoReconcileCommitSubject + "\"\n")
	sb.WriteString("\n")
	sb.WriteString("          # Guard (c): reconcile against the fresh remote tip and refuse a\n")
	sb.WriteString("          # non-fast-forward. If the branch advanced while we worked, abort\n")
	sb.WriteString("          # rather than overwrite the newer commit; the plain (non-force) push\n")
	sb.WriteString("          # enforces the same fast-forward rule as a backstop.\n")
	sb.WriteString("          git fetch origin \"$HEAD_REF\"\n")
	sb.WriteString("          if ! git merge-base --is-ancestor \"origin/${HEAD_REF}\" HEAD; then\n")
	sb.WriteString("            echo \"::warning::${HEAD_REF} advanced during reconcile; aborting the push (non-fast-forward).\"\n")
	sb.WriteString("            exit 0\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          git push origin \"HEAD:${HEAD_REF}\"\n")
}

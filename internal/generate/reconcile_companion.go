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

package generate

import (
	"fmt"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// MergeQueueGenerator emits the opt-in merge-queue validation lane. When
// config.merge_queue.enabled is set, cascade generates a merge_group-triggered
// workflow that validates the prospective trunk commit with cascade's own
// logic: it runs `cascade lint` as a validity gate and a dry-run
// `cascade orchestrate setup` to preview the build/deploy decisions against the
// merge-group candidate ref. The lane is read-only (no state writes, no
// releases, no deploys) and reports a status the merge queue can require.
//
// This generator owns the LANE behavior and is the only supported way to attach a
// merge_group trigger. Attaching the raw merge_group event through
// extra_triggers.merge_group is rejected at validation, because it would fire the
// side-effecting orchestrate workflow on a speculative merge-queue build with no
// gh-readonly-queue guard; merge_queue.enabled emits this read-only lane instead.
type MergeQueueGenerator struct {
	config  *config.TrunkConfig
	baseDir string
}

// NewMergeQueueGenerator creates a merge-queue validation-lane generator.
func NewMergeQueueGenerator(cfg *config.TrunkConfig, baseDir string) *MergeQueueGenerator {
	return &MergeQueueGenerator{
		config:  cfg,
		baseDir: baseDir,
	}
}

// Enabled reports whether the manifest opts in to the merge-queue lane.
func (g *MergeQueueGenerator) Enabled() bool {
	return g.config != nil && g.config.MergeQueue != nil && g.config.MergeQueue.Enabled
}

// getCLIRef mirrors the ref-resolution used by the other generators so the
// emitted setup-cli ref tracks config.cli_version. The default (cli_version
// unset or "latest") resolves to config.DefaultCLIVersion, an immutable release
// tag; "beta" is the explicit opt-in escape hatch to the "master" branch.
func (g *MergeQueueGenerator) getCLIRef() string {
	return cliSetupRef(g.config)
}

// getManifestFilePath returns the repo-relative manifest path for use in the
// generated workflow, matching the release generator's resolution.
func (g *MergeQueueGenerator) getManifestFilePath() string {
	return relativeManifestPath(g.config, g.baseDir)
}

// Generate renders the merge-queue validation-lane workflow.
func (g *MergeQueueGenerator) Generate() (string, error) {
	var sb strings.Builder

	g.writeHeader(&sb)
	g.writeTriggers(&sb)
	g.writePermissions(&sb)
	g.writeJob(&sb)

	return sb.String(), nil
}

func (g *MergeQueueGenerator) writeHeader(sb *strings.Builder) {
	sb.WriteString(GeneratedFileMarker + "\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n", g.getManifestFilePath())
	sb.WriteString("#\n")
	sb.WriteString("# Merge-queue validation lane (opt-in via merge_queue.enabled).\n")
	sb.WriteString("# Runs on merge_group against the would-be-trunk commit and validates it\n")
	sb.WriteString("# with cascade's own logic: a lint validity gate plus a dry-run\n")
	sb.WriteString("# orchestrate setup that previews the build/deploy decisions. The lane is\n")
	sb.WriteString("# read-only (no state writes, releases, or deploys) and reports a status\n")
	sb.WriteString("# the merge queue can require.\n")
	sb.WriteString("\n")
}

func (g *MergeQueueGenerator) writeTriggers(sb *strings.Builder) {
	sb.WriteString("name: Merge Queue Validation\n\n")
	sb.WriteString("on:\n")
	sb.WriteString("  merge_group:\n")
	sb.WriteString("\n")
}

// writePermissions requests contents: read only. The lane reads the merge-group
// candidate and previews orchestration; it performs no writes.
func (g *MergeQueueGenerator) writePermissions(sb *strings.Builder) {
	sb.WriteString("permissions:\n")
	sb.WriteString("  contents: read\n")
	sb.WriteString("\n")
}

func (g *MergeQueueGenerator) writeJob(sb *strings.Builder) {
	sb.WriteString("jobs:\n")
	sb.WriteString("  merge-queue-validate:\n")
	sb.WriteString("    name: Merge Queue Validation\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    steps:\n")
	writeActionStep(sb, g.config, "      ", actionCheckout)
	sb.WriteString("        with:\n")
	sb.WriteString("          fetch-depth: 0\n")

	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())
	// github.token is the built-in Actions token, sufficient to authenticate
	// gh release download against the public stablekernel/cascade repository.
	sb.WriteString("          token: ${{ github.token }}\n")

	// Validity gate: lint --json reports validity in its JSON output, so gate
	// on the parsed result.
	sb.WriteString("      - name: Validate Manifest\n")
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	sb.WriteString("          RESULT=$(cascade lint --json --config \"$MANIFEST_FILE\")\n")
	sb.WriteString("          echo \"$RESULT\"\n")
	sb.WriteString("          VALID=$(echo \"$RESULT\" | jq -r '.valid // false')\n")
	sb.WriteString("          if [[ \"$VALID\" != \"true\" ]]; then\n")
	sb.WriteString("            echo \"$RESULT\" | jq -r '.errors[]? | \"::error::\" + .'\n")
	sb.WriteString("            echo \"::error::Manifest validation failed\"\n")
	sb.WriteString("            exit 1\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          echo \"::notice::Manifest is valid\"\n")

	// Dry-run preview: orchestrate setup with the root --dry-run flag computes
	// what would build/deploy for the merge-group candidate without writing any
	// state. This exercises cascade's change-detection and version logic against
	// the prospective trunk commit.
	//
	// When the manifest declares environments the step targets the first (lowest)
	// environment with --environment, mirroring how the orchestrate workflow
	// defaults its setup environment. orchestrate setup resolves the version
	// against that environment, so an empty value would fail with `environment ""
	// not found`. A manifest with no environments omits the flag and runs the
	// no-environment version calculation. The preview is read-only regardless of
	// which environment it reports on.
	sb.WriteString("      - name: Preview Orchestration (dry-run)\n")
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	sb.WriteString("          cascade --dry-run orchestrate setup \\\n")
	if len(g.config.Environments) > 0 {
		sb.WriteString("            --config \"$MANIFEST_FILE\" \\\n")
		fmt.Fprintf(sb, "            --environment %s\n", g.config.Environments[0].Name)
	} else {
		sb.WriteString("            --config \"$MANIFEST_FILE\"\n")
	}
}

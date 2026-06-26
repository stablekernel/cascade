package generate

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// ValidateCheckGenerator emits the opt-in manifest-validation PR check. When
// config.validate_check.enabled is set, cascade generates a lightweight
// pull_request workflow that runs `cascade parse-config` against the manifest
// and fails when the manifest is invalid, so a malformed configuration cannot
// merge to trunk. The check validates cascade's own configuration only: it does
// not run the consumer's build/test CI, requests contents: read alone, and has
// no dry-run or comment side effects.
type ValidateCheckGenerator struct {
	config  *config.TrunkConfig
	baseDir string
}

// NewValidateCheckGenerator creates a manifest-validation PR-check generator.
func NewValidateCheckGenerator(cfg *config.TrunkConfig, baseDir string) *ValidateCheckGenerator {
	return &ValidateCheckGenerator{
		config:  cfg,
		baseDir: baseDir,
	}
}

// Enabled reports whether the manifest opts in to the validation check.
func (g *ValidateCheckGenerator) Enabled() bool {
	return g.config != nil && g.config.ValidateCheck != nil && g.config.ValidateCheck.Enabled
}

// getCLIRef mirrors the ref-resolution used by the other generators so the
// emitted setup-cli ref tracks config.cli_version. The default (cli_version
// unset or "latest") resolves to config.DefaultCLIVersion, an immutable release
// tag; "beta" is the explicit opt-in escape hatch to the "master" branch.
func (g *ValidateCheckGenerator) getCLIRef() string {
	return cliSetupRef(g.config)
}

// getManifestFilePath returns the repo-relative manifest path for use in the
// generated workflow, matching the release generator's resolution.
func (g *ValidateCheckGenerator) getManifestFilePath() string {
	manifestPath := g.config.GetManifestFile()
	if !filepath.IsAbs(manifestPath) {
		return manifestPath
	}
	if g.baseDir != "" {
		if rel, err := filepath.Rel(g.baseDir, manifestPath); err == nil {
			return rel
		}
	}
	return ".github/manifest.yaml"
}

// Generate renders the manifest-validation PR-check workflow.
func (g *ValidateCheckGenerator) Generate() (string, error) {
	var sb strings.Builder

	g.writeHeader(&sb)
	g.writeTriggers(&sb)
	g.writePermissions(&sb)
	g.writeJob(&sb)

	return sb.String(), nil
}

func (g *ValidateCheckGenerator) writeHeader(sb *strings.Builder) {
	sb.WriteString(GeneratedFileMarker + "\n")
	fmt.Fprintf(sb, "# Regenerate with: cascade generate-workflow --config %s\n", g.getManifestFilePath())
	sb.WriteString("#\n")
	sb.WriteString("# Manifest-validation PR check (opt-in via validate_check.enabled).\n")
	sb.WriteString("# Runs `cascade parse-config` against the manifest on pull_request and\n")
	sb.WriteString("# fails when the configuration is invalid, so a malformed manifest cannot\n")
	sb.WriteString("# merge. Validates cascade's own configuration only; it does not run the\n")
	sb.WriteString("# repository's build or test suites.\n")
	sb.WriteString("\n")
}

func (g *ValidateCheckGenerator) writeTriggers(sb *strings.Builder) {
	sb.WriteString("name: Validate Manifest\n\n")
	sb.WriteString("on:\n")
	sb.WriteString("  pull_request:\n")
	sb.WriteString("    paths:\n")
	fmt.Fprintf(sb, "      - %s\n", g.getManifestFilePath())
	sb.WriteString("\n")
}

// writePermissions requests contents: read only. The check reads the manifest
// from the checked-out PR head and reports its status; it never writes.
func (g *ValidateCheckGenerator) writePermissions(sb *strings.Builder) {
	sb.WriteString("permissions:\n")
	sb.WriteString("  contents: read\n")
	sb.WriteString("\n")
}

func (g *ValidateCheckGenerator) writeJob(sb *strings.Builder) {
	sb.WriteString("jobs:\n")
	sb.WriteString("  validate-manifest:\n")
	sb.WriteString("    name: Validate Manifest\n")
	sb.WriteString("    runs-on: ubuntu-latest\n")
	sb.WriteString("    steps:\n")
	writeActionStep(sb, g.config, "      ", actionCheckout)

	sb.WriteString("      - name: Setup CLI\n")
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", g.getCLIRef())
	sb.WriteString("        with:\n")
	fmt.Fprintf(sb, "          version: %s\n", g.config.GetCLIVersion())
	// github.token is the built-in Actions token, sufficient to authenticate
	// gh release download against the public stablekernel/cascade repository.
	sb.WriteString("          token: ${{ github.token }}\n")

	sb.WriteString("      - name: Validate Manifest\n")
	sb.WriteString("        run: |\n")
	fmt.Fprintf(sb, "          MANIFEST_FILE=\"%s\"\n", g.getManifestFilePath())
	sb.WriteString("          if [[ ! -f \"$MANIFEST_FILE\" ]]; then\n")
	sb.WriteString("            echo \"::error::$MANIFEST_FILE not found\"\n")
	sb.WriteString("            exit 1\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          # parse-config reports validity in its JSON output (valid: false on\n")
	sb.WriteString("          # parse or schema errors) rather than via exit code, so gate on the\n")
	sb.WriteString("          # parsed result.\n")
	sb.WriteString("          RESULT=$(cascade parse-config --config \"$MANIFEST_FILE\")\n")
	sb.WriteString("          echo \"$RESULT\"\n")
	sb.WriteString("          VALID=$(echo \"$RESULT\" | jq -r '.valid // false')\n")
	sb.WriteString("          if [[ \"$VALID\" != \"true\" ]]; then\n")
	sb.WriteString("            echo \"$RESULT\" | jq -r '.errors[]? | \"::error::\" + .'\n")
	sb.WriteString("            echo \"::error::Manifest validation failed\"\n")
	sb.WriteString("            exit 1\n")
	sb.WriteString("          fi\n")
	sb.WriteString("          echo \"::notice::Manifest is valid\"\n")
}

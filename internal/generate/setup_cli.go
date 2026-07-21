package generate

import (
	_ "embed"
	"fmt"
	"strings"
)

// setup_cli_install.sh is a byte-identical copy of the composite action's
// install script (.github/actions/setup-cli/install.sh). go:embed cannot reach
// a path outside the package directory (no ".." references), so the canonical
// script is copied here and TestSetupCLIInstallScript_MatchesActionCopy guards
// the two against drift. Binary mode writes these exact bytes to disk and runs
// them, so both install modes share one verify contract.
//
//go:embed setup_cli_install.sh
var setupCLIInstallScript string

// setup_cli_binary_preamble.sh resolves the version, detects OS/arch, and
// installs a checksum-verified cosign, then hands off to the embedded
// install.sh. Binary mode emits it inline so no third-party action is required.
//
//go:embed setup_cli_binary_preamble.sh
var setupCLIBinaryPreamble string

// cliInstallMode selects how a generator emits the "Setup CLI" step. The zero
// value is the default action mode, so a generator that never has a mode set
// emits byte-identical output to before this option existed.
type cliInstallMode int

const (
	// cliInstallModeAction installs the CLI through the setup-cli composite
	// action (uses: stablekernel/cascade/.github/actions/setup-cli@<ref>). This
	// is the default and the zero value.
	cliInstallModeAction cliInstallMode = iota
	// cliInstallModeBinary installs the CLI with a self-contained run: step that
	// uses no third-party action: it resolves the version, detects OS/arch,
	// installs a checksum-verified cosign by direct download, and runs the same
	// mandatory sha256 gate and keyless cosign verification the action's
	// install.sh performs. It lets a repo adopt cascade without an organization
	// Actions allowlist entry for a third-party action.
	cliInstallModeBinary
)

// String renders the mode as its flag spelling, so error messages and help echo
// the value a user would pass.
func (m cliInstallMode) String() string {
	if m == cliInstallModeBinary {
		return "binary"
	}
	return "action"
}

// parseCLIInstallMode maps a --cli-install flag value to a cliInstallMode. Only
// "action" (default) and "binary" are valid; anything else is rejected loudly so
// a typo does not silently fall back to a mode the user did not ask for.
func parseCLIInstallMode(s string) (cliInstallMode, error) {
	switch s {
	case "", "action":
		return cliInstallModeAction, nil
	case "binary":
		return cliInstallModeBinary, nil
	default:
		return cliInstallModeAction, fmt.Errorf("invalid --cli-install %q: expected \"action\" or \"binary\"", s)
	}
}

// installModeHolder carries the CLI install mode a generator threads into its
// setup-cli emission. Embedding it gives every generator the field and its
// setter without duplicating either across the dozen generator types.
type installModeHolder struct {
	installMode cliInstallMode
}

// setInstallMode records the CLI install mode for a generator. The generate
// command sets it from the --cli-install flag; unset leaves the default action
// mode, which keeps output byte-identical.
func (h *installModeHolder) setInstallMode(mode cliInstallMode) { h.installMode = mode }

// setupCLIStep describes a single emission of the "Setup CLI" step that installs
// the cascade binary. Every generator routes its setup-cli emission through
// writeSetupCLIStep so the install logic has exactly one source.
type setupCLIStep struct {
	// ref is the git ref spliced after "setup-cli@" in action mode. Callers pass
	// the result of their own getCLIRef helper so the ref-resolution logic stays
	// unchanged. It is unused in binary mode.
	ref string
	// version is the value of the action's "version" input (action mode) or the
	// CASCADE_CLI_VERSION the binary-mode preamble resolves. Typically the result
	// of TrunkConfig.GetCLIVersion.
	version string
	// token is the value of the action's "token" input (action mode) or the
	// GH_TOKEN the binary-mode steps use for gh (binary mode).
	token string
	// tokenBeforeVersion emits the token input above the version input in action
	// mode, matching the release-path ordering. When false, version is emitted
	// first. It has no effect in binary mode.
	tokenBeforeVersion bool
	// ifExpr, when non-empty, adds a step-level "if:" guard above the step.
	ifExpr string
	// installMode selects action (default) or binary emission.
	installMode cliInstallMode
}

// writeSetupCLIStep emits the "Setup CLI" step that installs the cascade binary.
// It is the single canonical emitter, so any change to how the CLI is installed
// has one place to branch. In the default action mode it emits the composite
// action reference; in binary mode it emits a self-contained, third-party-free
// install that preserves the same sha256 and cosign-keyless guarantees.
func writeSetupCLIStep(sb *strings.Builder, step setupCLIStep) {
	if step.installMode == cliInstallModeBinary {
		writeSetupCLIStepBinary(sb, step)
		return
	}
	sb.WriteString("      - name: Setup CLI\n")
	if step.ifExpr != "" {
		fmt.Fprintf(sb, "        if: %s\n", step.ifExpr)
	}
	fmt.Fprintf(sb, "        uses: stablekernel/cascade/.github/actions/setup-cli@%s\n", step.ref)
	sb.WriteString("        with:\n")
	if step.tokenBeforeVersion {
		fmt.Fprintf(sb, "          token: %s\n", step.token)
		fmt.Fprintf(sb, "          version: %s\n", step.version)
		return
	}
	fmt.Fprintf(sb, "          version: %s\n", step.version)
	fmt.Fprintf(sb, "          token: %s\n", step.token)
}

// binaryStepIndent is the indentation of the run: block body for a job step: six
// spaces to the "- name:", eight to its keys, ten to the script lines. YAML
// strips this common indent from a literal block scalar, so the shell receives
// the embedded scripts exactly as authored.
const binaryStepIndent = "          "

// writeSetupCLIStepBinary emits the self-contained, third-party-free install.
// The caller's token flows through GH_TOKEN and the version through
// CASCADE_CLI_VERSION (both via env, never spliced into the run: script, so no
// ${{ }} appears inside the script and real GitHub does not reject it at parse).
// The step writes the embedded install.sh to a temp file and runs the preamble,
// which resolves the tag, detects OS/arch, installs a verified cosign, and execs
// install.sh for the mandatory sha256 gate and keyless cosign verification.
func writeSetupCLIStepBinary(sb *strings.Builder, step setupCLIStep) {
	sb.WriteString("      - name: Setup CLI\n")
	if step.ifExpr != "" {
		fmt.Fprintf(sb, "        if: %s\n", step.ifExpr)
	}
	sb.WriteString("        env:\n")
	fmt.Fprintf(sb, "          GH_TOKEN: %s\n", step.token)
	fmt.Fprintf(sb, "          CASCADE_CLI_VERSION: %s\n", step.version)
	sb.WriteString("        run: |\n")
	sb.WriteString(binaryStepIndent + "set -euo pipefail\n")
	sb.WriteString(binaryStepIndent + "CASCADE_WORK=\"$(mktemp -d)\"\n")
	// Write install.sh verbatim via a quoted heredoc so nothing in it is expanded
	// or re-interpreted; the script carries no ${{ }} and no line equal to the
	// terminator, so it round-trips byte-for-byte.
	sb.WriteString(binaryStepIndent + "cat > \"$CASCADE_WORK/install.sh\" <<'CASCADE_INSTALL_SH_EOF'\n")
	writeIndentedScriptLines(sb, binaryStepIndent, setupCLIInstallScript)
	sb.WriteString(binaryStepIndent + "CASCADE_INSTALL_SH_EOF\n")
	sb.WriteString(binaryStepIndent + "export CASCADE_INSTALL_SCRIPT=\"$CASCADE_WORK/install.sh\"\n")
	writeIndentedScriptLines(sb, binaryStepIndent, stripShebang(setupCLIBinaryPreamble))
}

// writeIndentedScriptLines emits script into a YAML literal block: every
// non-empty line is prefixed with indent (so it stays inside the block scalar),
// and empty lines are emitted bare so no trailing whitespace lands in the file.
// A single trailing newline on script is dropped to avoid an extra blank line.
func writeIndentedScriptLines(sb *strings.Builder, indent, script string) {
	for _, line := range strings.Split(strings.TrimRight(script, "\n"), "\n") {
		if strings.TrimRight(line, " \t") == "" {
			sb.WriteByte('\n')
			continue
		}
		sb.WriteString(indent)
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
}

// stripShebang drops a leading "#!" line so the preamble reads as a plain
// script fragment when inlined into a run: block (where a shebang would only be
// a comment). The standalone file keeps its shebang for shellcheck.
func stripShebang(s string) string {
	if strings.HasPrefix(s, "#!") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return s[i+1:]
		}
	}
	return s
}

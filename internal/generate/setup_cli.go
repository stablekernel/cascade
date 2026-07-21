package generate

import (
	"fmt"
	"strings"
)

// setupCLIStep describes a single emission of the "Setup CLI" step that installs
// the cascade binary through the setup-cli composite action. Every generator
// routes its setup-cli emission through writeSetupCLIStep so the action
// reference and its inputs have exactly one source.
type setupCLIStep struct {
	// ref is the git ref spliced after "setup-cli@". Callers pass the result of
	// their own getCLIRef helper so the ref-resolution logic stays unchanged.
	ref string
	// version is the value of the action's "version" input, typically the result
	// of TrunkConfig.GetCLIVersion.
	version string
	// token is the value of the action's "token" input.
	token string
	// tokenBeforeVersion emits the token input above the version input, matching
	// the release-path ordering. When false, version is emitted first.
	tokenBeforeVersion bool
	// ifExpr, when non-empty, adds a step-level "if:" guard above the uses line.
	ifExpr string
}

// writeSetupCLIStep emits the "Setup CLI" step that installs the cascade binary
// via the setup-cli composite action. It is the single canonical emitter for the
// action reference (`uses: ...setup-cli@<ref>`) and the `version` input, so any
// future change to how the CLI is installed has one place to branch.
func writeSetupCLIStep(sb *strings.Builder, step setupCLIStep) {
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

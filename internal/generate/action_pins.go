package generate

import (
	"fmt"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// Third-party action names cascade emits into generated workflows. These are
// the marketplace actions referenced by uses: lines that the pin policy
// governs. cascade's own stablekernel/cascade/... action refs are NOT listed
// here: they are versioned by cascade's release flow via config.cli_version
// (see getCLIRef / getActionPath) and are intentionally excluded from SHA
// pinning.
const (
	actionCheckout         = "actions/checkout"
	actionGithubScript     = "actions/github-script"
	actionDownloadArtifact = "actions/download-artifact"
	actionUploadArtifact   = "actions/upload-artifact"
	actionCreateAppToken   = "actions/create-github-app-token"
)

// actionPin records the default mutable tag cascade emits in tag mode and the
// resolved commit SHA (with the human-readable version it corresponds to) that
// cascade emits in sha mode.
type actionPin struct {
	tag        string // mutable major tag emitted in tag mode, e.g. "v4"
	sha        string // 40-char commit SHA emitted in sha mode
	shaVersion string // precise version the SHA corresponds to, for the trailing comment
}

// defaultActionPins is the built-in pin table for every third-party action
// cascade emits. tag mode emits <action>@<tag>; sha mode emits
// <action>@<sha> # <shaVersion>. The SHAs were resolved from the upstream
// repositories at the major tags below; action_pins entries in the manifest
// override any of these without code changes.
var defaultActionPins = map[string]actionPin{
	actionCheckout:         {tag: "v6", sha: "df4cb1c069e1874edd31b4311f1884172cec0e10", shaVersion: "v6.0.3"},
	actionGithubScript:     {tag: "v7", sha: "f28e40c7f34bde8b3046d885e986cb6290c5673b", shaVersion: "v7.1.0"},
	actionDownloadArtifact: {tag: "v4", sha: "d3f86a106a0bac45b974a628896c90dbdf5c8093", shaVersion: "v4.3.0"},
	actionUploadArtifact:   {tag: "v4", sha: "ea165f8d65b6e75b540449e92b4886f43607fa02", shaVersion: "v4.6.2"},
	actionCreateAppToken:   {tag: "v3", sha: "bcd2ba49218906704ab6c1aa796996da409d3eb1", shaVersion: "v3.2.0"},
}

// actionRef returns the fully-rendered uses: value for a third-party action
// (the portion after "uses: "), honoring the manifest pin policy. It is the
// single seam every third-party uses: emission across orchestrate, promote,
// release, and external generation routes through, so the policy is applied
// uniformly and no ref is missed.
//
// Resolution order:
//  1. config.action_pins[action]: explicit per-action override (any ref/sha),
//     applied regardless of pin_mode. Use this for forks or org-mirrored actions.
//  2. pin_mode: sha: emit <action>@<sha> # <version> from the built-in table.
//  3. pin_mode: tag (default): emit <action>@<tag>, today's behavior, never
//     @latest for a third-party action.
//
// An action not present in the built-in table and not overridden falls back to
// the literal action string unchanged, so callers can pass an already-qualified
// ref safely.
func actionRef(cfg *config.TrunkConfig, action string) string {
	if cfg != nil {
		if override, ok := cfg.ActionPins[action]; ok && override != "" {
			return action + "@" + override
		}
	}

	pin, known := defaultActionPins[action]
	if !known {
		return action
	}

	if cfg.GetPinMode() == config.PinModeSHA {
		ref := action + "@" + pin.sha
		if pin.shaVersion != "" {
			ref += " # " + pin.shaVersion
		}
		return ref
	}

	return action + "@" + pin.tag
}

// cliSetupRef returns the ref portion emitted after "setup-cli@" for cascade's
// own self-action. It returns a 40-hex commit SHA with a trailing "# <version>"
// comment when pin_mode is sha and cli_version_sha is set, otherwise the
// cli_version tag (today's behavior). "beta" always opts into the master trunk.
//
// Unlike third-party actions (resolved by actionRef against a static pin table),
// the self-action SHA tracks cli_version, which moves every release, so it is
// sourced from the manifest (written by the bump automation) rather than a baked
// constant. Generation stays a pure offline function of the committed manifest.
//
// Precedence: beta (master) > (pin_mode sha AND cli_version_sha set) > tag. An
// empty cli_version_sha under pin_mode sha degrades gracefully to the tag,
// never a broken ref.
func cliSetupRef(cfg *config.TrunkConfig) string {
	if cfg.CLIVersion == "beta" {
		return "master" // Explicit opt-in escape hatch to trunk.
	}
	version := cfg.GetCLIVersion()
	if cfg.GetPinMode() == config.PinModeSHA && cfg.CLIVersionSHA != "" {
		return cfg.CLIVersionSHA + " # " + version
	}
	return version
}

// writeActionStep writes a "<indent>- uses: <ref>\n" line for a third-party
// action, routing the ref through actionRef so the pin policy is applied. Pass
// the leading indentation (spaces before "- ").
func writeActionStep(sb *strings.Builder, cfg *config.TrunkConfig, indent, action string) {
	fmt.Fprintf(sb, "%s- uses: %s\n", indent, actionRef(cfg, action))
}

// writeActionUses writes a "<indent>uses: <ref>\n" line for a third-party
// action used as a continuation of an already-emitted step (e.g. a step that
// has a name: on the preceding line). Pass the leading indentation before
// "uses:".
func writeActionUses(sb *strings.Builder, cfg *config.TrunkConfig, indent, action string) {
	fmt.Fprintf(sb, "%suses: %s\n", indent, actionRef(cfg, action))
}

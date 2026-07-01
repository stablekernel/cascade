package generate

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

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

// actionPinsManifest is the on-disk shape of action_pins.yaml: every action
// cascade pins, keyed by action path, with its tag, resolved commit SHA,
// precise version, and whether the generator emits it.
type actionPinsManifest struct {
	Actions map[string]actionPinEntry `yaml:"actions"`
}

// actionPinEntry is a single action's pin record as authored in the manifest.
// emit distinguishes the actions the generator renders into user workflows from
// the maintainer-only actions recorded only to keep the pin set in one place.
type actionPinEntry struct {
	Tag     string `yaml:"tag"`
	SHA     string `yaml:"sha"`
	Version string `yaml:"version"`
	Emit    bool   `yaml:"emit"`
}

// actionPinsYAML is the committed single source of truth for every third-party
// action version cascade pins. It is parsed once at package init; generation
// stays a pure offline function of these committed bytes.
//
//go:embed action_pins.yaml
var actionPinsYAML []byte

// commitSHAPattern matches a 40-character lowercase hex commit SHA. A manifest
// entry whose sha does not match is rejected at init so a malformed pin can
// never reach generation.
var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// defaultActionPins is the built-in pin table for every third-party action the
// generator emits. tag mode emits <action>@<tag>; sha mode emits
// <action>@<sha> # <shaVersion>. It is parsed from the embedded action_pins.yaml
// manifest (emit: true entries only) so the values live in one committed file;
// action_pins entries in a user manifest still override any of these without
// code changes.
var defaultActionPins = mustParseActionPins(actionPinsYAML)

// mustParseActionPins parses the embedded manifest into the generator pin table,
// keeping only emit: true actions (the set the generator renders). It panics on
// a malformed manifest, a non-40-hex SHA, or a missing generator action so the
// failure surfaces at init rather than as silently wrong generated output.
func mustParseActionPins(data []byte) map[string]actionPin {
	var manifest actionPinsManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		panic(fmt.Sprintf("generate: parsing action_pins.yaml: %v", err))
	}

	pins := make(map[string]actionPin, len(manifest.Actions))
	for name, entry := range manifest.Actions {
		if !entry.Emit {
			continue
		}
		if !commitSHAPattern.MatchString(entry.SHA) {
			panic(fmt.Sprintf("generate: action_pins.yaml: %s sha %q is not a 40-char commit SHA", name, entry.SHA))
		}
		if entry.Tag == "" || entry.Version == "" {
			panic(fmt.Sprintf("generate: action_pins.yaml: %s is missing a tag or version", name))
		}
		pins[name] = actionPin{tag: entry.Tag, sha: entry.SHA, shaVersion: entry.Version}
	}

	// Every action the generator references by const must be present and emit:true,
	// so a manifest edit can never drop a governed action without a build-time panic.
	for _, name := range []string{
		actionCheckout, actionGithubScript, actionDownloadArtifact,
		actionUploadArtifact, actionCreateAppToken,
	} {
		if _, ok := pins[name]; !ok {
			panic(fmt.Sprintf("generate: action_pins.yaml is missing emit:true entry for %s", name))
		}
	}

	return pins
}

// actionRef returns the fully-rendered uses: value for a third-party action
// (the portion after "uses: "), honoring the manifest pin policy. It is the
// single seam every third-party uses: emission across orchestrate, promote,
// release, and external generation routes through, so the policy is applied
// uniformly and no ref is missed.
//
// Resolution order:
//  1. config.action_pins[action]: explicit per-action ref override (a tag or
//     sha), applied regardless of pin_mode. The override is the bare ref after
//     "@" for the same action path; it cannot repoint to a different owner/repo.
//     Use it to hold an action at a known-good commit or tag.
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

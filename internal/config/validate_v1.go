package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Structural validation for the v1 reserved-shape fields. These rules are the
// ones the frozen v1 contract mandates *now* (so a manifest using a reserved
// field can never encode something that would require a breaking change to wire
// up later). Generator/emit behavior is NOT implemented here.

// knownPermissionScopes are the GHA GITHUB_TOKEN permission scopes.
var knownPermissionScopes = map[string]bool{
	"actions":             true,
	"attestations":        true,
	"checks":              true,
	"contents":            true,
	"deployments":         true,
	"discussions":         true,
	"id-token":            true,
	"issues":              true,
	"packages":            true,
	"pages":               true,
	"pull-requests":       true,
	"repository-projects": true,
	"security-events":     true,
	"statuses":            true,
}

// validPermissionValues are the GHA permission levels.
var validPermissionValues = map[string]bool{"read": true, "write": true, "none": true}

// reservedDispatchInputNames are generator-owned dispatch inputs that a user
// dispatch_inputs map may not shadow.
var reservedDispatchInputNames = map[string]bool{"environment": true, "dry_run": true}

// validRolloutTypes enumerates the accepted rollout.type values.
var validRolloutTypes = map[string]bool{
	RolloutTypeDefault:   true,
	RolloutTypeRolling:   true,
	RolloutTypeCanary:    true,
	RolloutTypeBlueGreen: true,
}

// jobIDSafeNameRe matches a name that is safe to use as a component of a GitHub
// Actions job ID. cascade derives job IDs as build-<name>/deploy-<name> and
// keys several identifiers and expression references off environment names. A
// GitHub job ID must start with a letter or _ and contain only [A-Za-z0-9_-].
// Because the name is a suffix after a build-/deploy- prefix, a leading digit,
// uppercase letters, and hyphens are all acceptable in the name itself; only
// characters outside [A-Za-z0-9_-] (such as ".", spaces, and "/") break the job
// ID and the ${{ }} dereferences that read its outputs.
var jobIDSafeNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// commitSHARe matches a full 40-character lowercase-hex Git commit SHA.
var commitSHARe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// actionFolderRe matches a safe action_folder name: a plain path component
// with no directory separators or traversal segments, since the generator
// joins it directly into a filesystem path under .github/actions/.
var actionFolderRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateActionFolder rejects an action_folder value that is empty, contains
// a path separator, contains a ".." traversal segment, or otherwise falls
// outside a safe plain-name charset. The generator joins the value directly
// into .github/actions/<folder>/action.yaml, so an unsafe value could escape
// the intended actions directory.
func validateActionFolder(folder string) []string {
	if folder == "" {
		return nil
	}
	if strings.Contains(folder, "..") || strings.Contains(folder, "/") || !actionFolderRe.MatchString(folder) {
		return []string{fmt.Sprintf("action_folder %q must be a plain folder name with no path separators or '..' segments", folder)}
	}
	return nil
}

// actionPinValueRe bounds an action_pins override value to a ref plus an
// optional trailing "# <version>" comment. It rejects newlines and any
// character that could break out of the emitted YAML scalar, since actionRef
// splices the value raw into a generated workflow.
var actionPinValueRe = regexp.MustCompile(`^[A-Za-z0-9._+/-]+(?: # [A-Za-z0-9._+-]+)?$`)

// dispatchInputOptionRe matches a choice dispatch-input option that is safe to
// emit verbatim as a YAML block-sequence item under workflow_dispatch's
// inputs.<name>.options. cascade renders each option unquoted, so the accepted
// set is constrained to characters that need no escaping and cannot break the
// surrounding document: an empty option, a space, a colon, or a ${{ }} fragment
// would otherwise produce a workflow that fails actionlint or GitHub's parser.
// Dots are allowed so version-like options (v1.2.3) remain expressible.
var dispatchInputOptionRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// validateJobIDSafeName rejects a name that would produce an invalid GitHub
// Actions job ID or break the expression references derived from it. Names are
// rejected (not sanitized) on purpose: sanitizing distinct names could collapse
// them to a single job ID and silently merge two callbacks.
func validateJobIDSafeName(prefix, name string) []string {
	if name == "" {
		// Empty names are reported separately by the caller (".name is required").
		return nil
	}
	if jobIDSafeNameRe.MatchString(name) {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s %q must contain only letters, digits, hyphens, and underscores", prefix, name)}
}

// validateWorkflowRunXOR enforces that callbacks are reusable-workflow only.
// Inline run: and shell: are no longer supported, so each is rejected with an
// actionable error, and workflow: is required.
func validateWorkflowRunXOR(prefix, workflow, run, shell string) []string {
	var errs []string
	if run != "" {
		errs = append(errs, fmt.Sprintf("%s: inline run: callbacks are no longer supported; provide a reusable workflow via workflow: (see docs/security/hardening or the callback contract)", prefix))
	}
	if shell != "" {
		errs = append(errs, fmt.Sprintf("%s: shell: is no longer supported; inline run callbacks were removed, provide a reusable workflow via workflow:", prefix))
	}
	if workflow == "" {
		errs = append(errs, fmt.Sprintf("%s: workflow is required", prefix))
	}
	return errs
}

// validateExternalDeployWorkflowOnly enforces that external deploys are
// reusable-workflow only. An external deploy resolves to a workflow in the
// external repo, so it must declare workflow: and cannot use run:/shell:.
// Inline run: and shell: are no longer supported anywhere and are rejected here.
func validateExternalDeployWorkflowOnly(prefix, workflow, run, shell string) []string {
	var errs []string
	if run != "" {
		errs = append(errs, fmt.Sprintf("%s: external deploys are reusable-workflow only; run is not supported (set workflow instead)", prefix))
	}
	if shell != "" {
		errs = append(errs, fmt.Sprintf("%s: external deploys are reusable-workflow only; shell is not supported", prefix))
	}
	if workflow == "" {
		errs = append(errs, fmt.Sprintf("%s: workflow is required", prefix))
	}
	return errs
}

// validateOnUpdate enforces the shape of an external deploy's on_update block.
// The block is opt-in: a nil OnUpdate (or a nil Deploy within it) keeps the
// receiver record-only and passes validation. When on_update.deploy is set its
// workflow path is required, consistent with the reusable-workflow-only policy
// enforced on the external deploy itself.
func validateOnUpdate(prefix string, onUpdate *OnUpdateConfig) []string {
	if onUpdate == nil || onUpdate.Deploy == nil {
		return nil
	}
	var errs []string
	if onUpdate.Deploy.Workflow == "" {
		errs = append(errs, fmt.Sprintf("%s: on_update.deploy.workflow is required when on_update.deploy is set", prefix))
	}
	return errs
}

// validateJobControlFields rejects job-control fields that GHA does not accept on
// a reusable-workflow (jobs.<id>.uses) callback. runs_on and concurrency must be
// set inside the callback workflow itself, not on the calling job.
func validateJobControlFields(prefix string, isReusableWorkflow bool, runsOn *RunsOn, concurrency *ConcurrencyConfig) []string {
	if !isReusableWorkflow {
		return nil
	}
	var errs []string
	if runsOn != nil {
		errs = append(errs, fmt.Sprintf(
			"%s: runs_on is not valid on a reusable-workflow callback; set runs-on inside your callback workflow", prefix))
	}
	if concurrency != nil {
		errs = append(errs, fmt.Sprintf(
			"%s: concurrency is not valid on a reusable-workflow callback; set concurrency inside your callback workflow", prefix))
	}
	return errs
}

// validateCallbackTimeout rejects a per-callback timeout_minutes on a
// reusable-workflow callback. GitHub forbids timeout-minutes on a job that calls
// a reusable workflow (a jobs.<id>.uses job may only set uses, with, secrets,
// needs, if, permissions, strategy, name, and concurrency). Every cascade callback
// is a reusable-workflow uses: job, so the timeout must be declared inside the
// called workflow instead.
func validateCallbackTimeout(prefix string, isReusableWorkflow bool, timeoutMinutes int) []string {
	if !isReusableWorkflow || timeoutMinutes <= 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s: timeout_minutes is not valid on a reusable-workflow callback; GitHub forbids timeout-minutes on a job that calls a reusable workflow - set timeout-minutes inside your callback workflow instead", prefix)}
}

// localCallbackPathRe bounds a bare-filename or .github/workflows/... local
// callback value to characters safe to splice raw into a generated
// workflow's uses: line. It rejects newlines, other control characters,
// quotes, whitespace, and any character that could break out of the emitted
// YAML scalar, while allowing the path characters a callback workflow ref
// legitimately needs. The trailing "$" is Go's default (non-multiline)
// anchor, which matches only the true end of the string, so a trailing
// newline is rejected the same as an embedded one.
var localCallbackPathRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// crossRepoCallbackRe bounds a cross-repo "@"-containing callback ref to a
// path, a single "@", and a ref, each drawn from the same safe path charset
// as localCallbackPathRe. This is the positive-charset counterpart to the
// up-front containsUnsafeChar guard below, applied to the one accepted shape
// that carries an "@".
var crossRepoCallbackRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+@[A-Za-z0-9._/-]+$`)

// containsUnsafeChar reports whether s contains a newline, carriage return,
// other control character, or whitespace. Every branch of
// validateLocalCallbackWorkflowPath ultimately splices its value raw into a
// generated workflow's uses: line, so this check is applied once, up front,
// rather than duplicated per branch: it keeps guarding every branch even if
// the function grows a new accepted shape later.
func containsUnsafeChar(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// validateLocalCallbackWorkflowPath checks that a local callback workflow path
// is either a bare filename, a .github/workflows/... path, or a cross-repo
// external ref (containing "@"). Any other form is rejected because GitHub
// requires local reusable workflows to live under .github/workflows/. Every
// accepted form is charset-validated: the value is spliced raw into a
// generated workflow's uses: line, so it must never carry a newline or other
// character that could break out of the emitted YAML scalar.
func validateLocalCallbackWorkflowPath(prefix, workflow string) []string {
	if workflow == "" {
		return nil
	}
	if containsUnsafeChar(workflow) {
		return []string{fmt.Sprintf("%s: local callback workflow %q contains unsafe characters", prefix, workflow)}
	}
	// Cross-repo external refs contain "@" - valid only when the whole value
	// is a path@ref shape drawn from the safe path charset.
	if strings.Contains(workflow, "@") {
		if !crossRepoCallbackRe.MatchString(workflow) {
			return []string{fmt.Sprintf("%s: cross-repo local callback workflow %q must be a path@ref reference", prefix, workflow)}
		}
		return nil
	}
	// Bare filename (no "/") - valid; normalizeWorkflowPath will route it.
	if !strings.Contains(workflow, "/") {
		if !localCallbackPathRe.MatchString(workflow) {
			return []string{fmt.Sprintf("%s: local callback workflow %q contains unsafe characters", prefix, workflow)}
		}
		return nil
	}
	// .github/workflows/... path - valid only when it stays inside that
	// directory and carries no unsafe characters; a ".." traversal segment
	// after the prefix must not escape it.
	if strings.HasPrefix(workflow, ".github/workflows/") || strings.HasPrefix(workflow, "./.github/workflows/") {
		if strings.Contains(workflow, "..") {
			return []string{fmt.Sprintf("%s: local callback workflow must not contain '..' segments, got %q", prefix, workflow)}
		}
		if !localCallbackPathRe.MatchString(workflow) {
			return []string{fmt.Sprintf("%s: local callback workflow %q contains unsafe characters", prefix, workflow)}
		}
		return nil
	}
	return []string{fmt.Sprintf("%s: local callback workflow must be a .github/workflows/... path or a bare filename, got %q", prefix, workflow)}
}

// validatePermissions checks permission scope keys and values.
func validatePermissions(prefix string, perms map[string]string) []string {
	var errs []string
	for _, scope := range sortedKeys(perms) {
		if !knownPermissionScopes[scope] {
			errs = append(errs, fmt.Sprintf("%s.permissions: unknown permission scope %q", prefix, scope))
			continue
		}
		if !validPermissionValues[perms[scope]] {
			errs = append(errs, fmt.Sprintf("%s.permissions.%s must be one of: read, write, none", prefix, scope))
		}
	}
	return errs
}

// validateSecrets checks the secrets union. Form A (inherit) and form B (map)
// are both valid; an empty/unset value is fine.
func validateSecrets(prefix string, s *SecretsConfig) []string {
	if s == nil {
		return nil
	}
	if !s.Inherit && s.Map == nil {
		return []string{fmt.Sprintf("%s.secrets: must be \"inherit\" or a mapping of secret names", prefix)}
	}
	return nil
}

// validateRollout checks rollout.type and the canary/blue_green env gating.
func validateRollout(prefix string, r *RolloutConfig, environments []string) []string {
	if r == nil {
		return nil
	}
	var errs []string
	t := r.GetType()
	if !validRolloutTypes[t] {
		errs = append(errs, fmt.Sprintf("%s.rollout.type must be one of: default, rolling, canary, blue_green", prefix))
	}
	// canary / blue_green require environments.
	if (t == RolloutTypeCanary || t == RolloutTypeBlueGreen) && len(environments) == 0 {
		errs = append(errs, fmt.Sprintf("%s.rollout.type %q requires environments to be configured", prefix, t))
	}
	if r.Canary != nil {
		errs = append(errs, validateCanaryConfig(prefix, r.Canary)...)
	}
	return errs
}

// validateCanaryConfig checks the optional reserved fields on a CanaryConfig.
func validateCanaryConfig(prefix string, c *CanaryConfig) []string {
	var errs []string
	if c.Percent != 0 && (c.Percent < 1 || c.Percent > 100) {
		errs = append(errs, fmt.Sprintf("%s.rollout.canary.percent must be between 1 and 100", prefix))
	}
	if c.BakeTime != "" {
		if _, err := time.ParseDuration(c.BakeTime); err != nil {
			errs = append(errs, fmt.Sprintf("%s.rollout.canary.bake_time must be a valid Go duration: %v", prefix, err))
		}
	}
	errs = append(errs, validateLocalCallbackWorkflowPath(prefix+".rollout.canary.promote_callback", c.PromoteCallback)...)
	errs = append(errs, validateLocalCallbackWorkflowPath(prefix+".rollout.canary.rollback_callback", c.RollbackCallback)...)
	return errs
}

// validateDeployTarget checks deploy_target.mode and the reserved GitOps fields
// (branch, track_sha), which are meaningful only when mode is gitops.
func validateDeployTarget(prefix string, d *DeployTarget) []string {
	if d == nil {
		return nil
	}
	var errs []string
	mode := d.GetMode()
	if mode != DeployTargetModeDispatch && mode != DeployTargetModeGitOps {
		errs = append(errs, fmt.Sprintf("%s.deploy_target.mode must be one of: dispatch, gitops", prefix))
	}
	if mode == DeployTargetModeDispatch {
		if d.TrackSHA {
			errs = append(errs, fmt.Sprintf("%s.deploy_target.track_sha is only valid when mode is gitops", prefix))
		}
		if d.Branch != "" {
			errs = append(errs, fmt.Sprintf("%s.deploy_target.branch is only valid when mode is gitops", prefix))
		}
	}
	if d.Branch != "" {
		if strings.HasPrefix(d.Branch, "/") || strings.HasSuffix(d.Branch, "/") || strings.ContainsAny(d.Branch, " \t\n\r\f\v") {
			errs = append(errs, fmt.Sprintf("%s.deploy_target.branch must not have leading/trailing slashes or whitespace", prefix))
		}
	}
	return errs
}

// validateConfigLevel validates the config-level reserved fields.
func validateConfigLevel(cfg *TrunkConfig) []string {
	var errs []string

	// pin_mode must be tag or sha.
	if cfg.PinMode != "" && cfg.PinMode != PinModeTag && cfg.PinMode != PinModeSHA {
		errs = append(errs, "pin_mode must be one of: tag, sha")
	}

	// cli_version_sha, when set, must be a 40-char lowercase-hex commit SHA so it
	// can be SHA-pinned into the generated setup-cli refs without producing a
	// broken, unresolvable ref.
	if cfg.CLIVersionSHA != "" && !commitSHARe.MatchString(cfg.CLIVersionSHA) {
		errs = append(errs, "cli_version_sha must be a 40-character lowercase hex commit SHA")
	}

	// release_trigger must be push or dispatch.
	if cfg.ReleaseTrigger != "" && cfg.ReleaseTrigger != ReleaseTriggerPush && cfg.ReleaseTrigger != ReleaseTriggerDispatch {
		errs = append(errs, "release_trigger must be one of: push, dispatch")
	}

	// dispatch_inputs may not shadow generator-owned reserved names, must carry a
	// name safe to emit as a workflow_dispatch input key (and referenced via
	// ${{ inputs.<name> }}), and choice inputs need options that are safe to emit
	// verbatim. Every sibling identifier is charset-validated; the dispatch input
	// name and its choice options reach raw YAML the same way, so they are guarded
	// here rather than left to fail at actionlint or GitHub parse time.
	for _, name := range sortedKeys(toStringKeyed(cfg.DispatchInputs)) {
		di := cfg.DispatchInputs[name]
		if reservedDispatchInputNames[name] {
			errs = append(errs, fmt.Sprintf("dispatch_inputs.%s shadows a reserved dispatch input name", name))
		}
		errs = append(errs, validateJobIDSafeName("dispatch_inputs", name)...)
		switch di.Type {
		case "", DispatchInputTypeString, DispatchInputTypeBoolean, DispatchInputTypeEnvironment, DispatchInputTypeNumber:
			// ok
		case DispatchInputTypeChoice:
			if len(di.Options) == 0 {
				errs = append(errs, fmt.Sprintf("dispatch_inputs.%s is a choice input but has no options", name))
			}
			for _, opt := range di.Options {
				if !dispatchInputOptionRe.MatchString(opt) {
					errs = append(errs, fmt.Sprintf(
						"dispatch_inputs.%s option %q must contain only letters, digits, dots, hyphens, and underscores", name, opt))
				}
			}
		default:
			errs = append(errs, fmt.Sprintf("dispatch_inputs.%s.type must be one of: string, boolean, choice, environment, number", name))
		}
	}

	// environment_config keys must reference declared environments.
	if len(cfg.EnvironmentConfig) > 0 {
		envSet := make(map[string]bool, len(cfg.Environments))
		for _, e := range cfg.Environments {
			envSet[e] = true
		}
		for _, name := range sortedKeys(toEnvKeyed(cfg.EnvironmentConfig)) {
			if !envSet[name] {
				errs = append(errs, fmt.Sprintf("environment_config has key %q which is not in environments %v", name, cfg.Environments))
			}
		}
	}

	errs = append(errs, validateTelemetry(cfg.Telemetry)...)
	errs = append(errs, validateEnvironmentConfig(cfg)...)
	errs = append(errs, validateTokenSources(cfg)...)
	errs = append(errs, validateRollback(cfg.Rollback)...)
	errs = append(errs, validateActionPins(cfg)...)
	errs = append(errs, validateActionFolder(cfg.ActionFolder)...)
	errs = append(errs, validateReconcile(cfg.Reconcile)...)
	errs = append(errs, validateTagGrammar(cfg)...)
	errs = append(errs, validateExtraTriggers("extra_triggers", cfg.ExtraTriggers)...)

	return errs
}

// tagGrammarAllowedChars is the allowlist of characters a tag_grammar
// component may contain. Every character in it is simultaneously safe in a
// git ref, a regex literal, and a single-quoted shell string, so a value built
// only from this set can never break the resolved regex, the created git tag,
// or the single-quoted `sed '...'` cascade's own generated release workflow
// emits. A blocklist would need to anticipate every shell and regex
// metacharacter (including a bare single quote, which terminates the emitted
// shell string); an allowlist rejects everything it does not explicitly
// admit, so nothing new can slip through.
const tagGrammarAllowedChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"

// tagGrammarUnsafeChar reports whether s contains any character outside
// tagGrammarAllowedChars. A tag component carrying such a character could not
// be compiled into a regex, created as a git tag, or spliced into the
// generated release workflow's single-quoted sed expression, so it is
// rejected up front rather than failing opaquely at tag time.
func tagGrammarUnsafeChar(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune(tagGrammarAllowedChars, r) {
			return true
		}
	}
	return false
}

// validateTagGrammar structurally validates the optional tag_grammar block.
// These are hard errors, not advisories: an empty pre-release token, a
// component carrying a character outside tagGrammarAllowedChars, or a dryrun
// token that collides with the pre-release token would each produce a
// grammar that cannot round-trip, so generation must not proceed. A nil block
// is valid and preserves the historical grammar. The redundant-prefix case is
// handled separately as a non-fatal advisory (see TrunkConfig.TagGrammarWarnings).
func validateTagGrammar(cfg *TrunkConfig) []string {
	g := cfg.TagGrammar
	if g == nil {
		return nil
	}
	var errs []string

	if g.PreReleaseToken != nil {
		if *g.PreReleaseToken == "" {
			errs = append(errs, "tag_grammar.prerelease_token must not be empty")
		} else if tagGrammarUnsafeChar(*g.PreReleaseToken) {
			errs = append(errs, fmt.Sprintf(
				"tag_grammar.prerelease_token %q must contain only letters, digits, '.', '_', and '-'", *g.PreReleaseToken))
		}
	}
	if g.Prefix != nil && tagGrammarUnsafeChar(*g.Prefix) {
		errs = append(errs, fmt.Sprintf(
			"tag_grammar.prefix %q must contain only letters, digits, '.', '_', and '-'", *g.Prefix))
	}
	if g.PreReleaseSeparator != nil && tagGrammarUnsafeChar(*g.PreReleaseSeparator) {
		errs = append(errs, fmt.Sprintf(
			"tag_grammar.prerelease_separator %q must contain only letters, digits, '.', '_', and '-'", *g.PreReleaseSeparator))
	}
	if g.DryRunToken != nil && tagGrammarUnsafeChar(*g.DryRunToken) {
		errs = append(errs, fmt.Sprintf(
			"tag_grammar.dryrun_token %q must contain only letters, digits, '.', '_', and '-'", *g.DryRunToken))
	}

	// The dryrun token must stay distinguishable from the pre-release token, or
	// a rehearsal tag would parse as a real pre-release. Compare the resolved
	// values so a token that collides only after defaulting is still caught.
	spec := cfg.ResolveTagGrammar()
	if spec.DryRunToken == spec.PreReleaseToken {
		errs = append(errs, fmt.Sprintf(
			"tag_grammar.dryrun_token %q must differ from the prerelease_token so rehearsal tags stay distinguishable", spec.DryRunToken))
	}

	return errs
}

// validateReconcile checks the opt-in reconcile companion toggle. Source
// selects the change-source adapter the companion recognizes; Commit selects
// how an adoption commit is routed. Both are constrained to the known values
// so an unrecognized adapter or mode fails validation rather than reaching the
// generator with an assumption it cannot honor.
func validateReconcile(r *ReconcileConfig) []string {
	if r == nil {
		return nil
	}
	var errs []string
	switch r.Source {
	case "", ReconcileSourceDependabot:
		// ok
	default:
		errs = append(errs, fmt.Sprintf("reconcile.source must be one of: %s", ReconcileSourceDependabot))
	}
	switch r.Commit {
	case "", ReconcileCommitAppend, ReconcileCommitFollowup:
		// ok
	default:
		errs = append(errs, fmt.Sprintf("reconcile.commit must be one of: %s, %s", ReconcileCommitAppend, ReconcileCommitFollowup))
	}
	return errs
}

// validateActionPins charset-validates every action_pins override value. Each
// value is spliced raw into a generated workflow's uses: line, so it must be
// bounded to a ref plus an optional trailing "# <version>" comment and must
// never carry a newline or other character that could break out of the
// emitted YAML scalar.
func validateActionPins(cfg *TrunkConfig) []string {
	var errs []string
	for _, action := range sortedKeys(cfg.ActionPins) {
		ref := cfg.ActionPins[action]
		if !actionPinValueRe.MatchString(ref) {
			errs = append(errs, fmt.Sprintf("action_pins[%q]: invalid ref %q", action, ref))
		}
	}
	return errs
}

// repositoryDispatchTypeRe matches a repository_dispatch event type that is safe
// to emit verbatim under the on.repository_dispatch.types list. GitHub accepts
// arbitrary client-chosen event-type strings, but cascade renders them into YAML
// without quoting, so the accepted set is constrained to characters that need no
// escaping and cannot break the surrounding document.
var repositoryDispatchTypeRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// validateRepositoryDispatchTypes checks a repository_dispatch trigger's event
// types under the given prefix. At least one type is required (an empty list
// would make the trigger fire on every event type, which is never the intent for
// an opt-in rollback signal), and each type must be non-blank and safe to emit.
func validateRepositoryDispatchTypes(prefix string, rd *RepositoryDispatchTrigger) []string {
	if rd == nil {
		return nil
	}
	var errs []string
	if len(rd.Types) == 0 {
		errs = append(errs, fmt.Sprintf("%s.types must list at least one event type", prefix))
		return errs
	}
	for _, t := range rd.Types {
		if strings.TrimSpace(t) == "" {
			errs = append(errs, fmt.Sprintf("%s.types must not contain a blank event type", prefix))
			continue
		}
		if !repositoryDispatchTypeRe.MatchString(t) {
			errs = append(errs, fmt.Sprintf(
				"%s.types entry %q must contain only letters, digits, dots, hyphens, and underscores", prefix, t))
		}
	}
	return errs
}

// validateExtraTriggers checks an extra_triggers block. The only rule is on
// merge_group: extra_triggers attaches its events to the orchestrate workflow,
// which cuts release tags, publishes releases, and runs deploys while writing
// state. A raw merge_group trigger there lets a speculative merge-queue build on
// a candidate branch that may never land publish a real release and write real
// state, because orchestrate derives its target branch from the run ref with no
// gh-readonly-queue guard. The supported way to gate pull requests inside a
// merge queue is the read-only merge_queue.enabled lane, so merge_group under
// extra_triggers is rejected and the user is pointed at it. The prefix carries
// the component scope (or the top-level path) for an actionable message.
func validateExtraTriggers(prefix string, et *ExtraTriggers) []string {
	if et == nil || et.MergeGroup == nil {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s.merge_group is not allowed: extra_triggers attaches to the orchestrate workflow, "+
			"which cuts release tags, publishes releases, and runs deploys while writing state, so a "+
			"speculative merge-queue build could publish a real release from a candidate commit. To gate "+
			"pull requests inside a merge queue, set merge_queue.enabled, which emits a read-only validation lane.",
		prefix)}
}

// validateRollback checks the opt-in rollback configuration. A nil block is the
// default and passes. When repository_dispatch is set, its event types are
// validated the same way the shared repository_dispatch trigger is.
func validateRollback(rb *RollbackConfig) []string {
	if rb == nil {
		return nil
	}
	return validateRepositoryDispatchTypes("rollback.repository_dispatch", rb.RepositoryDispatch)
}

// validateTokenSources checks the optional release_token_app / state_token_app
// GitHub App identities. Each is lenient when absent. When present, BOTH app_id
// and private_key must be set (a half-configured App is rejected), and each must
// be a secret reference: a bare GitHub Actions secret name or a
// "${{ secrets.* }}" / "${{ vars.* }}" expression. Raw key material is rejected
// outright (a value containing a newline or the substring "PRIVATE KEY"), since
// these fields are references, never inline credentials.
func validateTokenSources(cfg *TrunkConfig) []string {
	var errs []string
	errs = append(errs, validateAppTokenSource("release_token_app", cfg.ReleaseTokenApp)...)
	errs = append(errs, validateAppTokenSource("state_token_app", cfg.StateTokenApp)...)
	return errs
}

// validateAppTokenSource checks a single App token source under the given prefix.
func validateAppTokenSource(prefix string, src *AppTokenSource) []string {
	if src == nil {
		return nil
	}
	var errs []string
	if strings.TrimSpace(src.AppID) == "" {
		errs = append(errs, fmt.Sprintf("%s.app_id is required when %s is set", prefix, prefix))
	} else {
		errs = append(errs, validateSecretReference(prefix+".app_id", src.AppID)...)
	}
	if strings.TrimSpace(src.PrivateKey) == "" {
		errs = append(errs, fmt.Sprintf("%s.private_key is required when %s is set", prefix, prefix))
	} else {
		errs = append(errs, validateSecretReference(prefix+".private_key", src.PrivateKey)...)
	}
	return errs
}

// validateSecretReference reports whether value is a secret reference rather than
// raw credential material. A reference is either a wrapped GitHub Actions
// expression ("${{ secrets.NAME }}" or "${{ vars.NAME }}") or a bare safe secret
// name. Any value carrying a newline or the substring "PRIVATE KEY" is rejected
// as raw key material.
func validateSecretReference(field, value string) []string {
	if strings.ContainsAny(value, "\r\n") || strings.Contains(value, "PRIVATE KEY") {
		return []string{fmt.Sprintf(
			"%s must be a secret reference (a secret name or a ${{ secrets.* }} expression), not raw key material", field)}
	}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "${{") && strings.HasSuffix(trimmed, "}}") {
		return nil
	}
	if safeSecretName(trimmed) {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s must be a valid GitHub Actions secret name or a ${{ secrets.* }} expression", field)}
}

// validateEnvironmentConfig checks the additive per-environment fields under
// environment_config (required_reviewers, wait_timer, branch_policy and its
// patterns, secrets, variables). Every check is lenient and applies only when a
// field is present, so a manifest that omits these fields, or omits
// environment_config entirely, is never rejected. Secret and variable entries
// are NAMES only: they are checked for a safe name shape, never treated as
// credential values. The env-key-references-a-declared-environment check lives
// in validateConfigLevel and is not duplicated here.
func validateEnvironmentConfig(cfg *TrunkConfig) []string {
	if len(cfg.EnvironmentConfig) == 0 {
		return nil
	}
	var errs []string
	for _, name := range sortedKeys(toEnvKeyed(cfg.EnvironmentConfig)) {
		ec := cfg.EnvironmentConfig[name]
		prefix := "environment_config." + name

		if ec.WaitTimer < 0 || ec.WaitTimer > MaxWaitTimerMinutes {
			errs = append(errs, fmt.Sprintf("%s.wait_timer must be between 0 and %d minutes", prefix, MaxWaitTimerMinutes))
		}

		switch ec.BranchPolicy {
		case "", EnvBranchPolicyProtected, EnvBranchPolicyCustom, EnvBranchPolicyAll:
			// ok
		default:
			errs = append(errs, fmt.Sprintf("%s.branch_policy must be one of: protected, custom, all", prefix))
		}
		if ec.BranchPolicy != EnvBranchPolicyCustom {
			if len(ec.BranchPatterns) > 0 {
				errs = append(errs, fmt.Sprintf("%s.branch_patterns is only valid when branch_policy is custom", prefix))
			}
			if len(ec.TagPatterns) > 0 {
				errs = append(errs, fmt.Sprintf("%s.tag_patterns is only valid when branch_policy is custom", prefix))
			}
		}

		for i, r := range ec.RequiredReviewers {
			if !safeReviewerSlug(r) {
				errs = append(errs, fmt.Sprintf("%s.required_reviewers[%d] %q must be a non-empty user or team slug (optionally org/team) with no whitespace", prefix, i, r))
			}
		}

		for i, s := range ec.Secrets {
			if !safeSecretName(s) {
				errs = append(errs, fmt.Sprintf("%s.secrets[%d] %q must be a valid GitHub Actions secret name (letters, digits, underscores; not starting with a digit)", prefix, i, s))
			}
		}
		for i, v := range ec.Variables {
			if !safeSecretName(v) {
				errs = append(errs, fmt.Sprintf("%s.variables[%d] %q must be a valid GitHub Actions variable name (letters, digits, underscores; not starting with a digit)", prefix, i, v))
			}
		}
	}
	return errs
}

// safeReviewerSlug reports whether s is a plausible GitHub reviewer slug: a
// non-empty, whitespace-free string of at most two slash-separated segments
// (a "user" slug or an "org/team" slug). It guards a name reference, not a
// credential, so it is deliberately permissive about the slug character set
// while rejecting empty, whitespace-only, and value-carrying shapes.
func safeReviewerSlug(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, " \t\n\r\f\v") {
		return false
	}
	segments := strings.Split(s, "/")
	if len(segments) > 2 {
		return false
	}
	for _, seg := range segments {
		if seg == "" {
			return false
		}
	}
	return true
}

// validateTelemetry checks only the newly reserved telemetry.webhook fields.
// adapter is left unchecked on purpose: an arbitrary adapter string parses and
// validates today, and the seam must stay additive, so no enum is enforced. The
// checks here are lenient and apply only when webhook is present, so they never
// reject a manifest that is valid without these new fields. secret_name is a
// reference to a GitHub Actions secret, never an inline token, so it is checked
// for a safe secret-name shape rather than treated as a credential.
func validateTelemetry(t *TelemetryConfig) []string {
	if t == nil || t.Webhook == nil {
		return nil
	}
	var errs []string
	w := t.Webhook
	if w.URL != "" {
		if !strings.HasPrefix(w.URL, "https://") && !strings.HasPrefix(w.URL, "http://") {
			errs = append(errs, "telemetry.webhook.url must be an http(s) URL")
		}
	}
	if w.SecretName != "" && !safeSecretName(w.SecretName) {
		errs = append(errs, "telemetry.webhook.secret_name must be a valid GitHub Actions secret name (letters, digits, underscores; not starting with a digit)")
	}
	return errs
}

// safeSecretName reports whether name is a syntactically valid GitHub Actions
// secret name: ASCII letters, digits, and underscores only, and not starting
// with a digit. This guards a reference, not a credential value.
func safeSecretName(name string) bool {
	for i, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		isUnderscore := r == '_'
		if i == 0 && isDigit {
			return false
		}
		if !isLetter && !isDigit && !isUnderscore {
			return false
		}
	}
	return true
}

// validateComponents validates the top-level components map (#176). When a
// components: block is present the top-level config is the shared default set
// and each component inherits it, so the rules enforced here are:
//
//   - Component names must be job-ID-safe (so a generator can key job IDs on the
//     name without breakage).
//   - path and tag_prefix are required per component; path must be a clean
//     relative path (no leading slash, no ".." segments).
//   - Per-component tag prefixes must be distinct, so no two components share a
//     tag namespace and reap or read each other's tags.
//   - A component may not override a top-level-only (global) field, and may not
//     pin the concurrency group to a shared literal (only cancel_in_progress is
//     overridable; the group is derived per component).
//   - A manifest-global concurrency.group literal is rejected while components
//     are declared, because it would collapse every component onto one lane.
func validateComponents(cfg *TrunkConfig) []string {
	if len(cfg.Components) == 0 {
		return nil
	}
	var errs []string

	if cfg.Concurrency != nil && cfg.Concurrency.Group != "" {
		errs = append(errs, "concurrency.group must not be set to a shared literal when components are declared; "+
			"the orchestrate group is derived per component so runs never serialize across components")
	}

	tagPrefixOwner := make(map[string]string, len(cfg.Components))
	for _, name := range sortedComponentKeys(cfg.Components) {
		errs = append(errs, validateJobIDSafeName("components."+name, name)...)
		comp := cfg.Components[name]

		if comp.Path == "" {
			errs = append(errs, fmt.Sprintf("components.%s.path is required", name))
		} else if strings.HasPrefix(comp.Path, "/") {
			errs = append(errs, fmt.Sprintf("components.%s.path must be a relative path, not absolute", name))
		} else if strings.Contains(comp.Path, "..") {
			errs = append(errs, fmt.Sprintf("components.%s.path must not contain '..' segments", name))
		}

		if comp.TagPrefix == "" {
			errs = append(errs, fmt.Sprintf("components.%s.tag_prefix is required so each component has its own tag namespace", name))
		} else if prior, seen := tagPrefixOwner[comp.TagPrefix]; seen {
			errs = append(errs, fmt.Sprintf(
				"components.%s.tag_prefix %q collides with component %q; each component needs a distinct tag namespace",
				name, comp.TagPrefix, prior))
		} else {
			tagPrefixOwner[comp.TagPrefix] = name
		}

		if comp.Concurrency != nil && comp.Concurrency.Group != "" {
			errs = append(errs, fmt.Sprintf(
				"components.%s.concurrency.group cannot be overridden; the orchestrate group is derived per component", name))
		}

		errs = append(errs, validateExtraTriggers(fmt.Sprintf("components.%s.extra_triggers", name), comp.ExtraTriggers)...)

		for _, key := range sortedKeys(toAnyKeyed(comp.Extra)) {
			if _, global := globalOnlyComponentFields[key]; global {
				errs = append(errs, fmt.Sprintf(
					"components.%s.%s is a top-level-only field and cannot be overridden per component", name, key))
			} else {
				errs = append(errs, fmt.Sprintf("components.%s has unknown field %q", name, key))
			}
		}
	}
	return errs
}

// validateVersionOverrides validates the reserved release.version_overrides
// pointer. Rules frozen at v1 mirror components.path: any configured dir must be
// a clean relative path (no leading slash, no ".." segments). Validation applies
// only when the block is present, so it never rejects a manifest that is valid
// without it.
func validateVersionOverrides(release *ReleaseConfig) []string {
	if release == nil || release.VersionOverrides == nil {
		return nil
	}
	dir := release.VersionOverrides.Dir
	if dir == "" {
		return nil
	}
	var errs []string
	if strings.HasPrefix(dir, "/") {
		errs = append(errs, "release.version_overrides.dir must be a relative path, not absolute")
	} else if strings.Contains(dir, "..") {
		errs = append(errs, "release.version_overrides.dir must not contain '..' segments")
	}
	return errs
}

// sortedComponentKeys returns the keys of a ComponentConfig map in deterministic order.
func sortedComponentKeys(m map[string]ComponentConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedKeys returns the keys of a string-valued map in deterministic order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// toStringKeyed adapts a DispatchInput map to a string-keyed map for sortedKeys.
func toStringKeyed(m map[string]DispatchInput) map[string]string {
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = ""
	}
	return out
}

// toEnvKeyed adapts an EnvironmentConfig map to a string-keyed map for sortedKeys.
func toEnvKeyed(m map[string]EnvironmentConfig) map[string]string {
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = ""
	}
	return out
}

// toAnyKeyed adapts an inline catch-all map to a string-keyed map for sortedKeys.
func toAnyKeyed(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = ""
	}
	return out
}

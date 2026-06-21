package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
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

// validateLocalCallbackWorkflowPath checks that a local callback workflow path
// is either a bare filename, a .github/workflows/... path, or a cross-repo
// external ref (containing "@"). Any other form is rejected because GitHub
// requires local reusable workflows to live under .github/workflows/.
func validateLocalCallbackWorkflowPath(prefix, workflow string) []string {
	if workflow == "" {
		return nil
	}
	// Cross-repo external refs contain "@" - always valid.
	if strings.Contains(workflow, "@") {
		return nil
	}
	// Bare filename (no "/") - valid; normalizeWorkflowPath will route it.
	if !strings.Contains(workflow, "/") {
		return nil
	}
	// .github/workflows/... path - valid.
	if strings.HasPrefix(workflow, ".github/workflows/") || strings.HasPrefix(workflow, "./.github/workflows/") {
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

	// dispatch_inputs may not shadow generator-owned reserved names, and choice
	// inputs need options.
	for _, name := range sortedKeys(toStringKeyed(cfg.DispatchInputs)) {
		di := cfg.DispatchInputs[name]
		if reservedDispatchInputNames[name] {
			errs = append(errs, fmt.Sprintf("dispatch_inputs.%s shadows a reserved dispatch input name", name))
		}
		switch di.Type {
		case "", DispatchInputTypeString, DispatchInputTypeBoolean, DispatchInputTypeEnvironment, DispatchInputTypeNumber:
			// ok
		case DispatchInputTypeChoice:
			if len(di.Options) == 0 {
				errs = append(errs, fmt.Sprintf("dispatch_inputs.%s is a choice input but has no options", name))
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

	return errs
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

// validateComponents validates the reserved top-level components map (#176).
// Rules frozen at v1: component names must be job-ID-safe (so a future
// generator can key job IDs on the name without breakage), and any configured
// Path must be a clean relative path (no leading slash, no ".." segments).
func validateComponents(cfg *TrunkConfig) []string {
	if len(cfg.Components) == 0 {
		return nil
	}
	var errs []string
	for _, name := range sortedComponentKeys(cfg.Components) {
		errs = append(errs, validateJobIDSafeName("components."+name, name)...)
		comp := cfg.Components[name]
		if comp.Path != "" {
			if strings.HasPrefix(comp.Path, "/") {
				errs = append(errs, fmt.Sprintf("components.%s.path must be a relative path, not absolute", name))
			} else if strings.Contains(comp.Path, "..") {
				errs = append(errs, fmt.Sprintf("components.%s.path must not contain '..' segments", name))
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

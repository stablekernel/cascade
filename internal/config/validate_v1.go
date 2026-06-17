package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
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
	return errs
}

// validateDeployTarget checks deploy_target.mode.
func validateDeployTarget(prefix string, d *DeployTarget) []string {
	if d == nil {
		return nil
	}
	mode := d.GetMode()
	if mode != DeployTargetModeDispatch && mode != DeployTargetModeGitOps {
		return []string{fmt.Sprintf("%s.deploy_target.mode must be one of: dispatch, gitops", prefix)}
	}
	return nil
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

	return errs
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

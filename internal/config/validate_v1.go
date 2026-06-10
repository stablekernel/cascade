package config

import (
	"fmt"
	"sort"
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

// validateWorkflowRunXOR enforces that exactly one of workflow:/run: is set, and
// that shell: is only present alongside run:.
func validateWorkflowRunXOR(prefix, workflow, run, shell string) []string {
	var errs []string
	switch {
	case workflow == "" && run == "":
		errs = append(errs, fmt.Sprintf("%s: one of workflow or run is required", prefix))
	case workflow != "" && run != "":
		errs = append(errs, fmt.Sprintf("%s: workflow and run are mutually exclusive; set exactly one", prefix))
	}
	if shell != "" && run == "" {
		errs = append(errs, fmt.Sprintf("%s: shell is only valid alongside run", prefix))
	}
	return errs
}

// validateExternalDeployWorkflowOnly enforces that external deploys are
// reusable-workflow only. Inline run: callbacks are emitted as cascade-owned
// jobs in the primary repo; an external deploy resolves to a workflow in the
// external repo, so it must declare workflow: and cannot use run:/shell:.
func validateExternalDeployWorkflowOnly(prefix, workflow, run, shell string) []string {
	var errs []string
	if run != "" {
		errs = append(errs, fmt.Sprintf("%s: external deploys are reusable-workflow only; run is not supported (set workflow instead)", prefix))
	}
	if shell != "" {
		errs = append(errs, fmt.Sprintf("%s: external deploys are reusable-workflow only; shell is not supported", prefix))
	}
	if run == "" && workflow == "" {
		errs = append(errs, fmt.Sprintf("%s: workflow is required", prefix))
	}
	return errs
}

// validateJobControlFields rejects job-control fields that GHA does not accept on
// a reusable-workflow (jobs.<id>.uses) callback. runs_on and concurrency apply
// cleanly only to inline run: callbacks and cascade-owned jobs.
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

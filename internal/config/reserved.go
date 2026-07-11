package config

import (
	"fmt"
	"sort"
)

// reservedCategory classifies why a manifest field that parses today is still
// rejected by lint. The category selects the operator-facing message, so the
// wording lives in exactly one place.
type reservedCategory int

const (
	// reservedNotWired marks a field that is modeled and parses but is not wired
	// to generation in this cascade version (category (a)).
	reservedNotWired reservedCategory = iota
	// reservedWrongPlace marks a field that is parsed and validated but is
	// deliberately never emitted because it belongs somewhere else (category
	// (b)): callback timeout_minutes belongs inside the called workflow.
	reservedWrongPlace
)

// errorFor renders the category message for a concrete field path.
func (c reservedCategory) errorFor(path string) string {
	switch c {
	case reservedWrongPlace:
		return fmt.Sprintf("%s: timeout belongs in the called workflow, not the caller; GitHub forbids timeout-minutes on a job that calls a reusable workflow", path)
	default:
		return fmt.Sprintf("%s is reserved and not implemented in this cascade version; remove it from the manifest", path)
	}
}

// reservedFieldDoc records one reserved field path and its category.
type reservedFieldDoc struct {
	path     string
	category reservedCategory
}

// reservedFieldRegistry is the single, reviewable source of truth for the
// reserved-field taxonomy: every manifest field that parses today but is not
// wired to generation, hand-verified field-by-field against internal/generate
// (the doc comments in the schema are unreliable - some "reserved-shape" fields
// are in fact consumed). It mirrors the globalOnlyComponentFields pattern one
// level up. Presence detection is per-field (nil/zero checks) in
// validateReservedFields below; this table keeps the path -> category mapping in
// one place so a reviewer can audit the surface without reading the walk.
//
// Deliberately NOT reserved (consumed by generation, verified): rollout
// max_parallel and fail_fast (promote.go, generator.go) and config-level
// job_timeout_minutes (generator.go). target_sha is a component-state field on
// EnvState, not a manifest field, so it never reaches manifest lint.
var reservedFieldRegistry = []reservedFieldDoc{
	{"telemetry", reservedNotWired},
	{"deploys[].rollout.type", reservedNotWired},
	{"deploys[].rollout.canary", reservedNotWired},
	{"deploys[].rollout.blue_green", reservedNotWired},
	{"deploys[].deploy_target", reservedNotWired},
	{"release.version_overrides", reservedNotWired},
	{"<callback>.timeout_minutes", reservedWrongPlace},
}

// validateReservedFields rejects any manifest that populates a reserved field.
// It walks the top-level surface and every component (components share the same
// build/deploy/release/validate/external types), so a reserved field is caught
// wherever it appears.
func validateReservedFields(cfg *TrunkConfig) []string {
	if cfg == nil {
		return nil
	}
	var errs []string

	// telemetry is a top-level-only block (global-only per component), so it is
	// checked once here.
	if cfg.Telemetry != nil {
		errs = append(errs, reservedNotWired.errorFor("telemetry"))
	}

	errs = append(errs, reservedInScope("", cfg.Builds, cfg.Deploys, cfg.Validate, cfg.Release, cfg.External)...)

	names := make([]string, 0, len(cfg.Components))
	for name := range cfg.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := cfg.Components[name]
		scope := fmt.Sprintf("components.%s.", name)
		errs = append(errs, reservedInScope(scope, c.Builds, c.Deploys, c.Validate, c.Release, c.External)...)
	}

	return errs
}

// reservedInScope checks the callbacks and release block reachable from one
// scope (the top level, scope "", or a single component, scope
// "components.<name>."). Factoring it keeps the top-level and per-component walks
// identical.
func reservedInScope(scope string, builds []BuildConfig, deploys []DeployConfig, validate *ValidateConfig, release *ReleaseConfig, external []ExternalRepoConfig) []string {
	var errs []string

	for i := range builds {
		errs = append(errs, reservedCallbackTimeout(fmt.Sprintf("%sbuilds[%d]", scope, i), builds[i].TimeoutMinutes)...)
	}
	for i := range deploys {
		p := fmt.Sprintf("%sdeploys[%d]", scope, i)
		errs = append(errs, reservedCallbackTimeout(p, deploys[i].TimeoutMinutes)...)
		errs = append(errs, reservedRollout(p, deploys[i].Rollout)...)
		errs = append(errs, reservedDeployTarget(p, deploys[i].DeployTarget)...)
	}
	if validate != nil {
		errs = append(errs, reservedCallbackTimeout(scope+"validate", validate.TimeoutMinutes)...)
	}
	if release != nil && release.VersionOverrides != nil {
		errs = append(errs, reservedNotWired.errorFor(scope+"release.version_overrides"))
	}
	for i := range external {
		for j := range external[i].Deploys {
			p := fmt.Sprintf("%sexternal[%d].deploys[%d]", scope, i, j)
			errs = append(errs, reservedRollout(p, external[i].Deploys[j].Rollout)...)
			errs = append(errs, reservedDeployTarget(p, external[i].Deploys[j].DeployTarget)...)
		}
	}

	return errs
}

// reservedCallbackTimeout rejects a callback's timeout_minutes (category (b)).
// The per-callback timeout is parsed and validated but never emitted: every
// cascade callback is a reusable-workflow uses: job, and GitHub forbids
// timeout-minutes there, so the timeout belongs inside the called workflow.
func reservedCallbackTimeout(prefix string, timeoutMinutes int) []string {
	if timeoutMinutes <= 0 {
		return nil
	}
	return []string{reservedWrongPlace.errorFor(prefix + ".timeout_minutes")}
}

// reservedRollout rejects the inert members of a rollout block (type, canary,
// blue_green). The consumed knobs max_parallel and fail_fast are intentionally
// not checked here - they are wired to the generated strategy block.
func reservedRollout(prefix string, r *RolloutConfig) []string {
	if r == nil {
		return nil
	}
	var errs []string
	if r.Type != "" {
		errs = append(errs, reservedNotWired.errorFor(prefix+".rollout.type"))
	}
	if r.Canary != nil {
		errs = append(errs, reservedNotWired.errorFor(prefix+".rollout.canary"))
	}
	if r.BlueGreen != nil {
		errs = append(errs, reservedNotWired.errorFor(prefix+".rollout.blue_green"))
	}
	return errs
}

// reservedDeployTarget rejects a deploy_target block (the reserved GitOps-mirror
// deploy variant, not wired to generation).
func reservedDeployTarget(prefix string, dt *DeployTarget) []string {
	if dt == nil {
		return nil
	}
	return []string{reservedNotWired.errorFor(prefix + ".deploy_target")}
}

package config

import "fmt"

// HasComponents reports whether the manifest declares a components: block. When
// it does not, the component dimension does not exist and the single-component
// code path is used untouched.
func (c *TrunkConfig) HasComponents() bool {
	return len(c.Components) > 0
}

// ComponentConcurrencyGroup derives the orchestrate concurrency group for a
// named component. Composing the component identity into the group keeps two
// components from serializing against each other on one lane, which a bare
// shared literal would silently do. The exact emitted expression is a generator
// concern and may be refined there; the invariant fixed here is that the
// component identity is always part of the key.
func ComponentConcurrencyGroup(name string) string {
	return fmt.Sprintf("orchestrate-%s-${{ github.ref }}", name)
}

// ResolvedComponent is the effective configuration for one component: its
// identity (Name), the subtree it owns (Path), and Config, a TrunkConfig holding
// the shared defaults with the component's overrides applied. Path is a
// per-component axis with no home on TrunkConfig, so it is carried here rather
// than folded into Config; downstream stages scope version and state work to it.
type ResolvedComponent struct {
	Name   string
	Path   string
	Config *TrunkConfig
}

// ResolveComponent returns the effective configuration for the named component:
// a copy of the shared top-level defaults with every inheritable field the
// component overrides applied, the component's required tag prefix set, and a
// concurrency group derived per component so no two components collapse onto one
// serialization lane. Global (top-level-only) fields are carried through from
// the shared config unchanged, and the effective config declares no nested
// components of its own.
//
// It returns an error if the component is not declared. ResolveComponent assumes
// the manifest already passed validateComponents; it does not re-validate.
func (c *TrunkConfig) ResolveComponent(name string) (*ResolvedComponent, error) {
	comp, ok := c.Components[name]
	if !ok {
		return nil, fmt.Errorf("component %q is not declared", name)
	}

	eff := *c            // shallow copy: global fields carried through verbatim
	eff.Components = nil // an effective per-component config has no nested components

	// Required per-component tag namespace.
	eff.TagPrefix = comp.TagPrefix

	// Inheritable overrides: apply only where the component set a value.
	if comp.TagGrammar != nil {
		eff.TagGrammar = comp.TagGrammar
	}
	if comp.Environments != nil {
		eff.Environments = comp.Environments
	}
	if comp.ReleaseTrigger != "" {
		eff.ReleaseTrigger = comp.ReleaseTrigger
	}
	if comp.AllowBreakingChanges != nil {
		eff.AllowBreakingChanges = *comp.AllowBreakingChanges
	}
	if comp.Validate != nil {
		eff.Validate = comp.Validate
	}
	if comp.Builds != nil {
		eff.Builds = comp.Builds
	}
	if comp.Deploys != nil {
		eff.Deploys = comp.Deploys
	}
	if comp.Publish != nil {
		eff.Publish = comp.Publish
	}
	if comp.External != nil {
		eff.External = comp.External
	}
	if comp.Notify != nil {
		eff.Notify = comp.Notify
	}
	if comp.Release != nil {
		eff.Release = comp.Release
	}
	if comp.Changelog != nil {
		eff.Changelog = comp.Changelog
	}
	if comp.RunsOn != nil {
		eff.RunsOn = comp.RunsOn
	}
	if comp.JobTimeoutMinutes != nil {
		eff.JobTimeoutMinutes = *comp.JobTimeoutMinutes
	}
	if comp.DispatchInputs != nil {
		eff.DispatchInputs = comp.DispatchInputs
	}
	if comp.ExtraTriggers != nil {
		eff.ExtraTriggers = comp.ExtraTriggers
	}
	if comp.PRPreview != nil {
		eff.PRPreview = comp.PRPreview
	}
	if comp.ValidateCheck != nil {
		eff.ValidateCheck = comp.ValidateCheck
	}
	if comp.Rollback != nil {
		eff.Rollback = comp.Rollback
	}
	if comp.Deployments != nil {
		eff.Deployments = comp.Deployments
	}
	if comp.EnvironmentConfig != nil {
		eff.EnvironmentConfig = comp.EnvironmentConfig
	}
	if comp.Triggers != nil {
		eff.Triggers = comp.Triggers
	}
	if comp.ReleaseToken != "" {
		eff.ReleaseToken = comp.ReleaseToken
	}
	if comp.ReleaseTokenApp != nil {
		eff.ReleaseTokenApp = comp.ReleaseTokenApp
	}

	// Concurrency: cancel_in_progress is inheritable, the group is derived per
	// component. Start from the shared cancel policy, let the component override
	// it, and always compose the per-component group so the shared lane trap
	// cannot occur.
	cancel := c.GetConcurrencyCancelInProgress()
	if comp.Concurrency != nil {
		cancel = comp.Concurrency.CancelInProgress
	}
	eff.Concurrency = &ConcurrencyConfig{
		Group:            ComponentConcurrencyGroup(name),
		CancelInProgress: cancel,
	}

	return &ResolvedComponent{Name: name, Path: comp.Path, Config: &eff}, nil
}

// globalOnlyComponentFields is the set of top-level-only (global) manifest keys
// that must never be overridden per component. It backs the targeted rejection
// message in validateComponents; any of these keys set under a component is a
// configuration error, not a silent no-op. Keys mirror the yaml names in
// TrunkConfig and the override matrix.
var globalOnlyComponentFields = map[string]struct{}{
	"schema_version":  {},
	"trunk_branch":    {},
	"cli_version":     {},
	"cli_version_sha": {},
	"state_token":     {},
	"state_token_app": {},
	"manifest_file":   {},
	"manifest_key":    {},
	"action_folder":   {},
	"git":             {},
	"drift_check":     {},
	"reconcile":       {},
	"pin_mode":        {},
	"action_pins":     {},
	"telemetry":       {},
	"merge_queue":     {},
	"components":      {},
}

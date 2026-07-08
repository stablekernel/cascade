package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stablekernel/cascade/internal/taggrammar"
)

// clone returns a fully independent deep copy of the config via a JSON round
// trip. Every TrunkConfig field carries a json tag and none is json:"-" (the
// sole json:"-" field, ComponentConfig.Extra, lives off this type), so the round
// trip reproduces the value exactly while breaking every pointer, slice, and map
// alias. ResolveComponent needs this: a shallow copy (eff := *c) would leave
// non-overridden pointer/slice/map fields aliasing the shared config, so one
// component's derivation could bleed into a sibling.
func (c *TrunkConfig) clone() (*TrunkConfig, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("cloning config: %w", err)
	}
	var dup TrunkConfig
	if err := json.Unmarshal(data, &dup); err != nil {
		return nil, fmt.Errorf("cloning config: %w", err)
	}
	return &dup, nil
}

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

// PromoteConcurrencyGroup derives the promote concurrency group for a named
// component. It lives in a dedicated "promote-" namespace, deliberately distinct
// from ComponentConcurrencyGroup's "orchestrate-" namespace, because GitHub
// concurrency groups are repo-global across workflows: a promote workflow that
// reused the orchestrate key would silently serialize or cancel against that
// component's orchestrate run. Like the single-component promote lane (which
// keys on the bare workflow name to serialize every promote run), this carries
// no ref or mode axis, so all of a component's promote runs serialize against
// each other; the component identity keeps two components from sharing a lane.
func PromoteConcurrencyGroup(name string) string {
	return fmt.Sprintf("promote-%s", name)
}

// GetComponentTagPrefix returns the declared tag_prefix for the named component,
// the tag namespace that component's versions and tags live under. It errors when
// the component is not declared. Version and tag discovery use this so a
// component's scan is scoped to its own namespace and never a sibling's.
func (c *TrunkConfig) GetComponentTagPrefix(name string) (string, error) {
	comp, ok := c.Components[name]
	if !ok {
		return "", fmt.Errorf("component %q is not declared", name)
	}
	return comp.TagPrefix, nil
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

	// Deep-copy the shared config so non-overridden pointer/slice/map fields do
	// not alias across sibling components. Global fields are carried through
	// verbatim by the copy.
	eff, err := c.clone()
	if err != nil {
		return nil, err
	}
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

	// Derive a path-scoped push-paths filter from the component subtree when
	// neither the component nor the shared defaults set an explicit triggers
	// list, so each component's orchestrate workflow fires only on changes under
	// its own path. An explicit inherited or per-component triggers list (applied
	// above) still wins.
	if len(eff.Triggers) == 0 {
		eff.Triggers = []string{strings.TrimRight(comp.Path, "/") + "/**"}
	}

	return &ResolvedComponent{Name: name, Path: comp.Path, Config: eff}, nil
}

// TagGrammarSpec returns the tag grammar a component reads and emits its versions
// under: the component's resolved grammar (carrying its required tag_prefix and
// any inherited or overridden tag_grammar block) with StrictPrefix forced true.
// The strict flip is the HLD Section 5 isolation invariant: a component with a
// declared tag_prefix parses its tags literally so api-1.2.3 and web-1.2.3 never
// cross-match, and a nested-substring prefix (api- vs api-beta-) cannot collide
// either. It is forced on regardless of whether a tag_grammar block set
// strict_prefix, because the per-component namespace boundary is not optional. The
// implicit default (single-component) path does not call this; it keeps the
// permissive ResolveTagGrammar spec so single-component reads are unchanged.
func (r *ResolvedComponent) TagGrammarSpec() taggrammar.Spec {
	spec := r.Config.ResolveTagGrammar()
	spec.StrictPrefix = true
	return spec
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

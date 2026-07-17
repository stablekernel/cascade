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

// RollbackConcurrencyGroup derives the rollback concurrency group for a named
// component. It lives in a dedicated "rollback-" namespace, deliberately distinct
// from both ComponentConcurrencyGroup's "orchestrate-" namespace and
// PromoteConcurrencyGroup's "promote-" namespace, because GitHub concurrency
// groups are repo-global across workflows: a rollback workflow that reused either
// key would silently serialize or cancel against that component's orchestrate or
// promote run. Like the single-component rollback lane (which keys on the bare
// workflow name to serialize every rollback run), this carries no ref or mode
// axis, so all of a component's rollback runs serialize against each other; the
// component identity keeps two components from sharing a lane.
func RollbackConcurrencyGroup(name string) string {
	return fmt.Sprintf("rollback-%s", name)
}

// GetComponentTagPrefix returns the declared tag_grammar.prefix for the named
// component, the tag namespace that component's versions and tags live under. It
// errors when the component is not declared. Version and tag discovery use this
// so a component's scan is scoped to its own namespace and never a sibling's.
// validateComponents guarantees a declared component carries a non-empty prefix.
func (c *TrunkConfig) GetComponentTagPrefix(name string) (string, error) {
	comp, ok := c.Components[name]
	if !ok {
		return "", fmt.Errorf("component %q is not declared", name)
	}
	if comp.TagGrammar == nil || comp.TagGrammar.Prefix == nil {
		return "", nil
	}
	return *comp.TagGrammar.Prefix, nil
}

// ResolvedComponent is the effective configuration for one component: its
// identity (Name), the subtree it owns (Path), and Config, a TrunkConfig holding
// the shared defaults with the component's overrides applied. Path is a
// per-component axis with no home on TrunkConfig, so it is carried here rather
// than folded into Config; downstream stages scope version and state work to it.
type ResolvedComponent struct {
	Name string
	Path string
	// ExtraPaths is the component's effective additional path set: its own
	// extra_paths unioned with the manifest's top-level shared_paths, deduplicated
	// and deterministically ordered. It is additive to Path. Version derivation and
	// change detection scope to Path plus ExtraPaths so a shared-dependency change
	// bumps and fires this component; it is empty when neither field is declared,
	// keeping the single-component and no-shared-path shapes unchanged.
	ExtraPaths []string
	Config     *TrunkConfig
}

// ResolveComponent returns the effective configuration for the named component:
// a copy of the shared top-level defaults with every inheritable field the
// component overrides applied (including its required tag_grammar.prefix), and a
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
	eff.Components = nil  // an effective per-component config has no nested components
	eff.SharedPaths = nil // top-level sugar, expanded into per-component extra paths below

	// Inheritable overrides: uniform deep-merge. Every inheritable block merges
	// field-by-field into the shared defaults rather than replacing it wholesale.
	// An override the component leaves unset keeps the inherited value; a scalar
	// or slice the component sets replaces the inherited one; a nested block (or a
	// keyed map such as environment_config) merges key-by-key so a partial
	// override never drops the inherited siblings. The component's required tag
	// namespace (tag_grammar.prefix) rides this merge: the block folds onto the
	// shared tag_grammar so a component setting only prefix keeps the inherited
	// sibling sub-fields. The one axis that stays a union rather than a merge is
	// extra_paths/shared_paths, resolved separately below.
	if err := deepMergeComponentOverrides(eff, comp); err != nil {
		return nil, err
	}

	// Concurrency: cancel_in_progress is inheritable, the group is derived per
	// component. Start from the shared cancel policy, let the component override
	// it, and always compose the per-component group so the shared lane trap
	// cannot occur.
	// A component overrides the shared cancel policy only when it states one:
	// an empty per-component concurrency block inherits rather than silently
	// resolving to false, which the pre-pointer plain bool could not express.
	cancel := c.Concurrency.CancelInProgressOr(true)
	if comp.Concurrency != nil && comp.Concurrency.CancelInProgress != nil {
		cancel = *comp.Concurrency.CancelInProgress
	}
	eff.Concurrency = &ConcurrencyConfig{
		Group:            ComponentConcurrencyGroup(name),
		CancelInProgress: &cancel,
	}

	// Derive a path-scoped push-paths filter from the component subtree when
	// neither the component nor the shared defaults set an explicit triggers
	// list, so each component's orchestrate workflow fires only on changes under
	// its own path. An explicit inherited or per-component triggers list (applied
	// above) still wins.
	if len(eff.Triggers) == 0 {
		eff.Triggers = []string{strings.TrimRight(comp.Path, "/") + "/**"}
	}

	// Effective additional paths: the component's own extra_paths unioned with the
	// manifest's top-level shared_paths, deduplicated in declaration order for a
	// single stable downstream representation. These fire the workflow (folded into the push
	// filter below) and, threaded by the orchestrator, count toward the version bump
	// and change detection. When both are empty this is nil and nothing changes.
	extraPaths := mergePaths(comp.ExtraPaths, c.SharedPaths)

	// Fold the extra paths into the push-paths filter so a shared-dependency change
	// fires this component's orchestrate workflow. GetAllTriggers reads eff.Triggers,
	// so appending here reaches the emitted on.push.paths block. Deduplicated so an
	// extra path that already equals a trigger is not repeated.
	if len(extraPaths) > 0 {
		eff.Triggers = mergePaths(eff.Triggers, extraPaths)
	}

	return &ResolvedComponent{Name: name, Path: comp.Path, ExtraPaths: extraPaths, Config: eff}, nil
}

// mergePaths returns the union of the given path lists with duplicates removed
// and a deterministic (first-occurrence) order, or nil when the union is empty.
// It backs the shared_paths fan-out and the push-filter fold so every
// downstream sink sees one stable representation of a component's effective
// additional paths. Declaration order is preserved, never re-sorted: trigger
// pattern order is semantic under the GitHub Actions paths filter
// (last-match-wins), and extra paths are positive patterns appended after the
// component's own list so they trigger unconditionally without disturbing the
// list's "!" exclusions.
func mergePaths(lists ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, list := range lists {
		for _, p := range list {
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

// deepMergeComponentOverrides folds a component's inheritable overrides into the
// shared defaults in place, field-by-field. It marshals both the base config and
// the component override to a generic JSON tree and recursively merges the
// override onto the base: an object present on both sides merges key-by-key (so a
// nested block or a keyed map such as environment_config keeps its inherited
// siblings), while a scalar or array on the override side replaces the inherited
// value. A field the component leaves unset is absent from its JSON (every
// override field carries omitempty), so it never clobbers the inherited value.
//
// The JSON round trip is the same fidelity contract clone() already depends on:
// every TrunkConfig field is JSON-tagged and none is json:"-" except the inline
// Extra catch-all, which is empty here (validation rejects unknown keys before
// resolution). Component-only keys such as path and extra_paths have no
// TrunkConfig field and are ignored on decode; concurrency is overwritten by the
// per-component derivation that follows, so leaving it in the merge is harmless.
func deepMergeComponentOverrides(base *TrunkConfig, override ComponentConfig) error {
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return fmt.Errorf("merging component overrides: %w", err)
	}
	overrideJSON, err := json.Marshal(override)
	if err != nil {
		return fmt.Errorf("merging component overrides: %w", err)
	}
	var baseTree, overrideTree map[string]any
	if err := json.Unmarshal(baseJSON, &baseTree); err != nil {
		return fmt.Errorf("merging component overrides: %w", err)
	}
	if err := json.Unmarshal(overrideJSON, &overrideTree); err != nil {
		return fmt.Errorf("merging component overrides: %w", err)
	}
	mergeJSONTrees(baseTree, overrideTree)
	mergedJSON, err := json.Marshal(baseTree)
	if err != nil {
		return fmt.Errorf("merging component overrides: %w", err)
	}
	// Reset before decoding so no stale slice or map tail from the pre-merge base
	// lingers; the merged tree is a superset of the base keys, so this is a full
	// rewrite of the effective config.
	*base = TrunkConfig{}
	if err := json.Unmarshal(mergedJSON, base); err != nil {
		return fmt.Errorf("merging component overrides: %w", err)
	}
	return nil
}

// mergeReplaceLeafKeys names the manifest blocks that must WHOLE-REPLACE on
// override rather than field-merge, even though they marshal to a JSON object.
// These are exclusive or atomic blocks whose fields are not independently
// composable, so recursively merging one component's fields onto the inherited
// block's fields corrupts it:
//
//   - secrets (SecretsConfig): XOR-exclusive - either "inherit" (all caller
//     secrets) OR an explicit {name: source} allow-list, never both. A
//     field-merge of an inherited "inherit" with a component allow-list yields
//     both set, and generation gives "inherit" precedence, silently BROADENING
//     the component's least-privilege allow-list. It has a custom UnmarshalYAML.
//   - runs_on (RunsOn): polymorphic - a scalar label, a list of labels, or a
//     {group, labels} object, exactly one form. Field-merging two forms leaves a
//     stale field from the inherited form set. It has a custom UnmarshalYAML.
//   - release_token_app / state_token_app (AppTokenSource): an atomic credential
//     identity (app_id + private_key). Field-merging would splice an app_id from
//     one source with a private_key from another; whole-replace makes an
//     incomplete override a clean validation error instead of a bad credential.
//
// A new exclusive, polymorphic, atomic, or custom-decoded block MUST be added
// here, or the tree-merge will silently corrupt it. The keys are matched by name
// at any depth because each of these names maps to exactly one such type in the
// schema (for example secrets under validate and under each build/deploy).
var mergeReplaceLeafKeys = map[string]struct{}{
	"secrets":           {},
	"runs_on":           {},
	"release_token_app": {},
	"state_token_app":   {},
}

// mergeJSONTrees recursively merges the override tree onto the base tree in
// place, implementing the deep-merge inheritance rule at the generic-JSON level:
// when a key holds an object on both sides the objects merge recursively; a null
// override is treated as unset and leaves the base untouched; anything else
// (scalar or array) replaces the base value. A key in mergeReplaceLeafKeys is a
// replace-leaf: it whole-replaces rather than recursing, so an exclusive or
// atomic block is never corrupted by a field-merge. It is the merge core behind
// deepMergeComponentOverrides.
func mergeJSONTrees(base, override map[string]any) {
	for key, ov := range override {
		if ov == nil {
			continue // an explicit null is treated as unset: inherit the base
		}
		if _, replaceLeaf := mergeReplaceLeafKeys[key]; !replaceLeaf {
			if ovMap, ok := ov.(map[string]any); ok {
				if baseMap, ok := base[key].(map[string]any); ok {
					mergeJSONTrees(baseMap, ovMap)
					continue
				}
			}
		}
		base[key] = ov
	}
}

// TagGrammarSpec returns the tag grammar a component reads and emits its versions
// under: the component's resolved grammar (carrying its required
// tag_grammar.prefix and any inherited or overridden sibling sub-fields) with
// StrictPrefix forced true.
// The strict flip is the HLD Section 5 isolation invariant: a component with a
// declared prefix parses its tags literally so api-1.2.3 and web-1.2.3 never
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
	"shared_paths":    {},
}

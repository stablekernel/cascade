package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// StateWrite describes a single scoped mutation of the state (or latest_release)
// subtree. Exactly one form is addressed per write, discriminated by Env:
//
//   - Env != "": a state directive. State != nil sets the addressed env leaf;
//     State == nil deletes it. Component == "" targets the single-component
//     state.<Env> form (byte-identical to today); a non-empty Component targets
//     state.components.<Component>.<Env>, carrying a full EnvState.
//   - Env == "": a latest_release directive. Latest != nil sets it; Latest == nil
//     deletes it. Component == "" targets the top-level latest_release; a
//     non-empty Component targets latest_release.components.<Component>.
//
// A manifest is either entirely single-component (all Component == "") or
// entirely component-scoped (all Component != ""); the two forms never coexist in
// one manifest, so WriteScopedState rejects a call that mixes them.
type StateWrite struct {
	// Component is the component name, or "" for the single-component form.
	Component string
	// Env is the environment key being addressed, or "" for a latest_release
	// directive.
	Env string
	// State is the env leaf value for a state directive (Env != ""). nil deletes
	// the env key.
	State *EnvState
	// Latest is the release value for a latest_release directive (Env == ""). nil
	// deletes the release record.
	Latest *LatestReleaseState
}

// WriteScopedState rewrites manifest bytes, applying writes against the parsed
// YAML node tree and touching only the addressed value nodes. Every sibling
// subtree, and any key the binary does not model, is preserved verbatim as its
// original parsed node.
//
// The single-component form (all writes with Component == "") reconciles the
// whole `state` node from the union of set writes so its bytes stay identical to
// the historical whole-state-node replacement: the map marshal is sorted, and a
// key absent from the set writes is dropped, matching the previous omitempty
// delete semantics. There are no unmodeled siblings under a single-component
// `state` node to preserve, so a whole-node rebuild is the byte-faithful choice.
//
// The component-scoped form (Component != "") node-patches only the addressed
// state.components.<name>.<env> (or latest_release.components.<name>) leaf,
// leaving every other component and env as its original node. A sibling component
// the binary cannot model is preserved precisely because it is never deserialized
// into a typed value. Deleting the last env of a component removes the component
// mapping, and deleting the last component removes the components mapping and the
// now-empty state key, so a manifest returning to no component state collapses
// cleanly.
//
// manifestKey defaults to DefaultManifestKey when empty. YAML parse and encode
// failures are wrapped with %w.
func WriteScopedState(current []byte, manifestKey string, writes ...StateWrite) ([]byte, error) {
	if manifestKey == "" {
		manifestKey = DefaultManifestKey
	}

	hasSingle, hasComponent := false, false
	for _, w := range writes {
		if w.Component == "" {
			hasSingle = true
		} else {
			hasComponent = true
		}
	}
	if hasSingle && hasComponent {
		return nil, fmt.Errorf("scoped state write mixes single-component and component-scoped forms in one manifest")
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		return nil, fmt.Errorf("parsing manifest for state write: %w", err)
	}

	root := documentMapping(&doc)
	if root == nil {
		// Empty or non-mapping document: start a fresh document root so the write
		// still produces a well-formed manifest.
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	}

	section := mappingValue(root, manifestKey)
	if section == nil || section.Kind != yaml.MappingNode {
		section = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMappingValue(root, manifestKey, section)
	}

	if hasComponent {
		if err := applyComponentWrites(section, writes); err != nil {
			return nil, err
		}
	} else if err := applySingleComponentWrites(section, writes); err != nil {
		return nil, err
	}

	data, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("encoding manifest after state write: %w", err)
	}
	return data, nil
}

// applySingleComponentWrites reconciles the whole single-component `state` node
// from the union of set writes (byte-identical to the historical whole-node
// replacement) and applies the top-level latest_release directive.
func applySingleComponentWrites(section *yaml.Node, writes []StateWrite) error {
	state := make(map[string]*EnvState)
	haveStateDirective := false
	for _, w := range writes {
		if w.Env == "" {
			continue // latest_release directive, handled below
		}
		haveStateDirective = true
		if w.State != nil {
			state[w.Env] = w.State
		}
		// A State == nil delete contributes no key, so the rebuilt node omits it.
	}

	if haveStateDirective {
		if len(state) == 0 {
			deleteMappingKey(section, "state")
		} else {
			node, err := valueNode(state)
			if err != nil {
				return fmt.Errorf("encoding state for state write: %w", err)
			}
			setMappingValue(section, "state", node)
		}
	}

	for _, w := range writes {
		if w.Env != "" {
			continue
		}
		if w.Latest == nil {
			deleteMappingKey(section, "latest_release")
		} else {
			node, err := valueNode(w.Latest)
			if err != nil {
				return fmt.Errorf("encoding latest_release for state write: %w", err)
			}
			setMappingValue(section, "latest_release", node)
		}
	}
	return nil
}

// applyComponentWrites node-patches the addressed state.components.<name>.<env>
// and latest_release.components.<name> leaves, preserving every other component
// and env verbatim, then collapses any mapping the writes emptied.
func applyComponentWrites(section *yaml.Node, writes []StateWrite) error {
	for _, w := range writes {
		if w.Env != "" {
			if err := applyComponentStateLeaf(section, w); err != nil {
				return err
			}
			continue
		}
		if err := applyComponentLatestLeaf(section, w); err != nil {
			return err
		}
	}
	collapseComponentContainer(section, "state")
	collapseComponentContainer(section, "latest_release")
	return nil
}

// applyComponentStateLeaf sets or deletes state.components.<Component>.<Env>.
func applyComponentStateLeaf(section *yaml.Node, w StateWrite) error {
	if w.State == nil {
		if comps := existingChild(section, "state", "components"); comps != nil {
			if comp := mappingValue(comps, w.Component); comp != nil && comp.Kind == yaml.MappingNode {
				deleteMappingKey(comp, w.Env)
			}
		}
		return nil
	}
	node, err := valueNode(w.State)
	if err != nil {
		return fmt.Errorf("encoding component state for state write: %w", err)
	}
	comp := mappingChild(mappingChild(mappingChild(section, "state"), "components"), w.Component)
	setMappingValue(comp, w.Env, node)
	return nil
}

// applyComponentLatestLeaf sets or deletes latest_release.components.<Component>.
func applyComponentLatestLeaf(section *yaml.Node, w StateWrite) error {
	if w.Latest == nil {
		if comps := existingChild(section, "latest_release", "components"); comps != nil {
			deleteMappingKey(comps, w.Component)
		}
		return nil
	}
	node, err := valueNode(w.Latest)
	if err != nil {
		return fmt.Errorf("encoding component latest_release for state write: %w", err)
	}
	comps := mappingChild(mappingChild(section, "latest_release"), "components")
	setMappingValue(comps, w.Component, node)
	return nil
}

// collapseComponentContainer removes emptied nesting under key (state or
// latest_release): empty per-component mappings, an empty components mapping, and
// finally the empty container key itself, so a manifest returning to no component
// state never emits a dangling `state.components: {}`.
func collapseComponentContainer(section *yaml.Node, key string) {
	container := mappingValue(section, key)
	if container == nil || container.Kind != yaml.MappingNode {
		return
	}
	comps := mappingValue(container, "components")
	if comps != nil && comps.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(comps.Content); {
			val := comps.Content[i+1]
			if val.Kind == yaml.MappingNode && len(val.Content) == 0 {
				name := comps.Content[i].Value
				deleteMappingKey(comps, name)
				continue // indices shifted; re-examine this position
			}
			i += 2
		}
		if len(comps.Content) == 0 {
			deleteMappingKey(container, "components")
		}
	}
	if len(container.Content) == 0 {
		deleteMappingKey(section, key)
	}
}

// existingChild returns the nested child mapping at parent.key.child without
// creating it, or nil when either level is absent or not a mapping.
func existingChild(parent *yaml.Node, key, child string) *yaml.Node {
	mid := mappingValue(parent, key)
	if mid == nil || mid.Kind != yaml.MappingNode {
		return nil
	}
	leaf := mappingValue(mid, child)
	if leaf == nil || leaf.Kind != yaml.MappingNode {
		return nil
	}
	return leaf
}

// WriteManifestState rewrites manifest bytes, replacing only the mutable state
// subtree (the `state` and `latest_release` keys under manifestKey) and leaving
// every other key untouched. It is the single-component wrapper over
// WriteScopedState, retained so its existing single-component callers and byte
// output are unchanged.
//
// State writers (reset, promote/finalize, hotfix/finalize, rollback) only ever
// change `state` and `latest_release`; the rest of the manifest is read-only at
// write time. Earlier writers re-marshaled the typed CICDFile, which silently
// dropped any key the running binary does not model (for example a config field
// added in a newer cascade release). Operating on the parsed YAML node and
// touching only the two mutable keys preserves all other content verbatim,
// including configuration this binary does not model and any comments.
//
// Its contract is reconcile to the map, not patch the map: it writes every
// `state.<env>` present in state and, by rebuilding the whole state node from
// state alone, drops any env or marker key (notably `prerelease`) present in the
// fetched bytes but absent from the map. This delete-awareness is load-bearing
// because the publish transition drops the prerelease marker by omission from the
// map (overlayOwnedState). state is written when non-empty and the `state` key is
// removed when empty, matching the previous omitempty behavior; the same rule
// applies to latest_release against nil. manifestKey defaults to
// DefaultManifestKey when empty.
func WriteManifestState(current []byte, manifestKey string, state map[string]*EnvState, latest *LatestReleaseState) ([]byte, error) {
	writes := make([]StateWrite, 0, len(state)+1)
	for env, st := range state {
		writes = append(writes, StateWrite{Env: env, State: st})
	}
	// Explicit deletes for env/marker keys present in the fetched bytes but absent
	// from the map, so the reconcile-to-map contract drops them (for example the
	// prerelease marker on a publish). The single-component rebuild would drop them
	// regardless, but expressing them as StateWrite deletes keeps the wrapper's
	// contract explicit and independent of that rebuild detail.
	for _, env := range fetchedStateEnvKeys(current, manifestKey) {
		if _, kept := state[env]; !kept {
			writes = append(writes, StateWrite{Env: env, State: nil})
		}
	}
	// Always reconcile latest_release: nil deletes, non-nil sets.
	writes = append(writes, StateWrite{Latest: latest})
	return WriteScopedState(current, manifestKey, writes...)
}

// fetchedStateEnvKeys returns the direct child keys of the single-component
// `state` node in current, or nil when absent. Used by WriteManifestState to
// derive the explicit env/marker deletes its reconcile-to-map contract emits.
func fetchedStateEnvKeys(current []byte, manifestKey string) []string {
	if manifestKey == "" {
		manifestKey = DefaultManifestKey
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		return nil
	}
	root := documentMapping(&doc)
	if root == nil {
		return nil
	}
	section := mappingValue(root, manifestKey)
	if section == nil || section.Kind != yaml.MappingNode {
		return nil
	}
	state := mappingValue(section, "state")
	if state == nil || state.Kind != yaml.MappingNode {
		return nil
	}
	keys := make([]string, 0, len(state.Content)/2)
	for i := 0; i+1 < len(state.Content); i += 2 {
		keys = append(keys, state.Content[i].Value)
	}
	return keys
}

// documentMapping returns the top-level mapping node of a parsed YAML document,
// or nil when the document is empty or its root is not a mapping.
func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		return doc.Content[0]
	}
	return nil
}

// mappingValue returns the value node for key in a mapping node, or nil when the
// key is absent. A mapping node stores alternating key/value children.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setMappingValue replaces the value for key in place when present, preserving
// its position, or appends a new key/value pair when absent.
func setMappingValue(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	m.Content = append(m.Content, keyNode, val)
}

// mappingChild returns the value node for key in m, creating an empty mapping in
// place (appended) when the key is absent or its existing value is not a mapping.
// It lets a scoped write synthesize a missing state -> components -> <name> path.
func mappingChild(m *yaml.Node, key string) *yaml.Node {
	v := mappingValue(m, key)
	if v == nil || v.Kind != yaml.MappingNode {
		v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMappingValue(m, key, v)
	}
	return v
}

// deleteMappingKey removes the key/value pair for key from a mapping node when
// present.
func deleteMappingKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// valueNode marshals v through YAML and returns the resulting value node, so a
// typed value can be spliced into the document tree.
func valueNode(v any) (*yaml.Node, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var n yaml.Node
	if err := yaml.Unmarshal(data, &n); err != nil {
		return nil, err
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		return n.Content[0], nil
	}
	return &n, nil
}

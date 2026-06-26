package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// WriteManifestState rewrites manifest bytes in place, replacing only the
// mutable state subtree (the `state` and `latest_release` keys under
// manifestKey) and leaving every other key untouched.
//
// State writers (reset, promote/finalize, hotfix/finalize, rollback) only ever
// change `state` and `latest_release`; the rest of the manifest is read-only at
// write time. Earlier writers re-marshaled the typed CICDFile, which silently
// dropped any key the running binary does not model (for example a config field
// added in a newer cascade release). Operating on the parsed YAML node and
// touching only the two mutable keys preserves all other content verbatim,
// including configuration this binary does not model and any comments.
//
// state is written when non-empty and the `state` key is removed when empty,
// matching the previous `omitempty` behavior; the same rule applies to
// latest_release against nil. manifestKey defaults to DefaultManifestKey when
// empty.
func WriteManifestState(current []byte, manifestKey string, state map[string]*EnvState, latest *LatestReleaseState) ([]byte, error) {
	if manifestKey == "" {
		manifestKey = DefaultManifestKey
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

	if len(state) == 0 {
		deleteMappingKey(section, "state")
	} else {
		node, err := valueNode(state)
		if err != nil {
			return nil, fmt.Errorf("encoding state for state write: %w", err)
		}
		setMappingValue(section, "state", node)
	}

	if latest == nil {
		deleteMappingKey(section, "latest_release")
	} else {
		node, err := valueNode(latest)
		if err != nil {
			return nil, fmt.Errorf("encoding latest_release for state write: %w", err)
		}
		setMappingValue(section, "latest_release", node)
	}

	data, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("encoding manifest after state write: %w", err)
	}
	return data, nil
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

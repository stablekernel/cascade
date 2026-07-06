package pinreconcile

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// manifestIndent is the indent width the surgical re-encode uses, matching the
// two-space indent cascade's own manifests are written with.
const manifestIndent = 2

// SetActionPinSurgical changes a single action's value under
// ci.config.action_pins in a user manifest's raw bytes, touching nothing else.
// It walks the document as a yaml.Node tree rather than round-tripping through
// a typed struct, so every unrelated key, comment, and the document's key
// order survive byte for byte. A missing action_pins mapping (or a missing key
// inside it) is created rather than treated as an error, so a manifest that
// has never carried an override still adopts cleanly.
func SetActionPinSurgical(doc []byte, action, ref string) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("manifest is empty")
	}

	ciMapping := mappingValue(root.Content[0], "ci")
	if ciMapping == nil {
		return nil, fmt.Errorf("manifest has no top-level %q mapping", "ci")
	}

	configMapping := mappingValue(ciMapping, "config")
	if configMapping == nil {
		return nil, fmt.Errorf("manifest has no %q mapping under %q", "config", "ci")
	}

	pinsMapping := mappingValue(configMapping, "action_pins")
	if pinsMapping == nil {
		pinsMapping = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		configMapping.Content = append(configMapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "action_pins"},
			pinsMapping,
		)
	}

	setMappingValue(pinsMapping, action, ref)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(manifestIndent)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	return buf.Bytes(), nil
}

// mappingValue returns the value node paired with key in mapping's Content, or
// nil when the key is absent. mapping must be a yaml.MappingNode.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// setMappingValue sets key's scalar value to value inside mapping, updating an
// existing entry in place (clearing any stale style, tag, and line comment so
// the new value re-encodes cleanly) or appending a new key/value pair when the
// key is absent.
func setMappingValue(mapping *yaml.Node, key, value string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			v := mapping.Content[i+1]
			v.Value = value
			v.Style = 0
			v.Tag = "!!str"
			v.LineComment = ""
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

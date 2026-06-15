// Package schema embeds the hand-authored JSON Schema for the cascade manifest
// and exposes it to the CLI and to editors.
//
// The canonical, authored schema lives at internal/schema/manifest.schema.json
// and is embedded into the binary. Two on-disk copies are kept byte-identical to
// it for distribution: schema/manifest.schema.json (a stable repo-root path) and
// docs/public/manifest.schema.json (served at the published $id URL). The drift
// test in this package asserts all three copies match, so a maintainer who edits
// one must sync the others.
//
// To regenerate the published copies after editing the canonical schema:
//
//	cascade schema --output schema/manifest.schema.json
//	cp schema/manifest.schema.json docs/public/manifest.schema.json
package schema

import _ "embed"

//go:embed manifest.schema.json
var manifest []byte

// Bytes returns the embedded manifest JSON Schema as raw bytes.
func Bytes() []byte {
	out := make([]byte, len(manifest))
	copy(out, manifest)
	return out
}

// String returns the embedded manifest JSON Schema as a string.
func String() string {
	return string(manifest)
}

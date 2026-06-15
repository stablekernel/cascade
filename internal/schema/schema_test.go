// The tests in this file are the lockstep contract between the hand-authored
// manifest JSON Schema and the Go manifest types. When a manifest field is
// added, changed, or removed in internal/config, this schema must be updated to
// match, and these tests prove it: a corpus of real manifests (cascade's own
// manifest, every e2e scenario, and the README examples) must validate against
// the schema, while a set of known-bad documents must be rejected. A sibling
// test asserts the three on-disk copies of the schema stay byte-identical.
//
// The JSON Schema validator (santhosh-tekuri/jsonschema) is a test-only
// dependency and never enters the cascade binary.
package schema_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/schema"
)

// repoRoot returns the worktree root, derived from this test file's location
// (internal/schema -> ../..).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// compileSchema compiles the embedded schema as draft-07.
func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schema.String()))
	if err != nil {
		t.Fatalf("unmarshal embedded schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	const id = "https://stablekernel.github.io/cascade/manifest.schema.json"
	if err := c.AddResource(id, doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	compiled, err := c.Compile(id)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return compiled
}

// toJSONValue normalizes a value loaded from YAML into the JSON-like types that
// the validator expects (map[string]any, []any, float64, string, bool, nil) by
// round-tripping through encoding/json.
func toJSONValue(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal to json: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	return out
}

// loadYAMLDoc decodes a YAML document into a generic map.
func loadYAMLDoc(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}
	return doc
}

func TestSchema_ValidatesCascadeOwnManifest(t *testing.T) {
	sch := compileSchema(t)
	root := repoRoot(t)

	data, err := os.ReadFile(filepath.Join(root, ".github", "manifest.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	doc := loadYAMLDoc(t, data)
	if err := sch.Validate(toJSONValue(t, doc)); err != nil {
		t.Fatalf("cascade .github/manifest.yaml must validate: %v", err)
	}
}

func TestSchema_ValidatesE2EScenarioConfigs(t *testing.T) {
	sch := compileSchema(t)
	root := repoRoot(t)

	matches, err := filepath.Glob(filepath.Join(root, "e2e", "scenarios", "*.yaml"))
	if err != nil {
		t.Fatalf("glob scenarios: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one e2e scenario manifest")
	}

	validated := 0
	for _, path := range matches {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read scenario: %v", err)
			}
			doc := loadYAMLDoc(t, data)
			cfg, ok := doc["config"]
			if !ok {
				t.Skipf("scenario has no config block")
			}
			// Scenario files are not ci-wrapped; wrap the config block.
			wrapped := map[string]any{"ci": map[string]any{"config": cfg}}
			if err := sch.Validate(toJSONValue(t, wrapped)); err != nil {
				t.Fatalf("scenario config must validate: %v", err)
			}
			validated++
		})
	}
	if validated == 0 {
		t.Fatal("no scenario config blocks were validated")
	}
}

func TestSchema_ValidatesREADMEExamples(t *testing.T) {
	sch := compileSchema(t)
	root := repoRoot(t)

	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}

	blocks := extractYAMLFences(string(data))
	ciBlocks := 0
	for i, block := range blocks {
		if !firstMeaningfulLineIsCI(block) {
			continue
		}
		ciBlocks++
		i := i
		block := block
		t.Run(fmt.Sprintf("readme-ci-block-%d", ciBlocks), func(t *testing.T) {
			doc := loadYAMLDoc(t, []byte(block))
			if err := sch.Validate(toJSONValue(t, doc)); err != nil {
				t.Fatalf("README ci block #%d must validate: %v", i, err)
			}
		})
	}
	if ciBlocks < 2 {
		t.Fatalf("expected at least 2 ci-rooted yaml blocks in README.md, found %d", ciBlocks)
	}
}

// firstMeaningfulLineIsCI reports whether the first non-blank, non-comment line
// of a YAML block is the top-level "ci:" key.
func firstMeaningfulLineIsCI(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return strings.HasPrefix(trimmed, "ci:")
	}
	return false
}

// extractYAMLFences returns the contents of every ```yaml fenced code block.
func extractYAMLFences(md string) []string {
	var blocks []string
	lines := strings.Split(md, "\n")
	inBlock := false
	var cur []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if trimmed == "```yaml" || trimmed == "```yml" {
				inBlock = true
				cur = nil
			}
			continue
		}
		if trimmed == "```" {
			inBlock = false
			blocks = append(blocks, strings.Join(cur, "\n"))
			continue
		}
		cur = append(cur, line)
	}
	return blocks
}

func TestSchema_RejectsKnownBadManifests(t *testing.T) {
	sch := compileSchema(t)

	cases := map[string]map[string]any{
		"unknown top-level key": {
			"ci":        map[string]any{"config": map[string]any{"trunk_branch": "main"}},
			"bogus_top": 1,
		},
		"environments as string": {
			"ci": map[string]any{"config": map[string]any{
				"trunk_branch": "main",
				"environments": "dev",
			}},
		},
		"secrets as integer": {
			"ci": map[string]any{"config": map[string]any{
				"trunk_branch": "main",
				"builds": []any{map[string]any{
					"name":    "app",
					"secrets": 7,
				}},
			}},
		},
		"build missing name": {
			"ci": map[string]any{"config": map[string]any{
				"trunk_branch": "main",
				"builds": []any{map[string]any{
					"workflow": ".github/workflows/build.yaml",
				}},
			}},
		},
	}

	for name, doc := range cases {
		doc := doc
		t.Run(name, func(t *testing.T) {
			if err := sch.Validate(toJSONValue(t, doc)); err == nil {
				t.Fatalf("expected validation to fail for %q, but it passed", name)
			}
		})
	}
}

func TestSchema_AcceptsSecretsUnionForms(t *testing.T) {
	sch := compileSchema(t)

	good := map[string]any{
		"ci": map[string]any{"config": map[string]any{
			"trunk_branch": "main",
			"builds": []any{
				map[string]any{"name": "a", "secrets": "inherit"},
				map[string]any{"name": "b", "secrets": map[string]any{"inherit": true}},
				map[string]any{"name": "c", "secrets": map[string]any{"DB_PASSWORD": "PROD_DB_PASSWORD"}},
			},
		}},
	}
	if err := sch.Validate(toJSONValue(t, good)); err != nil {
		t.Fatalf("secrets union forms must validate: %v", err)
	}
}

func TestSchema_AcceptsRunsOnUnionForms(t *testing.T) {
	sch := compileSchema(t)

	good := map[string]any{
		"ci": map[string]any{"config": map[string]any{
			"trunk_branch": "main",
			"runs_on":      "ubuntu-latest",
			"builds": []any{
				map[string]any{"name": "a", "runs_on": []any{"self-hosted", "linux"}},
				map[string]any{"name": "b", "runs_on": map[string]any{"group": "gpu", "labels": []any{"a100"}}},
			},
		}},
	}
	if err := sch.Validate(toJSONValue(t, good)); err != nil {
		t.Fatalf("runs_on union forms must validate: %v", err)
	}
}

func TestSchema_OnDiskCopiesAreByteIdentical(t *testing.T) {
	root := repoRoot(t)
	paths := []string{
		filepath.Join(root, "internal", "schema", "manifest.schema.json"),
		filepath.Join(root, "schema", "manifest.schema.json"),
		filepath.Join(root, "docs", "public", "manifest.schema.json"),
	}

	var first []byte
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if i == 0 {
			first = data
			// The embedded copy must also match the on-disk canonical file.
			if string(schema.Bytes()) != string(data) {
				t.Fatalf("embedded schema differs from %s", p)
			}
			continue
		}
		if string(data) != string(first) {
			t.Fatalf("schema copy %s differs from %s; run: cascade schema --output schema/manifest.schema.json && cp schema/manifest.schema.json docs/public/manifest.schema.json", p, paths[0])
		}
	}
}

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
	"io/fs"
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

// TestSchema_ValidatesDocsExamples holds the documentation site to the same bar
// the README has always been held to. Every ci-rooted example a reader can copy
// must validate against the published schema.
//
// The docs tree had no such check, and it drifted: examples omitted the required
// trunk_branch, which the schema rejects but lint used to accept, and generation
// then emitted an empty push allow-list. Following the docs produced a pipeline
// that reported green and never ran. This test is what makes the schema, lint,
// and docs agree by construction rather than by review.
func TestSchema_ValidatesDocsExamples(t *testing.T) {
	sch := compileSchema(t)
	root := repoRoot(t)
	docsDir := filepath.Join(root, "docs", "src", "content", "docs")

	ciBlocks := 0
	err := filepath.WalkDir(docsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(path, ".md") && !strings.HasSuffix(path, ".mdx")) {
			return nil
		}
		data, readErr := os.ReadFile(path) // #nosec G304 -- test-only walk of the in-repo docs tree
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for i, block := range extractYAMLFences(string(data)) {
			if !firstMeaningfulLineIsCI(block) {
				continue
			}
			ciBlocks++
			t.Run(fmt.Sprintf("%s-block-%d", rel, i), func(t *testing.T) {
				doc := loadYAMLDoc(t, []byte(block))
				if vErr := sch.Validate(toJSONValue(t, doc)); vErr != nil {
					t.Fatalf("docs example in %s must validate against the published schema: %v", rel, vErr)
				}
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs tree: %v", err)
	}
	if ciBlocks < 2 {
		t.Fatalf("expected at least 2 ci-rooted yaml blocks under %s, found %d", docsDir, ciBlocks)
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

// extractYAMLFences returns the contents of every ```yaml fenced code block,
// including fences that carry Starlight-style info-string attributes such as
// ```yaml title="cascade.yaml". An annotated fence is still a yaml fence and must
// not escape validation; only the exact ```yaml / ```yml used to match, so the
// first annotated example would silently dodge the schema check.
func extractYAMLFences(md string) []string {
	var blocks []string
	lines := strings.Split(md, "\n")
	inBlock := false
	var cur []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if isYAMLFenceOpen(trimmed) {
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

// isYAMLFenceOpen reports whether a trimmed line opens a yaml code fence. The
// info string must begin with the whole token "yaml" or "yml", optionally
// followed by whitespace and attributes (```yaml title="x"). It deliberately
// does not match neighbours like ```yamlfoo or ```yaml-lint, which are different
// languages, not annotated yaml.
func isYAMLFenceOpen(trimmed string) bool {
	info, ok := strings.CutPrefix(trimmed, "```")
	if !ok {
		return false
	}
	for _, lang := range []string{"yaml", "yml"} {
		rest, ok := strings.CutPrefix(info, lang)
		if !ok {
			continue
		}
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return true
		}
	}
	return false
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

func TestSchema_AcceptsTagGrammarBlock(t *testing.T) {
	sch := compileSchema(t)

	good := map[string]any{
		"ci": map[string]any{"config": map[string]any{
			"trunk_branch": "main",
			"tag_grammar": map[string]any{
				"prefix":               "release-",
				"prerelease_token":     "beta",
				"prerelease_separator": ".",
				"dryrun_token":         "rehearsal",
				"strict_prefix":        true,
			},
		}},
	}
	if err := sch.Validate(toJSONValue(t, good)); err != nil {
		t.Fatalf("valid tag_grammar block must validate: %v", err)
	}
}

// TestSchema_RejectsInvalidTagGrammar proves the JSON Schema enforces the same
// character allowlist as config.validateTagGrammar: a token carrying a
// disallowed character or an empty prerelease_token must be rejected, so the
// schema and the Go validator agree.
func TestSchema_RejectsInvalidTagGrammar(t *testing.T) {
	sch := compileSchema(t)

	cases := map[string]map[string]any{
		"prerelease_token with quote": {
			"ci": map[string]any{"config": map[string]any{
				"trunk_branch": "main",
				"tag_grammar":  map[string]any{"prerelease_token": "r'c"},
			}},
		},
		"empty prerelease_token": {
			"ci": map[string]any{"config": map[string]any{
				"trunk_branch": "main",
				"tag_grammar":  map[string]any{"prerelease_token": ""},
			}},
		},
		"unknown tag_grammar key": {
			"ci": map[string]any{"config": map[string]any{
				"trunk_branch": "main",
				"tag_grammar":  map[string]any{"bogus": "x"},
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

// TestSchema_AcceptsAllowBreakingChanges proves the config-level
// allow_breaking_changes boolean is declared, so a manifest that disables the
// breaking-change gate validates under the config object's
// additionalProperties: false.
func TestSchema_AcceptsAllowBreakingChanges(t *testing.T) {
	sch := compileSchema(t)

	good := map[string]any{
		"ci": map[string]any{"config": map[string]any{
			"trunk_branch":           "main",
			"allow_breaking_changes": true,
		}},
	}
	if err := sch.Validate(toJSONValue(t, good)); err != nil {
		t.Fatalf("allow_breaking_changes must validate: %v", err)
	}

	bad := map[string]any{
		"ci": map[string]any{"config": map[string]any{
			"trunk_branch":           "main",
			"allow_breaking_changes": "yes",
		}},
	}
	if err := sch.Validate(toJSONValue(t, bad)); err == nil {
		t.Fatalf("a non-boolean allow_breaking_changes must be rejected")
	}
}

// TestSchema_AcceptsReconcile proves the config-level reconcile block is
// declared, so a manifest that opts in to the emitted pin-reconcile companion
// validates under the config object's additionalProperties: false. Source and
// commit are constrained to their known adapter and routing values.
func TestSchema_AcceptsReconcile(t *testing.T) {
	sch := compileSchema(t)

	good := map[string]any{
		"ci": map[string]any{"config": map[string]any{
			"trunk_branch": "main",
			"reconcile": map[string]any{
				"enabled": true,
				"source":  "dependabot",
				"commit":  "followup",
			},
		}},
	}
	if err := sch.Validate(toJSONValue(t, good)); err != nil {
		t.Fatalf("reconcile block must validate: %v", err)
	}

	minimal := map[string]any{
		"ci": map[string]any{"config": map[string]any{
			"trunk_branch": "main",
			"reconcile":    map[string]any{"enabled": true},
		}},
	}
	if err := sch.Validate(toJSONValue(t, minimal)); err != nil {
		t.Fatalf("minimal reconcile block must validate: %v", err)
	}

	badSource := map[string]any{
		"ci": map[string]any{"config": map[string]any{
			"trunk_branch": "main",
			"reconcile":    map[string]any{"source": "renovate"},
		}},
	}
	if err := sch.Validate(toJSONValue(t, badSource)); err == nil {
		t.Fatalf("an unknown reconcile source must be rejected")
	}

	badKey := map[string]any{
		"ci": map[string]any{"config": map[string]any{
			"trunk_branch": "main",
			"reconcile":    map[string]any{"nope": true},
		}},
	}
	if err := sch.Validate(toJSONValue(t, badKey)); err == nil {
		t.Fatalf("an unknown reconcile property must be rejected")
	}
}

package generate

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
)

// ---------------------------------------------------------------------------
// T1: emitted-field enumeration guard.
//
// Every string-carrying manifest field is a potential splice into an emitted
// GitHub Actions context (YAML key/scalar, shell, github-script JS, cron,
// argv, ${{ }} expression). This guard walks the manifest schema structs by
// reflection and forces every such field into exactly one of two buckets:
//
//   - emittedFieldRegistry: the field reaches an emit sink and names the shape
//     its validator enforces (the shape keys into the adversarial battery the
//     T2 round-trip test drives through config.Validate).
//   - notEmittedAllowlist: the field does not reach an emit sink as a raw
//     splice, with the reason recorded (state-only, reserved, lookup key,
//     env-routed JSON, enum selector, and so on).
//
// A NEW field that is in neither bucket fails this test, forcing the author to
// classify it (and, when it is emitted, to give it a validator plus battery
// coverage). Removing a field, or a registry/allowlist entry, fails the test
// symmetrically, so the census cannot rot.
//
// Boundary (stated honestly): the walk covers fields reachable by reflection
// over the yaml/json tags of config.TrunkConfig. It does NOT prove anything
// about values the generator synthesizes from non-string fields, about a sink
// added for an already-registered field with a weaker shape than recorded, or
// about structs the walk cannot reach. The registry/allowlist is the
// human-audited seam, the same bounded guarantee as the docs-version-sync and
// changelog guards: it closes the known class and forces classification of new
// fields; it is not a proof of total absence.
// ---------------------------------------------------------------------------

// fieldShape names the sink-shape category a registered emitted field
// satisfies. Each shape maps to a hostile/good battery in shapeBatteries.
type fieldShape string

const (
	shapeJobIDName      fieldShape = "job-id-name"        // GHA job-ID grammar (names keying job IDs / outputs)
	shapeCallbackPath   fieldShape = "callback-path"      // uses: workflow path grammar (local)
	shapeExternalWfPath fieldShape = "external-wf-path"   // cross-repo uses: path(@ref) grammar
	shapeCron           fieldShape = "cron"               // five-field GHA cron
	shapeEventType      fieldShape = "event-type"         // unquoted YAML event-type list entry
	shapeWorkflowName   fieldShape = "workflow-name"      // single-line free prose, single-quote-escaped at emit
	shapePathPattern    fieldShape = "path-pattern"       // paths-filter glob, single-quote-escaped at emit
	shapeTagPrefix      fieldShape = "tag-prefix"         // tag grammar charset + no leading hyphen (argv)
	shapeTagToken       fieldShape = "tag-token"          // tag grammar charset (token components)
	shapeGHASecretName  fieldShape = "gha-secret-name"    // ${{ secrets.<name> }} splice / raw secrets: key
	shapeSecretRef      fieldShape = "secret-ref"         // secret reference (name or wrapped expression)
	shapeTokenExpr      fieldShape = "token-expr"         // token expression into unquoted YAML scalar
	shapeShellDQ        fieldShape = "shell-dq"           // double-quoted shell splice
	shapeJSSingleQuote  fieldShape = "js-single-quote"    // single-quoted github-script literal
	shapeEnvironmentURL fieldShape = "environment-url"    // http(s) URL into single-quoted shell assignment
	shapeGitRef         fieldShape = "git-ref"            // branch/ref name (YAML flow seq + JS + shell + argv)
	shapeRepoSlug       fieldShape = "repo-slug"          // owner/repo splice
	shapeManifestKey    fieldShape = "manifest-key"       // raw YAML scalar + shell splice
	shapeManifestPath   fieldShape = "manifest-path"      // shell-dq path splice
	shapeActionPin      fieldShape = "action-pin"         // uses: ref splice
	shapeActionFolder   fieldShape = "action-folder"      // filesystem path component + uses: path
	shapeArtifactName   fieldShape = "artifact-name"      // artifact name in emitted shell
	shapeArtifactPath   fieldShape = "artifact-path"      // unquoted glob in emitted shell / with: path
	shapeDispatchOption fieldShape = "dispatch-option"    // unquoted YAML sequence item
	shapeDispatchValue  fieldShape = "dispatch-value"     // single-line free scalar (yamlSingleQuote at emit)
	shapeWithValue      fieldShape = "with-value"         // operator input value (yamlSingleQuote at emit)
	shapeInputKey       fieldShape = "input-key"          // with: YAML key / promote JSON key
	shapeMatrixKey      fieldShape = "matrix-key"         // strategy.matrix key + ${{ matrix.<k> }} deref
	shapeCommitSHA      fieldShape = "commit-sha"         // 40-hex SHA into setup-cli ref
	shapeCLIVersion     fieldShape = "cli-version"        // setup-cli@<ref> splice
	shapeYAMLPlain      fieldShape = "yaml-plain"         // unquoted YAML scalar (expressions verbatim)
	shapeReleaseTagRef  fieldShape = "release-tag-ref"    // callback.output -> ${{ needs.<job>.outputs.<out> }}
	shapeComponentPath  fieldShape = "component-path"     // component subtree path -> paths-filter globs
)

// walkStringFields enumerates every string-carrying manifest field path under
// t. Slices of structs append "[]", maps of structs append ".*" (and register
// a synthetic "<path>[key]" entry for the map key itself). Fields tagged
// yaml:"-"/json:"-" or captured by inline catch-all maps are skipped: they are
// rejected by the strictness walk, never emitted. An exported field with no
// yaml/json tag at all is recorded in untagged (keyed "Struct.Field"):
// yaml.v3 decodes such a field via its implicit lowercased name, in
// preference to any inline catch-all, so it is manifest-reachable yet
// invisible to both this census and the strictness walk; the census fails on
// it rather than assuming it unreachable.
func walkStringFields(t reflect.Type, prefix string, stack []reflect.Type, out, untagged map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for _, anc := range stack {
		if anc == t {
			return // cycle guard
		}
	}
	stack = append(stack, t)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, inline, skip, noTag := fieldTagName(f)
		if noTag {
			untagged[t.Name()+"."+f.Name] = true
		}
		if skip {
			continue
		}
		path := prefix + "." + name
		if prefix == "" {
			path = name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if inline {
			if ft.Kind() == reflect.Struct {
				walkStringFields(ft, prefix, stack, out, untagged)
			}
			// Inline maps are the unknown-key catch-alls; the strictness walk
			// rejects any content, so they carry no emitted values.
			continue
		}
		switch ft.Kind() {
		case reflect.String:
			out[path] = true
		case reflect.Interface:
			out[path] = true
		case reflect.Struct:
			walkStringFields(ft, path, stack, out, untagged)
		case reflect.Slice:
			et := ft.Elem()
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			switch et.Kind() {
			case reflect.String, reflect.Interface:
				out[path+"[]"] = true
			case reflect.Struct:
				walkStringFields(et, path+"[]", stack, out, untagged)
			}
		case reflect.Map:
			out[path+"[key]"] = true
			vt := ft.Elem()
			for vt.Kind() == reflect.Pointer {
				vt = vt.Elem()
			}
			switch vt.Kind() {
			case reflect.String, reflect.Interface:
				out[path+".*"] = true
			case reflect.Struct:
				walkStringFields(vt, path+".*", stack, out, untagged)
			case reflect.Map, reflect.Slice:
				// map of maps / map of slices of scalars (env_inputs): the
				// values are data payloads; record the path once.
				out[path+".*"] = true
			}
		}
	}
}

// fieldTagName resolves the manifest-facing name of a struct field from its
// yaml tag, falling back to the json tag for union types whose custom
// UnmarshalYAML decodes json-tagged fields (SecretsConfig, RunsOn). An
// exported field with no tag at all is flagged untagged rather than skipped:
// yaml.v3 still decodes it via its implicit lowercased name, so treating it
// as unreachable would let it escape the census (see
// TestYAMLV3_UntaggedExportedFieldDecodes).
func fieldTagName(f reflect.StructField) (name string, inline, skip, untagged bool) {
	tag := f.Tag.Get("yaml")
	if tag == "" {
		tag = f.Tag.Get("json")
	}
	if tag == "" {
		return "", false, true, true
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, p := range parts[1:] {
		if p == "inline" {
			return "", true, false, false
		}
	}
	if name == "-" || name == "" {
		// Explicitly ignored (yaml:"-"): genuinely not decodable; an
		// unknown key with this name lands in the inline catch-all and is
		// rejected by the strictness walk.
		return "", false, true, false
	}
	return name, false, false, false
}

// classifiedPath resolves a walked field path to its registry/allowlist key.
// A component-scope twin (components.*.<path>) canonicalizes to its top-level
// path when that path is classified: config.Validate re-validates every
// resolved component with the same rules, so the classification genuinely
// covers both scopes. Component-only fields keep their own entries.
func classifiedPath(path string) string {
	if base, ok := strings.CutPrefix(path, "components.*."); ok {
		if _, reg := emittedFieldRegistry[base]; reg {
			return base
		}
		if _, allow := notEmittedAllowlist[base]; allow {
			return base
		}
	}
	return path
}

// untaggedGuardFixture carries one field of each tag classification so the
// walk's handling of all three is pinned: tagged (walked into the census),
// yaml:"-" (skipped: yaml.v3 never decodes it, the key lands in the inline
// catch-all and strictness rejects it), and untagged exported (must be
// reported as a census failure: yaml.v3 decodes it via the implicit
// lowercased field name, ahead of the inline catch-all, so it is
// manifest-reachable yet invisible to strictness and shape validation).
type untaggedGuardFixture struct {
	Nested untaggedGuardNested `yaml:"nested"`
}

type untaggedGuardNested struct {
	Tagged   string `yaml:"tagged"`
	Ignored  string `yaml:"-"`
	Untagged string
}

// TestYAMLV3_UntaggedExportedFieldDecodes pins the yaml.v3 semantics the
// untagged-field census failure exists for. If a yaml.v3 upgrade ever changes
// this behavior, this test flags that the census rule can be revisited.
func TestYAMLV3_UntaggedExportedFieldDecodes(t *testing.T) {
	type probe struct {
		Tagged   string                 `yaml:"tagged"`
		Untagged string
		Ignored  string                 `yaml:"-"`
		Extra    map[string]interface{} `yaml:",inline"`
	}
	var v probe
	require.NoError(t, yaml.Unmarshal([]byte("tagged: a\nuntagged: b\nignored: c\n"), &v))
	require.Equal(t, "a", v.Tagged)
	require.Equal(t, "b", v.Untagged,
		"yaml.v3 must decode an untagged exported field via its implicit lowercased name")
	require.NotContains(t, v.Extra, "untagged",
		"the untagged field must win over the inline catch-all, which is why strictness cannot reject it")
	require.Empty(t, v.Ignored, "a yaml:\"-\" field must never decode")
	require.Equal(t, "c", v.Extra["ignored"],
		"a yaml:\"-\" key must land in the inline catch-all, where strictness rejects it")
}

// TestWalkStringFields_ReportsUntaggedExportedFields proves the census walk
// reports an untagged exported field as a failure instead of silently
// skipping it, while still walking tagged fields and skipping yaml:"-" ones.
func TestWalkStringFields_ReportsUntaggedExportedFields(t *testing.T) {
	found := map[string]bool{}
	untaggedFields := map[string]bool{}
	walkStringFields(reflect.TypeOf(untaggedGuardFixture{}), "", nil, found, untaggedFields)

	require.Equal(t, map[string]bool{"nested.tagged": true}, found,
		"only the tagged field belongs in the census walk")
	require.Equal(t, map[string]bool{"untaggedGuardNested.Untagged": true}, untaggedFields,
		"the untagged exported field must be reported, and the yaml:\"-\" field must not be")
}

func TestEmittedFieldRegistry_EveryFieldClassified(t *testing.T) {
	found := map[string]bool{}
	untaggedFields := map[string]bool{}
	walkStringFields(reflect.TypeOf(config.TrunkConfig{}), "", nil, found, untaggedFields)

	if len(untaggedFields) > 0 {
		var list []string
		for f := range untaggedFields {
			list = append(list, f)
		}
		sort.Strings(list)
		t.Errorf("exported manifest-struct fields with no yaml/json tag (yaml.v3 decodes these via the "+
			"implicit lowercased field name, ahead of the inline catch-all, so they bypass both this census "+
			"and manifest strictness): add a yaml tag and classify the field in emittedFieldRegistry or "+
			"notEmittedAllowlist, or mark it yaml:\"-\" if it must never decode from a manifest:\n  %s",
			strings.Join(list, "\n  "))
	}

	classified := map[string]bool{}
	var unclassified, stale, both []string
	for path := range found {
		key := classifiedPath(path)
		classified[key] = true
		_, inReg := emittedFieldRegistry[key]
		_, inAllow := notEmittedAllowlist[key]
		if inReg && inAllow {
			both = append(both, key)
		}
		if !inReg && !inAllow {
			unclassified = append(unclassified, path)
		}
	}
	for path := range emittedFieldRegistry {
		if !classified[path] {
			stale = append(stale, path)
		}
	}
	for path := range notEmittedAllowlist {
		if !classified[path] {
			stale = append(stale, path)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)
	sort.Strings(both)

	if len(unclassified) > 0 {
		t.Errorf("unclassified manifest fields (add each to emittedFieldRegistry with a shape validator, "+
			"or to notEmittedAllowlist with the reason it is not spliced into emitted output):\n  %s",
			strings.Join(unclassified, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("registry/allowlist entries no longer present in the schema (remove them):\n  %s",
			strings.Join(stale, "\n  "))
	}
	if len(both) > 0 {
		t.Errorf("fields classified as both emitted and not-emitted:\n  %s", strings.Join(both, "\n  "))
	}
}

// TestEmittedFieldRegistry_EveryEntryHasBattery forces every registry shape to
// carry an adversarial battery and every registry entry to carry a mutator, so
// a new registry entry cannot skip the T2 round-trip.
func TestEmittedFieldRegistry_EveryEntryHasBattery(t *testing.T) {
	for path, entry := range emittedFieldRegistry {
		if _, ok := shapeBatteries[entry.shape]; !ok {
			t.Errorf("registry entry %s names shape %q with no battery in shapeBatteries", path, entry.shape)
		}
		if entry.set == nil {
			t.Errorf("registry entry %s has no mutator; the T2 round-trip cannot exercise it", path)
		}
	}
}

// registryEntry ties an emitted field to the shape its validator enforces and
// a mutator that plants a value at that field on a fresh base manifest. An
// optional good value overrides the shape battery's default when the field
// needs a specific legitimate value (a workflow file that exists, a token that
// must differ from a default).
type registryEntry struct {
	shape fieldShape
	set   func(cfg *config.TrunkConfig, v string)
	good  string
}

// shapeBatteries maps each shape to the hostile values its validator must
// reject and a good value that must survive validate + generate + re-parse.
var shapeBatteries = map[fieldShape]struct {
	hostile []string
	good    string
}{
	shapeJobIDName:      {hostile: []string{"bad name", "a.b", "x\ny"}, good: "app-x1"},
	shapeCallbackPath:   {hostile: []string{"../x.yaml", "a b.yaml", "x\ny.yaml"}, good: "build.yaml"},
	shapeExternalWfPath: {hostile: []string{"org/repo/.github/x y.yaml", "a@b@c", "x\ny"}, good: "org/repo/.github/workflows/deploy.yaml@v1"},
	shapeCron:           {hostile: []string{"0 2 * *", "0 2 * * * *", "$(id) * * * *", "0 2 * * *\n- x", "0 2 * * *'"}, good: "0 2 * * 1-5"},
	shapeEventType:      {hostile: []string{"deploy: now", "a b", "#x", "x\ny"}, good: "external-update"},
	shapeWorkflowName:   {hostile: []string{"a\nb", "  "}, good: "Bob's CI"},
	shapePathPattern:    {hostile: []string{"a\nb", "   "}, good: "src/it's/**"},
	shapeTagPrefix:      {hostile: []string{"-rc", "a'b", "a b"}, good: "api-v"},
	shapeTagToken:       {hostile: []string{"a'b", "a b", "a$b"}, good: "beta"},
	shapeGHASecretName:  {hostile: []string{"1KEY", "A-B", "A B", "A }} B"}, good: "MY_SECRET_1"},
	shapeSecretRef:      {hostile: []string{"BAD NAME!", "x\ny"}, good: "${{ secrets.APP_KEY }}"},
	shapeTokenExpr:      {hostile: []string{"SEC\nRET", "a: b", "x #y"}, good: "${{ secrets.MY_TOKEN }}"},
	shapeShellDQ:        {hostile: []string{`x"y`, "x$y", "x`y", `x\y`, "x\ny"}, good: "O'Brien [bot]"},
	shapeJSSingleQuote:  {hostile: []string{"x'y", `x\y`, "x\ny"}, good: "backend-svc"},
	shapeEnvironmentURL: {hostile: []string{"ftp://x", "https://x/a'b", "https://x/a b"}, good: "https://app.example.com/?$top=1"},
	shapeGitRef:         {hostile: []string{"a b", "x$y", "-main", "x\ny"}, good: "release/main"},
	shapeRepoSlug:       {hostile: []string{"orgonly", "org repo", "org/repo/extra", "x\ny"}, good: "org/primary"},
	shapeManifestKey:    {hostile: []string{"ci key", "ci:x", "ci'"}, good: "ci"},
	shapeManifestPath:   {hostile: []string{`x"y`, "x$y", "x\ny"}, good: ".github/manifest.yaml"},
	shapeActionPin:      {hostile: []string{"ref with spaces", "x\ny", "$(id)"}, good: "0123abcd # v4"},
	shapeActionFolder:   {hostile: []string{"../x", "a/b", "a b"}, good: "manage-release-x"},
	shapeArtifactName:   {hostile: []string{"a b", "a$b", "x\ny"}, good: "checksums.txt"},
	shapeArtifactPath:   {hostile: []string{"a b", "-dist", "$(id)"}, good: "dist/**"},
	shapeDispatchOption: {hostile: []string{"a b", "a:b", "'x"}, good: "v1.2.3"},
	shapeDispatchValue:  {hostile: []string{"a\nb"}, good: "it's fine"},
	shapeWithValue:      {hostile: []string{"a\nb"}, good: `it's {"a": 1}`},
	shapeInputKey:       {hostile: []string{"1bad", "a b", "a:b"}, good: "cluster"},
	shapeMatrixKey:      {hostile: []string{"1bad", "a b", "a:b"}, good: "go-version"},
	shapeCommitSHA:      {hostile: []string{"abc", strings.Repeat("g", 40), strings.Repeat("A", 40)}, good: strings.Repeat("a", 40)},
	shapeCLIVersion:     {hostile: []string{"v1 2", "v1\n2", "$(id)"}, good: "v1.2.3"},
	shapeYAMLPlain:      {hostile: []string{"a: b", "a #b", "a\nb"}, good: "lane-${{ github.ref }}"},
	shapeReleaseTagRef:  {hostile: []string{"app.o ut", "noformat", "app.o'ut"}, good: "app.tag"},
	shapeComponentPath:  {hostile: []string{"/abs", "srv/../x", "a\nb"}, good: "services/api"},
}

// componentSet wraps a component-only field mutation in a minimal valid
// component so per-component paths can be exercised with one mutation.
func componentSet(f func(c *config.ComponentConfig, v string)) func(*config.TrunkConfig, string) {
	return func(cfg *config.TrunkConfig, v string) {
		comp := config.ComponentConfig{
			Path:       "services/api",
			TagGrammar: &config.TagGrammarConfig{Prefix: strptr("api-v")},
		}
		f(&comp, v)
		cfg.Components = map[string]config.ComponentConfig{"api": comp}
	}
}

// Mutator helpers over the base fixture (one build "app", one deploy "runner").
func setBuild(f func(b *config.BuildConfig, v string)) func(*config.TrunkConfig, string) {
	return func(cfg *config.TrunkConfig, v string) { f(&cfg.Builds[0], v) }
}

func setDeploy(f func(d *config.DeployConfig, v string)) func(*config.TrunkConfig, string) {
	return func(cfg *config.TrunkConfig, v string) { f(&cfg.Deploys[0], v) }
}

func setValidateCB(f func(vc *config.ValidateConfig, v string)) func(*config.TrunkConfig, string) {
	return func(cfg *config.TrunkConfig, v string) {
		if cfg.Validate == nil {
			cfg.Validate = &config.ValidateConfig{Workflow: "validate.yaml"}
		}
		f(cfg.Validate, v)
	}
}

func setExternalDeploy(f func(d *config.ExternalDeployConfig, v string)) func(*config.TrunkConfig, string) {
	return func(cfg *config.TrunkConfig, v string) {
		ext := config.ExternalRepoConfig{
			Repo: "org/satellite",
			Ref:  "main",
			Deploys: []config.ExternalDeployConfig{{
				Name:     "ext-app",
				Workflow: "org/satellite/.github/workflows/deploy.yaml@v1",
			}},
		}
		f(&ext.Deploys[0], v)
		cfg.External = []config.ExternalRepoConfig{ext}
	}
}

func setGit(f func(g *config.GitConfig, v string)) func(*config.TrunkConfig, string) {
	return func(cfg *config.TrunkConfig, v string) {
		if cfg.Git == nil {
			cfg.Git = &config.GitConfig{}
		}
		f(cfg.Git, v)
	}
}

func setNotify(f func(n *config.NotifyConfig, v string)) func(*config.TrunkConfig, string) {
	return func(cfg *config.TrunkConfig, v string) {
		if cfg.Notify == nil {
			cfg.Notify = &config.NotifyConfig{Repo: "org/primary"}
		}
		f(cfg.Notify, v)
	}
}

// emittedFieldRegistry is the census of manifest fields that reach an emit
// sink. Component-scope twins (components.*.<path>) inherit the top-level
// classification: config.Validate re-validates every resolved component with
// the same rules, so one entry covers both scopes. Component-only fields are
// listed explicitly.
var emittedFieldRegistry = map[string]registryEntry{
	"action_folder": {shape: shapeActionFolder, set: func(c *config.TrunkConfig, v string) { c.ActionFolder = v }},
	"action_pins.*": {shape: shapeActionPin, set: func(c *config.TrunkConfig, v string) {
		c.ActionPins = map[string]string{"actions/checkout": v}
	}},

	"builds[].artifacts[].name": {shape: shapeArtifactName, set: setBuild(func(b *config.BuildConfig, v string) {
		b.Artifacts = []config.ArtifactConfig{{Name: v, Path: "dist/**"}}
	})},
	"builds[].artifacts[].path": {shape: shapeArtifactPath, set: setBuild(func(b *config.BuildConfig, v string) {
		b.Artifacts = []config.ArtifactConfig{{Name: "bundle", Path: v}}
	})},
	"builds[].artifact_upload.upload": {shape: shapeArtifactPath, set: setBuild(func(b *config.BuildConfig, v string) {
		b.PassthroughArtifact = &config.PassthroughArtifact{Upload: v}
	})},
	"builds[].artifact_upload.downloads[]": {shape: shapeJobIDName, good: "app", set: setBuild(func(b *config.BuildConfig, v string) {
		b.PassthroughArtifact = &config.PassthroughArtifact{Downloads: []string{v}}
	})},
	"builds[].inputs[key]": {shape: shapeInputKey, set: setBuild(func(b *config.BuildConfig, v string) {
		b.Inputs = map[string]interface{}{v: "x"}
	})},
	"builds[].inputs.*": {shape: shapeWithValue, set: setBuild(func(b *config.BuildConfig, v string) {
		b.Inputs = map[string]interface{}{"cluster": v}
	})},
	"builds[].matrix.dimensions[key]": {shape: shapeMatrixKey, set: setBuild(func(b *config.BuildConfig, v string) {
		b.Matrix = &config.MatrixConfig{Dimensions: map[string][]string{v: {"1"}}}
	})},
	"builds[].name": {shape: shapeJobIDName, set: setBuild(func(b *config.BuildConfig, v string) { b.Name = v })},
	"builds[].secrets.map[key]": {shape: shapeGHASecretName, set: setBuild(func(b *config.BuildConfig, v string) {
		b.Secrets = &config.SecretsConfig{Map: map[string]string{v: "GOOD_OUT"}}
	})},
	"builds[].secrets.map.*": {shape: shapeGHASecretName, set: setBuild(func(b *config.BuildConfig, v string) {
		b.Secrets = &config.SecretsConfig{Map: map[string]string{"GOOD_IN": v}}
	})},
	"builds[].triggers[]": {shape: shapePathPattern, set: setBuild(func(b *config.BuildConfig, v string) { b.Triggers = []string{v} })},
	"builds[].workflow":   {shape: shapeCallbackPath, set: setBuild(func(b *config.BuildConfig, v string) { b.Workflow = v })},

	"changelog.workflow": {shape: shapeCallbackPath, good: "changelog.yaml", set: func(c *config.TrunkConfig, v string) {
		c.Changelog = &config.ChangelogConfig{Workflow: v}
	}},
	"cli_version":     {shape: shapeCLIVersion, set: func(c *config.TrunkConfig, v string) { c.CLIVersion = v }},
	"cli_version_sha": {shape: shapeCommitSHA, set: func(c *config.TrunkConfig, v string) { c.CLIVersionSHA = v }},

	"components[key]": {shape: shapeJobIDName, good: "api", set: func(c *config.TrunkConfig, v string) {
		c.Components = map[string]config.ComponentConfig{v: {
			Path:       "services/api",
			TagGrammar: &config.TagGrammarConfig{Prefix: strptr("api-v")},
		}}
	}},
	"components.*.path": {shape: shapeComponentPath, set: componentSet(func(cc *config.ComponentConfig, v string) { cc.Path = v })},
	"components.*.extra_paths[]": {shape: shapePathPattern, set: componentSet(func(cc *config.ComponentConfig, v string) {
		cc.ExtraPaths = []string{v}
	})},

	"concurrency.group": {shape: shapeYAMLPlain, set: func(c *config.TrunkConfig, v string) {
		c.Concurrency = &config.ConcurrencyConfig{Group: v, CancelInProgress: true}
	}},

	"deploys[].artifact_upload.upload": {shape: shapeArtifactPath, set: setDeploy(func(d *config.DeployConfig, v string) {
		d.PassthroughArtifact = &config.PassthroughArtifact{Upload: v}
	})},
	"deploys[].artifact_upload.downloads[]": {shape: shapeJobIDName, good: "app", set: setDeploy(func(d *config.DeployConfig, v string) {
		d.PassthroughArtifact = &config.PassthroughArtifact{Downloads: []string{v}}
	})},
	"deploys[].inputs[key]": {shape: shapeInputKey, set: setDeploy(func(d *config.DeployConfig, v string) {
		d.Inputs = map[string]interface{}{v: "x"}
	})},
	"deploys[].inputs.*": {shape: shapeWithValue, set: setDeploy(func(d *config.DeployConfig, v string) {
		d.Inputs = map[string]interface{}{"cluster": v}
	})},
	"deploys[].name": {shape: shapeJobIDName, set: setDeploy(func(d *config.DeployConfig, v string) { d.Name = v })},
	"deploys[].secrets.map[key]": {shape: shapeGHASecretName, set: setDeploy(func(d *config.DeployConfig, v string) {
		d.Secrets = &config.SecretsConfig{Map: map[string]string{v: "GOOD_OUT"}}
	})},
	"deploys[].secrets.map.*": {shape: shapeGHASecretName, set: setDeploy(func(d *config.DeployConfig, v string) {
		d.Secrets = &config.SecretsConfig{Map: map[string]string{"GOOD_IN": v}}
	})},
	"deploys[].triggers[]": {shape: shapePathPattern, set: setDeploy(func(d *config.DeployConfig, v string) { d.Triggers = []string{v} })},
	"deploys[].workflow":   {shape: shapeCallbackPath, good: "deploy.yaml", set: setDeploy(func(d *config.DeployConfig, v string) { d.Workflow = v })},

	"dispatch_inputs[key]": {shape: shapeJobIDName, good: "mode", set: func(c *config.TrunkConfig, v string) {
		c.DispatchInputs = map[string]config.DispatchInput{v: {Type: config.DispatchInputTypeString}}
	}},
	"dispatch_inputs.*.default": {shape: shapeDispatchValue, set: func(c *config.TrunkConfig, v string) {
		c.DispatchInputs = map[string]config.DispatchInput{"note": {Type: config.DispatchInputTypeString, Default: v}}
	}},
	"dispatch_inputs.*.options[]": {shape: shapeDispatchOption, set: func(c *config.TrunkConfig, v string) {
		c.DispatchInputs = map[string]config.DispatchInput{"mode": {Type: config.DispatchInputTypeChoice, Options: []string{v}}}
	}},

	"environments[].name": {shape: shapeJobIDName, good: "stage-x1", set: func(c *config.TrunkConfig, v string) {
		c.Environments = config.EnvNames("dev", v)
	}},
	"environments[].environment_url": {shape: shapeEnvironmentURL, set: func(c *config.TrunkConfig, v string) {
		c.Deployments = &config.DeploymentsConfig{Enabled: boolPtr(true)}
		c.Environments = []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{EnvironmentURL: v}},
		}
	}},
	"environments[].secrets[]": {shape: shapeGHASecretName, set: func(c *config.TrunkConfig, v string) {
		c.Environments = []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{Secrets: []string{v}}},
		}
	}},
	"environments[].variables[]": {shape: shapeGHASecretName, set: func(c *config.TrunkConfig, v string) {
		c.Environments = []config.EnvironmentEntry{
			{Name: "dev"},
			{Name: "prod", EnvironmentConfig: config.EnvironmentConfig{Variables: []string{v}}},
		}
	}},

	"external[].ref": {shape: shapeGitRef, good: "v1.2.3", set: func(c *config.TrunkConfig, v string) {
		c.External = []config.ExternalRepoConfig{{
			Repo: "org/satellite",
			Ref:  v,
			Deploys: []config.ExternalDeployConfig{{
				Name:     "ext-app",
				Workflow: "org/satellite/.github/workflows/deploy.yaml",
			}},
		}}
	}},
	"external[].deploys[].name": {shape: shapeJobIDName, set: setExternalDeploy(func(d *config.ExternalDeployConfig, v string) { d.Name = v })},
	"external[].deploys[].workflow": {shape: shapeExternalWfPath, set: setExternalDeploy(func(d *config.ExternalDeployConfig, v string) {
		d.Workflow = v
	})},
	"external[].deploys[].on_update.deploy.workflow": {shape: shapeExternalWfPath, set: setExternalDeploy(func(d *config.ExternalDeployConfig, v string) {
		d.OnUpdate = &config.OnUpdateConfig{Deploy: &config.OnUpdateDeploy{Workflow: v}}
	})},
	"external[].deploys[].triggers[]": {shape: shapePathPattern, set: setExternalDeploy(func(d *config.ExternalDeployConfig, v string) {
		d.Triggers = []string{v}
	})},
	"external[].deploys[].secrets.map[key]": {shape: shapeGHASecretName, set: setExternalDeploy(func(d *config.ExternalDeployConfig, v string) {
		d.Secrets = &config.SecretsConfig{Map: map[string]string{v: "GOOD_OUT"}}
	})},
	"external[].deploys[].secrets.map.*": {shape: shapeGHASecretName, set: setExternalDeploy(func(d *config.ExternalDeployConfig, v string) {
		d.Secrets = &config.SecretsConfig{Map: map[string]string{"GOOD_IN": v}}
	})},

	"extra_triggers.schedule[].cron": {shape: shapeCron, set: func(c *config.TrunkConfig, v string) {
		c.ExtraTriggers = &config.ExtraTriggers{Schedule: []config.ScheduleEntry{{Cron: v}}}
	}},
	"extra_triggers.repository_dispatch.types[]": {shape: shapeEventType, set: func(c *config.TrunkConfig, v string) {
		c.ExtraTriggers = &config.ExtraTriggers{RepositoryDispatch: &config.RepositoryDispatchTrigger{Types: []string{v}}}
	}},
	"extra_triggers.workflow_run.types[]": {shape: shapeEventType, good: "completed", set: func(c *config.TrunkConfig, v string) {
		c.ExtraTriggers = &config.ExtraTriggers{WorkflowRun: &config.WorkflowRunTrigger{Workflows: []string{"Upstream CI"}, Types: []string{v}}}
	}},
	"extra_triggers.workflow_run.workflows[]": {shape: shapeWorkflowName, set: func(c *config.TrunkConfig, v string) {
		c.ExtraTriggers = &config.ExtraTriggers{WorkflowRun: &config.WorkflowRunTrigger{Workflows: []string{v}, Types: []string{"completed"}}}
	}},

	"git.user_name":  {shape: shapeShellDQ, set: setGit(func(g *config.GitConfig, v string) { g.Mode = config.GitModeCustom; g.UserName = v; g.UserEmail = "bot@example.com" })},
	"git.user_email": {shape: shapeShellDQ, good: "o'brien@example.com", set: setGit(func(g *config.GitConfig, v string) { g.Mode = config.GitModeCustom; g.UserName = "Release Bot"; g.UserEmail = v })},
	"git.gpg_key_id": {shape: shapeGHASecretName, set: setGit(func(g *config.GitConfig, v string) {
		g.GPGKeyID = v
		g.GPGKeySecret = "GPG_PRIVATE_KEY"
	})},
	"git.gpg_key_secret": {shape: shapeGHASecretName, set: setGit(func(g *config.GitConfig, v string) {
		g.GPGKeyID = "GPG_KEY_ID"
		g.GPGKeySecret = v
	})},

	"manifest_file": {shape: shapeManifestPath, set: func(c *config.TrunkConfig, v string) { c.ManifestFile = v }},
	"manifest_key":  {shape: shapeManifestKey, set: func(c *config.TrunkConfig, v string) { c.ManifestKey = v }},

	"notify.repo":        {shape: shapeRepoSlug, set: setNotify(func(n *config.NotifyConfig, v string) { n.Repo = v })},
	"notify.workflow":    {shape: shapeJSSingleQuote, good: "external-update.yaml", set: setNotify(func(n *config.NotifyConfig, v string) { n.Workflow = v })},
	"notify.deploy_name": {shape: shapeJSSingleQuote, set: setNotify(func(n *config.NotifyConfig, v string) { n.DeployName = v })},
	"notify.environment": {shape: shapeJSSingleQuote, good: "dev", set: setNotify(func(n *config.NotifyConfig, v string) { n.Environment = v })},
	"notify.token":       {shape: shapeTokenExpr, set: setNotify(func(n *config.NotifyConfig, v string) { n.Token = v })},

	"publish.workflow": {shape: shapeCallbackPath, good: "publish.yaml", set: func(c *config.TrunkConfig, v string) {
		c.Publish = &config.PublishConfig{Workflow: v}
	}},
	"release_build.workflow": {shape: shapeCallbackPath, good: "release.yaml", set: func(c *config.TrunkConfig, v string) {
		c.ReleaseBuild = &config.ReleaseBuildConfig{Workflow: v}
	}},
	"release_build.tag": {shape: shapeReleaseTagRef, set: func(c *config.TrunkConfig, v string) {
		c.ReleaseBuild = &config.ReleaseBuildConfig{Workflow: "release.yaml", Tag: v}
	}},

	"release_token": {shape: shapeTokenExpr, set: func(c *config.TrunkConfig, v string) { c.ReleaseToken = v }},
	"state_token":   {shape: shapeTokenExpr, set: func(c *config.TrunkConfig, v string) { c.StateToken = v }},
	"release_token_app.app_id": {shape: shapeSecretRef, set: func(c *config.TrunkConfig, v string) {
		c.ReleaseTokenApp = &config.AppTokenSource{AppID: v, PrivateKey: "${{ secrets.APP_KEY }}"}
	}},
	"release_token_app.private_key": {shape: shapeSecretRef, set: func(c *config.TrunkConfig, v string) {
		c.ReleaseTokenApp = &config.AppTokenSource{AppID: "${{ vars.APP_ID }}", PrivateKey: v}
	}},
	"state_token_app.app_id": {shape: shapeSecretRef, set: func(c *config.TrunkConfig, v string) {
		c.StateTokenApp = &config.AppTokenSource{AppID: v, PrivateKey: "${{ secrets.APP_KEY }}"}
	}},
	"state_token_app.private_key": {shape: shapeSecretRef, set: func(c *config.TrunkConfig, v string) {
		c.StateTokenApp = &config.AppTokenSource{AppID: "${{ vars.APP_ID }}", PrivateKey: v}
	}},

	"rollback.repository_dispatch.types[]": {shape: shapeEventType, set: func(c *config.TrunkConfig, v string) {
		c.Rollback = &config.RollbackConfig{RepositoryDispatch: &config.RepositoryDispatchTrigger{Types: []string{v}}}
	}},

	"shared_paths[]": {shape: shapePathPattern, set: func(c *config.TrunkConfig, v string) {
		componentSet(func(cc *config.ComponentConfig, _ string) {})(c, v)
		c.SharedPaths = []string{v}
	}},

	"tag_grammar.prefix": {shape: shapeTagPrefix, set: func(c *config.TrunkConfig, v string) {
		c.TagGrammar = &config.TagGrammarConfig{Prefix: strptr(v)}
	}},
	"tag_grammar.prerelease_token": {shape: shapeTagToken, set: func(c *config.TrunkConfig, v string) {
		c.TagGrammar = &config.TagGrammarConfig{PreReleaseToken: strptr(v)}
	}},
	"tag_grammar.prerelease_separator": {shape: shapeTagToken, good: "_", set: func(c *config.TrunkConfig, v string) {
		c.TagGrammar = &config.TagGrammarConfig{PreReleaseSeparator: strptr(v)}
	}},
	"tag_grammar.dryrun_token": {shape: shapeTagToken, good: "rehearsal", set: func(c *config.TrunkConfig, v string) {
		c.TagGrammar = &config.TagGrammarConfig{DryRunToken: strptr(v)}
	}},

	"triggers[]":   {shape: shapePathPattern, set: func(c *config.TrunkConfig, v string) { c.Triggers = []string{v} }},
	"trunk_branch": {shape: shapeGitRef, set: func(c *config.TrunkConfig, v string) { c.TrunkBranch = v }},

	"validate.workflow": {shape: shapeCallbackPath, good: "validate.yaml", set: setValidateCB(func(vc *config.ValidateConfig, v string) { vc.Workflow = v })},
	"validate.triggers[]": {shape: shapePathPattern, set: setValidateCB(func(vc *config.ValidateConfig, v string) {
		vc.Triggers = []string{v}
	})},
	"validate.inputs[key]": {shape: shapeInputKey, set: setValidateCB(func(vc *config.ValidateConfig, v string) {
		vc.Inputs = map[string]interface{}{v: "x"}
	})},
	"validate.inputs.*": {shape: shapeWithValue, set: setValidateCB(func(vc *config.ValidateConfig, v string) {
		vc.Inputs = map[string]interface{}{"cluster": v}
	})},
	"validate.secrets.map[key]": {shape: shapeGHASecretName, set: setValidateCB(func(vc *config.ValidateConfig, v string) {
		vc.Secrets = &config.SecretsConfig{Map: map[string]string{v: "GOOD_OUT"}}
	})},
	"validate.secrets.map.*": {shape: shapeGHASecretName, set: setValidateCB(func(vc *config.ValidateConfig, v string) {
		vc.Secrets = &config.SecretsConfig{Map: map[string]string{"GOOD_IN": v}}
	})},
}

// notEmittedAllowlist records every string-carrying manifest field that does
// NOT reach an emit sink as a raw splice, with the reason. Component-scope
// twins inherit the classification (see emittedFieldRegistry).
var notEmittedAllowlist = map[string]string{
	"action_pins[key]": "lookup key selecting a pinned action; unmatched keys are inert",

	"builds[].concurrency.group":   "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"builds[].runs_on.group":       "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"builds[].runs_on.label":       "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"builds[].runs_on.labels[]":    "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"builds[].depends_on[]":        "validated reference to a declared callback (ResolveDependency); needs: uses the derived job ID",
	"builds[].optional_depends_on[]": "validated reference to a declared callback (ResolveDependency)",
	"builds[].env_inputs[key]":     "validated reference to a declared environment",
	"builds[].env_inputs.*":        "env-routed JSON matrices (json.Marshal cannot smuggle raw newlines)",
	"builds[].matrix.dimensions.*": "emitted via Go %q into a flow sequence; escapes are YAML-double-quote compatible",
	"builds[].on_failure":          "validated enum (validateCallbackPolicy)",
	"builds[].run_policy":          "validated enum (validateCallbackPolicy)",
	"builds[].permissions[key]":    "validated enum (knownPermissionScopes)",
	"builds[].permissions.*":       "validated enum (validPermissionValues)",
	"builds[].run":                 "rejected by validateWorkflowRunXOR (inline callbacks removed)",
	"builds[].shell":               "rejected by validateWorkflowRunXOR (inline callbacks removed)",
	"builds[].state_tags[]":        "parsed but not consumed by generation (state metadata seam)",

	"deploys[].concurrency.group":    "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"deploys[].runs_on.group":        "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"deploys[].runs_on.label":        "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"deploys[].runs_on.labels[]":     "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"deploys[].depends_on[]":         "validated reference to a declared callback (ResolveDependency)",
	"deploys[].optional_depends_on[]": "validated reference to a declared callback (ResolveDependency)",
	"deploys[].env_inputs[key]":      "validated reference to a declared environment",
	"deploys[].env_inputs.*":         "env-routed JSON matrices (json.Marshal cannot smuggle raw newlines)",
	"deploys[].on_failure":           "validated enum (validateCallbackPolicy)",
	"deploys[].run_policy":           "validated enum (validateCallbackPolicy)",
	"deploys[].permissions[key]":     "validated enum (knownPermissionScopes)",
	"deploys[].permissions.*":        "validated enum (validPermissionValues)",
	"deploys[].run":                  "rejected by validateWorkflowRunXOR (inline callbacks removed)",
	"deploys[].shell":                "rejected by validateWorkflowRunXOR (inline callbacks removed)",
	"deploys[].state_tags[]":         "parsed but not consumed by generation (state metadata seam)",
	"deploys[].deploy_target.branch": "reserved: any deploy_target block is rejected by validateReservedFields",
	"deploys[].deploy_target.field":  "reserved: any deploy_target block is rejected by validateReservedFields",
	"deploys[].deploy_target.mode":   "reserved: any deploy_target block is rejected by validateReservedFields",
	"deploys[].deploy_target.path":   "reserved: any deploy_target block is rejected by validateReservedFields",
	"deploys[].deploy_target.repo":   "reserved: any deploy_target block is rejected by validateReservedFields",
	"deploys[].deploy_target.value":  "reserved: any deploy_target block is rejected by validateReservedFields",
	"deploys[].rollout.type":         "reserved: rejected by validateReservedFields (reservedRollout)",
	"deploys[].rollout.canary.analysis":           "reserved: rejected by validateReservedFields (reservedRollout)",
	"deploys[].rollout.canary.bake_time":          "reserved: rejected by validateReservedFields (reservedRollout)",
	"deploys[].rollout.canary.promote_callback":   "reserved: rejected by validateReservedFields (reservedRollout)",
	"deploys[].rollout.canary.rollback_callback":  "reserved: rejected by validateReservedFields (reservedRollout)",
	"deploys[].rollout.blue_green.switch":         "reserved: rejected by validateReservedFields (reservedRollout)",

	"dispatch_inputs.*.description": "emitted via Go %q; escapes are YAML-double-quote compatible",
	"dispatch_inputs.*.type":        "validated enum (dispatch input types)",

	"environments[].role":                 "validated enum (validateEnvironments)",
	"environments[].branch_policy":        "validated enum (validateEnvironmentConfig)",
	"environments[].gha_environment":      "runtime environments-provisioning API payload (JSON); generation only tests presence",
	"environments[].branch_patterns[]":    "runtime environments-provisioning API payload (JSON)",
	"environments[].tag_patterns[]":       "runtime environments-provisioning API payload (JSON)",
	"environments[].required_reviewers[]": "runtime environments-provisioning API payload (JSON); slug-validated",

	"external[].repo": "runtime identity comparison and recorded state; the emitted uses: path comes from the deploy's workflow field",
	"external[].deploys[].run":   "rejected by validateExternalDeployWorkflowOnly",
	"external[].deploys[].shell": "rejected by validateExternalDeployWorkflowOnly",
	"external[].deploys[].concurrency.group":    "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"external[].deploys[].runs_on.group":        "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"external[].deploys[].runs_on.label":        "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"external[].deploys[].runs_on.labels[]":     "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"external[].deploys[].optional_depends_on[]": "validated reference to a declared callback (ResolveDependency)",
	"external[].deploys[].permissions[key]":     "validated enum (knownPermissionScopes)",
	"external[].deploys[].permissions.*":        "validated enum (validPermissionValues)",
	"external[].deploys[].deploy_target.branch": "reserved: any deploy_target block is rejected by validateReservedFields",
	"external[].deploys[].deploy_target.field":  "reserved: any deploy_target block is rejected by validateReservedFields",
	"external[].deploys[].deploy_target.mode":   "reserved: any deploy_target block is rejected by validateReservedFields",
	"external[].deploys[].deploy_target.path":   "reserved: any deploy_target block is rejected by validateReservedFields",
	"external[].deploys[].deploy_target.repo":   "reserved: any deploy_target block is rejected by validateReservedFields",
	"external[].deploys[].deploy_target.value":  "reserved: any deploy_target block is rejected by validateReservedFields",
	"external[].deploys[].rollout.type":                "reserved: rejected by validateReservedFields (reservedRollout)",
	"external[].deploys[].rollout.canary.analysis":     "reserved: rejected by validateReservedFields (reservedRollout)",
	"external[].deploys[].rollout.canary.bake_time":    "reserved: rejected by validateReservedFields (reservedRollout)",
	"external[].deploys[].rollout.canary.promote_callback":  "reserved: rejected by validateReservedFields (reservedRollout)",
	"external[].deploys[].rollout.canary.rollback_callback": "reserved: rejected by validateReservedFields (reservedRollout)",
	"external[].deploys[].rollout.blue_green.switch":        "reserved: rejected by validateReservedFields (reservedRollout)",

	"git.mode": "behavior selector; the value itself is never spliced",

	"pin_mode":         "validated enum (tag | sha)",
	"release_trigger":  "validated enum (push | dispatch)",
	"reconcile.source": "validated enum (validateReconcile)",
	"reconcile.commit": "validated enum (validateReconcile)",

	"release_build.version_overrides.dir": "reserved: rejected by validateReservedFields",

	"runs_on.group":    "documented validated-only; never emitted (manifest.md)",
	"runs_on.label":    "documented validated-only; never emitted (manifest.md)",
	"runs_on.labels[]": "documented validated-only; never emitted (manifest.md)",

	"telemetry.adapter":             "reserved: any telemetry block is rejected by validateReservedFields",
	"telemetry.webhook.url":         "reserved block; url shape still validated for when it lands",
	"telemetry.webhook.secret_name": "reserved block; name shape still validated for when it lands",

	"validate.concurrency.group": "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"validate.runs_on.group":     "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"validate.runs_on.label":     "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"validate.runs_on.labels[]":  "rejected on reusable-workflow callbacks (validateJobControlFields)",
	"validate.env_inputs[key]":   "validated reference to a declared environment",
	"validate.env_inputs.*":      "env-routed JSON matrices (json.Marshal cannot smuggle raw newlines)",
	"validate.on_failure":        "validated enum (validateCallbackPolicy)",
	"validate.run_policy":        "validated enum (validateCallbackPolicy)",
	"validate.permissions[key]":  "validated enum (knownPermissionScopes)",
	"validate.permissions.*":     "validated enum (validPermissionValues)",
	"validate.run":               "rejected by validateWorkflowRunXOR (inline callbacks removed)",
	"validate.shell":             "rejected by validateWorkflowRunXOR (inline callbacks removed)",
}

// guardBaseConfig is the minimal valid manifest the battery mutates: two
// environments, one build, one deploy, so orchestrate and promote both emit.
func guardBaseConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: "build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "runner", Workflow: "deploy.yaml", Triggers: []string{"deploy/**"}},
		},
	}
}

// generateGuardDocs renders the emitted workflows for cfg (per resolved
// component when components are declared) and returns their contents.
func generateGuardDocs(t *testing.T, cfg *config.TrunkConfig, baseDir string) []string {
	t.Helper()
	var docs []string
	if len(cfg.Components) > 0 {
		names := make([]string, 0, len(cfg.Components))
		for name := range cfg.Components {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			resolved, err := cfg.ResolveComponent(name)
			if err != nil {
				t.Fatalf("resolving component %q: %v", name, err)
			}
			content, err := NewGenerator(resolved.Config, baseDir).Generate()
			if err != nil {
				t.Fatalf("generating orchestrate for component %q: %v", name, err)
			}
			docs = append(docs, content)
		}
		return docs
	}
	orchestrate, err := NewGenerator(cfg, baseDir).Generate()
	if err != nil {
		t.Fatalf("generating orchestrate: %v", err)
	}
	docs = append(docs, orchestrate)
	promote, err := NewPromoteGenerator(cfg, baseDir).Generate()
	if err != nil {
		t.Fatalf("generating promote: %v", err)
	}
	docs = append(docs, promote)
	if len(cfg.External) > 0 {
		external, err := NewExternalUpdateGenerator(cfg, baseDir).Generate()
		if err != nil {
			t.Fatalf("generating external-update: %v", err)
		}
		docs = append(docs, external)
	}
	return docs
}

// TestEmittedFieldBattery_HostileRejectedGoodRoundTrips is the T2 adversarial
// round-trip: for every registered emitted field, each hostile value in its
// shape battery must be REJECTED by config.Validate, and its good value must
// validate clean, generate, and re-parse as valid YAML (the same bar GitHub's
// parser applies). The battery covers only the fixed hostile set per shape:
// apostrophe, whitespace, `$`, backtick, newline, leading hyphen, colon,
// comment marker, and injection payloads as applicable; it is not a fuzzer.
func TestEmittedFieldBattery_HostileRejectedGoodRoundTrips(t *testing.T) {
	baseDir := callbackWorkflowDir(t,
		"build.yaml", "deploy.yaml", "validate.yaml", "changelog.yaml", "publish.yaml", "release.yaml")

	require.Empty(t, config.Validate(guardBaseConfig()), "the guard base fixture must validate clean")

	paths := make([]string, 0, len(emittedFieldRegistry))
	for p := range emittedFieldRegistry {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		entry := emittedFieldRegistry[path]
		battery := shapeBatteries[entry.shape]
		t.Run(path, func(t *testing.T) {
			for _, hostile := range battery.hostile {
				cfg := guardBaseConfig()
				entry.set(cfg, hostile)
				if errs := config.Validate(cfg); len(errs) == 0 {
					t.Errorf("hostile value %q was accepted by validation", hostile)
				}
			}

			good := battery.good
			if entry.good != "" {
				good = entry.good
			}
			cfg := guardBaseConfig()
			entry.set(cfg, good)
			errs := config.Validate(cfg)
			if len(errs) > 0 {
				t.Fatalf("good value %q rejected by validation: %v", good, errs)
			}
			for _, doc := range generateGuardDocs(t, cfg, baseDir) {
				var parsed map[string]interface{}
				if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
					t.Fatalf("good value %q produced an unparseable workflow: %v\n%s", good, err, doc)
				}
			}
		})
	}
}

package config

import "testing"

func TestResolveComponent_InheritsSharedDefaults(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
release_trigger: dispatch
allow_breaking_changes: true
job_timeout_minutes: 30
builds:
  - name: app
    workflow: .github/workflows/build.yaml
    triggers: ["src/**"]
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
  web:
    path: services/web
    tag_grammar:
      prefix: web-
`)

	rc, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent: %v", err)
	}
	eff := rc.Config

	if rc.Name != "api" {
		t.Errorf("name = %q, want api", rc.Name)
	}
	if rc.Path != "services/api" {
		t.Errorf("path = %q, want services/api", rc.Path)
	}
	if eff.GetTagPrefix() != "api-" {
		t.Errorf("tag_grammar.prefix = %q, want api-", eff.GetTagPrefix())
	}
	// Inherited shared defaults.
	if eff.ReleaseTrigger != "dispatch" {
		t.Errorf("release_trigger = %q, want inherited dispatch", eff.ReleaseTrigger)
	}
	if !eff.AllowBreakingChanges {
		t.Error("allow_breaking_changes should inherit shared true")
	}
	if eff.JobTimeoutMinutes != 30 {
		t.Errorf("job_timeout_minutes = %d, want inherited 30", eff.JobTimeoutMinutes)
	}
	if len(eff.Environments) != 2 || eff.Environments[0].Name != "dev" {
		t.Errorf("environments = %v, want inherited [dev prod]", eff.Environments)
	}
	if len(eff.Builds) != 1 || eff.Builds[0].Name != "app" {
		t.Errorf("builds not inherited: %#v", eff.Builds)
	}
	// The effective config carries no nested components.
	if eff.Components != nil {
		t.Errorf("effective config should have no nested components, got %v", eff.Components)
	}
}

func TestResolveComponent_OverridesWhereSet(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
release_trigger: push
allow_breaking_changes: true
job_timeout_minutes: 30
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
    environments: [dev]
    release_trigger: dispatch
    allow_breaking_changes: false
    job_timeout_minutes: 5
`)

	rc, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent: %v", err)
	}
	eff := rc.Config

	if got := eff.Environments; len(got) != 1 || got[0].Name != "dev" {
		t.Errorf("environments override = %v, want [dev]", got)
	}
	if eff.ReleaseTrigger != "dispatch" {
		t.Errorf("release_trigger override = %q, want dispatch", eff.ReleaseTrigger)
	}
	if eff.AllowBreakingChanges {
		t.Error("allow_breaking_changes override to false not applied")
	}
	if eff.JobTimeoutMinutes != 5 {
		t.Errorf("job_timeout_minutes override = %d, want 5", eff.JobTimeoutMinutes)
	}
	// The shared config is untouched by resolution.
	if !cfg.AllowBreakingChanges || cfg.JobTimeoutMinutes != 30 {
		t.Error("ResolveComponent mutated the shared config")
	}
}

func TestResolveComponent_DerivesPerComponentConcurrencyGroup(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
  web:
    path: services/web
    tag_grammar:
      prefix: web-
`)

	api, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	web, err := cfg.ResolveComponent("web")
	if err != nil {
		t.Fatalf("ResolveComponent(web): %v", err)
	}

	if api.Config.GetConcurrencyGroup() == web.Config.GetConcurrencyGroup() {
		t.Fatalf("components share a concurrency group: %q", api.Config.GetConcurrencyGroup())
	}
	if want := ComponentConcurrencyGroup("api"); api.Config.GetConcurrencyGroup() != want {
		t.Errorf("api group = %q, want %q", api.Config.GetConcurrencyGroup(), want)
	}
}

func TestResolveComponent_CancelInProgressOverride(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
concurrency:
  cancel_in_progress: false
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
  web:
    path: services/web
    tag_grammar:
      prefix: web-
    concurrency:
      cancel_in_progress: true
`)

	api, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	web, err := cfg.ResolveComponent("web")
	if err != nil {
		t.Fatalf("ResolveComponent(web): %v", err)
	}

	if api.Config.GetConcurrencyCancelInProgress() {
		t.Error("api should inherit shared cancel_in_progress=false")
	}
	if !web.Config.GetConcurrencyCancelInProgress() {
		t.Error("web should override cancel_in_progress=true")
	}
}

func TestResolveComponent_UnknownComponentErrors(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
`)
	if _, err := cfg.ResolveComponent("missing"); err == nil {
		t.Fatal("expected an error resolving an undeclared component")
	}
}

func TestValidateComponents_RequiresPathAndTagGrammarPrefix(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
components:
  api:
    tag_grammar:
      prefix: api-
  web:
    path: services/web
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, "components.api.path is required") {
		t.Errorf("expected api.path required error, got %v", errs)
	}
	if !hasErrContaining(errs, "components.web.tag_grammar.prefix is required") {
		t.Errorf("expected web.tag_grammar.prefix required error, got %v", errs)
	}
}

func TestValidateComponents_DistinctTagGrammarPrefixes(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
components:
  api:
    path: services/api
    tag_grammar:
      prefix: svc-
  web:
    path: services/web
    tag_grammar:
      prefix: svc-
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, "collides") {
		t.Fatalf("expected a tag-prefix collision error, got %v", errs)
	}
}

func TestValidateComponents_RejectsGlobalFieldOverride(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
    trunk_branch: other
    cli_version: v9.9.9
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, "components.api.trunk_branch is a top-level-only field") {
		t.Errorf("expected trunk_branch global-override rejection, got %v", errs)
	}
	if !hasErrContaining(errs, "components.api.cli_version is a top-level-only field") {
		t.Errorf("expected cli_version global-override rejection, got %v", errs)
	}
}

func TestValidateComponents_RejectsUnknownField(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
    nonsense: true
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, `components.api has unknown field "nonsense"`) {
		t.Fatalf("expected unknown-field rejection, got %v", errs)
	}
}

func TestValidateComponents_RejectsPerComponentConcurrencyGroup(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
    concurrency:
      group: shared-lane
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, "components.api.concurrency.group cannot be overridden") {
		t.Fatalf("expected per-component group rejection, got %v", errs)
	}
}

func TestValidateComponents_RejectsGlobalConcurrencyGroupWhenComponentsDeclared(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
concurrency:
  group: orchestrate-shared
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, "concurrency.group must not be set to a shared literal when components are declared") {
		t.Fatalf("expected manifest-global group rejection, got %v", errs)
	}
}

func TestValidateComponents_SchemaVersionOneAndOmittedAccept(t *testing.T) {
	for _, sv := range []string{"", "schema_version: 1\n"} {
		cfg := parseInline(t, sv+`
trunk_branch: main
environments: [dev, prod]
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
  web:
    path: services/web
    tag_grammar:
      prefix: web-
`)
		if errs := Validate(cfg); len(errs) != 0 {
			t.Errorf("schema_version %q: expected clean validation, got %v", sv, errs)
		}
		if got := cfg.GetSchemaVersion(); got != 1 {
			t.Errorf("schema_version %q: effective version = %d, want 1", sv, got)
		}
	}
}

// TestNoComponents_SingleComponentPathUntouched asserts the byte-identical
// invariant at the config layer: a manifest with no components: block resolves
// exactly as before. The component dimension does not exist, so no accessor
// gains a component axis and ResolveComponent is never reachable.
func TestNoComponents_SingleComponentPathUntouched(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
builds:
  - name: app
    workflow: .github/workflows/build.yaml
    triggers: ["src/**"]
`)
	if cfg.HasComponents() {
		t.Fatal("a manifest without components: must report no components")
	}
	if cfg.Components != nil {
		t.Fatalf("expected nil Components, got %v", cfg.Components)
	}
	if got := cfg.GetConcurrencyGroup(); got != "orchestrate-${{ github.ref }}" {
		t.Errorf("single-component concurrency group changed: %q", got)
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("single-component manifest should validate clean, got %v", errs)
	}
}

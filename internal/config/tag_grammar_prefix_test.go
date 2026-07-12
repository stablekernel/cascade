package config

import "testing"

// TestResolveComponent_TagGrammarPrefixIsRequiredNamespace proves the canonical
// per-component tag namespace lives on tag_grammar.prefix: a component that sets
// only tag_grammar.prefix validates (no standalone tag_prefix), resolves that
// prefix, and inherits the shared top-level tag_grammar sibling sub-fields under
// deep-merge rather than dropping them.
func TestResolveComponent_TagGrammarPrefixIsRequiredNamespace(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
tag_grammar:
  prefix: v
  prerelease_token: rc
  prerelease_separator: "-"
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

	if errs := validateComponents(cfg); len(errs) != 0 {
		t.Fatalf("validateComponents rejected components declaring only tag_grammar.prefix: %v", errs)
	}

	rc, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	spec := rc.TagGrammarSpec()
	if spec.Prefix != "api-" {
		t.Errorf("resolved prefix = %q, want api-", spec.Prefix)
	}
	// Sibling sub-fields inherit from the shared top-level grammar under deep-merge.
	if spec.PreReleaseToken != "rc" {
		t.Errorf("prerelease_token = %q, want inherited rc", spec.PreReleaseToken)
	}
	if spec.PreReleaseSeparator != "-" {
		t.Errorf("prerelease_separator = %q, want inherited -", spec.PreReleaseSeparator)
	}
}

// TestValidateComponents_TagGrammarPrefixRequiredAndDistinct proves the required
// and distinct rules moved onto tag_grammar.prefix: a component missing the
// prefix is rejected, and two components sharing a prefix collide.
func TestValidateComponents_TagGrammarPrefixRequiredAndDistinct(t *testing.T) {
	missing := parseInline(t, `
trunk_branch: main
components:
  api:
    path: services/api
`)
	if errs := validateComponents(missing); !hasErrContaining(errs, "tag_grammar.prefix is required") {
		t.Fatalf("expected tag_grammar.prefix-required error, got: %v", errs)
	}

	collide := parseInline(t, `
trunk_branch: main
components:
  api:
    path: services/api
    tag_grammar:
      prefix: shared-
  web:
    path: services/web
    tag_grammar:
      prefix: shared-
`)
	if errs := validateComponents(collide); !hasErrContaining(errs, "collides") {
		t.Fatalf("expected a distinct-prefix collision error, got: %v", errs)
	}
}

// TestValidate_RejectsLegacyTagPrefixTopLevel proves the removed standalone
// tag_prefix key is now an unknown top-level field, rejected with a did-you-mean
// steering the author to tag_grammar.prefix.
func TestValidate_RejectsLegacyTagPrefixTopLevel(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
tag_prefix: release-
`)
	errs := Validate(cfg)
	if !errsContain(errs, "tag_prefix") {
		t.Fatalf("expected the legacy tag_prefix key to be rejected, got: %v", errs)
	}
	if !errsContain(errs, "tag_grammar.prefix") {
		t.Fatalf("expected a 'did you mean tag_grammar.prefix?' suggestion, got: %v", errs)
	}
}

// TestValidate_RejectsLegacyTagPrefixComponent proves the same at the component
// level: a per-component tag_prefix is an unknown field carrying the
// tag_grammar.prefix did-you-mean, mirroring the top-level suggestion.
func TestValidate_RejectsLegacyTagPrefixComponent(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
components:
  api:
    path: services/api
    tag_prefix: api-
`)
	errs := Validate(cfg)
	if !errsContain(errs, `unknown field "tag_prefix"`) {
		t.Fatalf("expected component tag_prefix rejected as unknown, got: %v", errs)
	}
	if !errsContain(errs, "tag_grammar.prefix") {
		t.Fatalf("expected a 'did you mean tag_grammar.prefix?' suggestion, got: %v", errs)
	}
}

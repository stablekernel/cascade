package config

import "testing"

// TestResolveComponent_DeepMergesEnvironmentConfig proves a component that sets
// only one environment's config inherits the shared entries for the others, and
// that within an overridden entry the unset fields fall back to the shared
// entry's values. Under the previous whole-replace semantics the component's
// single-key map dropped the shared dev/staging entries entirely.
func TestResolveComponent_DeepMergesEnvironmentConfig(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, staging, prod]
environment_config:
  dev:
    gha_environment: development
  staging:
    gha_environment: staging-env
  prod:
    gha_environment: production
    wait_timer: 10
components:
  api:
    path: services/api
    tag_prefix: api-
    environment_config:
      prod:
        wait_timer: 15
  web:
    path: services/web
    tag_prefix: web-
`)

	rc, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	ec := rc.Config.EnvironmentConfig
	if len(ec) != 3 {
		t.Fatalf("environment_config keys = %v, want dev/staging/prod all present", ec)
	}
	if ec["dev"].GHAEnvironment != "development" {
		t.Errorf("dev env_config dropped: %#v", ec["dev"])
	}
	if ec["staging"].GHAEnvironment != "staging-env" {
		t.Errorf("staging env_config dropped: %#v", ec["staging"])
	}
	// Within the overridden prod entry: wait_timer overrides, gha_environment
	// inherits the shared entry (within-key deep merge).
	if ec["prod"].WaitTimer != 15 {
		t.Errorf("prod wait_timer = %d, want override 15", ec["prod"].WaitTimer)
	}
	if ec["prod"].GHAEnvironment != "production" {
		t.Errorf("prod gha_environment = %q, want inherited production", ec["prod"].GHAEnvironment)
	}
}

// TestResolveComponent_DeepMergesTagGrammar proves a component that sets only
// tag_grammar.prefix inherits the shared prerelease_token and separator instead
// of dropping them. Under whole-replace the sibling sub-fields were lost.
func TestResolveComponent_DeepMergesTagGrammar(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
tag_grammar:
  prefix: v
  prerelease_token: rc
  prerelease_separator: "-"
components:
  api:
    path: services/api
    tag_prefix: api-
    tag_grammar:
      prefix: api
`)

	rc, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	tg := rc.Config.TagGrammar
	if tg == nil {
		t.Fatal("tag_grammar is nil, want merged block")
	}
	if tg.Prefix == nil || *tg.Prefix != "api" {
		t.Errorf("tag_grammar.prefix = %v, want override api", tg.Prefix)
	}
	if tg.PreReleaseToken == nil || *tg.PreReleaseToken != "rc" {
		t.Errorf("tag_grammar.prerelease_token = %v, want inherited rc", tg.PreReleaseToken)
	}
	if tg.PreReleaseSeparator == nil || *tg.PreReleaseSeparator != "-" {
		t.Errorf("tag_grammar.prerelease_separator = %v, want inherited -", tg.PreReleaseSeparator)
	}
}

// TestResolveComponent_ExtraPathsStillUnion guards the one axis that must NOT
// become a deep-merge: a component's extra_paths still unions with the top-level
// shared_paths, deduplicated and sorted.
func TestResolveComponent_ExtraPathsStillUnion(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
shared_paths: [libs/**]
components:
  api:
    path: services/api
    tag_prefix: api-
    extra_paths: [protos/**]
`)

	rc, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	got := map[string]bool{}
	for _, p := range rc.ExtraPaths {
		got[p] = true
	}
	if !got["libs/**"] || !got["protos/**"] {
		t.Errorf("ExtraPaths = %v, want union of libs/** and protos/**", rc.ExtraPaths)
	}
}

// TestResolveComponent_FullBlockOverrideStillWins proves that when a component
// sets every field of a nested block, the component's values win outright: deep
// merge reduces to replacement when nothing is left to inherit.
func TestResolveComponent_FullBlockOverrideStillWins(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
tag_grammar:
  prefix: v
  prerelease_token: rc
components:
  api:
    path: services/api
    tag_prefix: api-
    tag_grammar:
      prefix: api
      prerelease_token: beta
`)

	rc, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	tg := rc.Config.TagGrammar
	if tg.Prefix == nil || *tg.Prefix != "api" {
		t.Errorf("prefix = %v, want api", tg.Prefix)
	}
	if tg.PreReleaseToken == nil || *tg.PreReleaseToken != "beta" {
		t.Errorf("prerelease_token = %v, want override beta", tg.PreReleaseToken)
	}
}

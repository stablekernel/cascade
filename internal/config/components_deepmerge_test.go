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
    tag_grammar:
      prefix: api-
    environment_config:
      prod:
        wait_timer: 15
  web:
    path: services/web
    tag_grammar:
      prefix: web-
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
	if ec["prod"].WaitTimerMinutes() != 15 {
		t.Errorf("prod wait_timer = %d, want override 15", ec["prod"].WaitTimerMinutes())
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
    tag_grammar:
      prefix: api-
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

// TestResolveComponent_DeepMergeOptOutOverridesInheritedTrue proves the opt-out
// direction: a component that sets an inherited boolean back to false overrides
// the inherited true rather than silently inheriting it. This is the regression
// the pointer-typed override fields guard against: with a bare bool the
// component's explicit false marshalled to nothing under the deep merge and the
// component silently stayed enabled.
func TestResolveComponent_DeepMergeOptOutOverridesInheritedTrue(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
deployments:
  enabled: true
changelog:
  disabled: true
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
    deployments:
      enabled: false
    changelog:
      disabled: false
  web:
    path: services/web
    tag_grammar:
      prefix: web-
`)

	api, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	if api.Config.Deployments.IsEnabled() {
		t.Error("api set deployments.enabled: false but stayed enabled (inherited the shared true)")
	}
	if api.Config.Changelog.IsDisabled() {
		t.Error("api set changelog.disabled: false but stayed disabled (inherited the shared true)")
	}

	// The sibling with no override still inherits the shared true values.
	web, err := cfg.ResolveComponent("web")
	if err != nil {
		t.Fatalf("ResolveComponent(web): %v", err)
	}
	if !web.Config.Deployments.IsEnabled() {
		t.Error("web should inherit the shared deployments.enabled: true")
	}
	if !web.Config.Changelog.IsDisabled() {
		t.Error("web should inherit the shared changelog.disabled: true")
	}
}

// TestResolveComponent_SecretsBlockWholeReplaces proves the secrets block is a
// replace-leaf: it is XOR-exclusive (inherit ALL, or an explicit allow-list,
// never both), so a component override whole-replaces the inherited block rather
// than field-merging. A field-merge would combine an inherited "inherit" with a
// component allow-list, and generation gives inherit precedence, silently
// broadening the component's least-privilege scope.
func TestResolveComponent_SecretsBlockWholeReplaces(t *testing.T) {
	// Shared inherit-all, component narrows to an explicit allow-list.
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
validate:
  workflow: .github/workflows/validate.yaml
  secrets: inherit
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
    validate:
      secrets:
        NPM_TOKEN: NPM_TOKEN
  web:
    path: services/web
    tag_grammar:
      prefix: web-
`)
	api, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	sec := api.Config.Validate.Secrets
	if sec == nil {
		t.Fatal("api validate.secrets is nil, want the explicit allow-list")
	}
	if sec.Inherit {
		t.Error("api narrowed secrets to an allow-list but Inherit stayed true (least-privilege broadened)")
	}
	if len(sec.Map) != 1 || sec.Map["NPM_TOKEN"] != "NPM_TOKEN" {
		t.Errorf("api secrets map = %v, want only NPM_TOKEN", sec.Map)
	}
	// The sibling with no override inherits the shared inherit-all block intact.
	web, err := cfg.ResolveComponent("web")
	if err != nil {
		t.Fatalf("ResolveComponent(web): %v", err)
	}
	if web.Config.Validate.Secrets == nil || !web.Config.Validate.Secrets.Inherit {
		t.Errorf("web should inherit the shared secrets: inherit, got %#v", web.Config.Validate.Secrets)
	}

	// Reverse: shared allow-list, component broadens back to inherit.
	cfg2 := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
validate:
  workflow: .github/workflows/validate.yaml
  secrets:
    SHARED_TOKEN: SHARED_TOKEN
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
    validate:
      secrets: inherit
`)
	api2, err := cfg2.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api) reverse: %v", err)
	}
	sec2 := api2.Config.Validate.Secrets
	if sec2 == nil || !sec2.Inherit {
		t.Errorf("api should override to secrets: inherit, got %#v", sec2)
	}
	if len(sec2.Map) != 0 {
		t.Errorf("api secrets map should be empty after overriding to inherit, got %v", sec2.Map)
	}
}

// TestResolveComponent_RunsOnBlockWholeReplaces proves runs_on is a replace-leaf:
// a component that overrides the polymorphic runs_on block replaces it outright,
// so no field from the inherited form (a stale label or group) lingers.
func TestResolveComponent_RunsOnBlockWholeReplaces(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
runs_on:
  group: shared-group
  labels: [self-hosted, linux]
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
    runs_on: ubuntu-latest
`)
	api, err := cfg.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	ro := api.Config.RunsOn
	if ro == nil {
		t.Fatal("api runs_on is nil, want the scalar override")
	}
	if ro.Label != "ubuntu-latest" {
		t.Errorf("api runs_on label = %q, want ubuntu-latest", ro.Label)
	}
	if ro.Group != "" || len(ro.Labels) != 0 {
		t.Errorf("api runs_on retained inherited fields (group=%q labels=%v); the block must whole-replace", ro.Group, ro.Labels)
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

package config

import "testing"

// baseComponentConfig returns a shared-default TrunkConfig declaring two
// components that inherit slice, map, and pointer-struct fields from the top
// level. It exercises the aliasing surface ResolveComponent must not leak
// across siblings.
func baseComponentConfig() *TrunkConfig {
	return &TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		ActionPins:   map[string]string{"actions/checkout": "v4"},
		Git:          &GitConfig{UserName: "shared-bot", UserEmail: "bot@example.com"},
		Components: map[string]ComponentConfig{
			"api": {Path: "services/api", TagGrammar: &TagGrammarConfig{Prefix: strptr("api-")}},
			"web": {Path: "services/web", TagGrammar: &TagGrammarConfig{Prefix: strptr("web-")}},
		},
	}
}

// TestResolveComponent_NoSiblingBleed proves the resolved effective config is a
// deep copy: mutating one component's slice, map, or pointer-struct field must
// not reach a sibling component's resolved config or the shared source config. A
// shallow copy (eff := *c) would alias the backing array/map/pointer and fail
// every assertion below.
func TestResolveComponent_NoSiblingBleed(t *testing.T) {
	c := baseComponentConfig()

	api, err := c.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	web, err := c.ResolveComponent("web")
	if err != nil {
		t.Fatalf("ResolveComponent(web): %v", err)
	}

	// Slice: overwrite api's inherited environments in place.
	api.Config.Environments[0] = "MUTATED"
	if web.Config.Environments[0] != "dev" {
		t.Errorf("slice bleed: web env[0] = %q, want dev", web.Config.Environments[0])
	}
	if c.Environments[0] != "dev" {
		t.Errorf("slice bleed into source: c env[0] = %q, want dev", c.Environments[0])
	}

	// Map: mutate api's inherited action pins.
	api.Config.ActionPins["actions/checkout"] = "MUTATED"
	if web.Config.ActionPins["actions/checkout"] != "v4" {
		t.Errorf("map bleed: web pin = %q, want v4", web.Config.ActionPins["actions/checkout"])
	}
	if c.ActionPins["actions/checkout"] != "v4" {
		t.Errorf("map bleed into source: c pin = %q, want v4", c.ActionPins["actions/checkout"])
	}

	// Pointer struct: mutate api's inherited git identity.
	api.Config.Git.UserName = "MUTATED"
	if web.Config.Git.UserName != "shared-bot" {
		t.Errorf("pointer bleed: web git user = %q, want shared-bot", web.Config.Git.UserName)
	}
	if c.Git.UserName != "shared-bot" {
		t.Errorf("pointer bleed into source: c git user = %q, want shared-bot", c.Git.UserName)
	}
}

// TestResolveComponent_DerivesPathTrigger proves a component with no explicit
// triggers gets a push-paths filter scoped to its own subtree, so its
// orchestrate workflow only fires on changes under its path.
func TestResolveComponent_DerivesPathTrigger(t *testing.T) {
	c := baseComponentConfig()

	api, err := c.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	got := api.Config.GetAllTriggers()
	want := []string{"services/api/**"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("api triggers = %v, want %v", got, want)
	}

	web, err := c.ResolveComponent("web")
	if err != nil {
		t.Fatalf("ResolveComponent(web): %v", err)
	}
	if g := web.Config.GetAllTriggers(); len(g) != 1 || g[0] != "services/web/**" {
		t.Errorf("web triggers = %v, want [services/web/**]", g)
	}
}

// TestResolveComponent_HonorsExplicitTriggers proves an inherited explicit
// triggers list wins over path derivation: the shared default filter is kept, not
// replaced by the component subtree.
func TestResolveComponent_HonorsExplicitTriggers(t *testing.T) {
	c := baseComponentConfig()
	c.Triggers = []string{"shared/**"}

	api, err := c.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	if g := api.Config.GetAllTriggers(); len(g) != 1 || g[0] != "shared/**" {
		t.Errorf("api triggers = %v, want [shared/**] (explicit inherited filter honored)", g)
	}
}

// TestResolveComponent_PerComponentTrigger proves a per-component triggers
// override wins over both path derivation and the shared default.
func TestResolveComponent_PerComponentTrigger(t *testing.T) {
	c := baseComponentConfig()
	comp := c.Components["api"]
	comp.Triggers = []string{"custom/**"}
	c.Components["api"] = comp

	api, err := c.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	if g := api.Config.GetAllTriggers(); len(g) != 1 || g[0] != "custom/**" {
		t.Errorf("api triggers = %v, want [custom/**] (per-component override honored)", g)
	}
}

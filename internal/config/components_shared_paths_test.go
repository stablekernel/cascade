package config

import (
	"reflect"
	"testing"
)

// TestResolveComponent_ExtraPaths proves a per-component extra_paths list is
// carried onto the resolved component (for version and change-detection scoping)
// and folded into the push-paths filter, additive to the component's own path.
func TestResolveComponent_ExtraPaths(t *testing.T) {
	c := baseComponentConfig()
	comp := c.Components["api"]
	comp.ExtraPaths = []string{"libs/proto/**"}
	c.Components["api"] = comp

	api, err := c.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}

	if want := []string{"libs/proto/**"}; !reflect.DeepEqual(api.ExtraPaths, want) {
		t.Errorf("api.ExtraPaths = %v, want %v", api.ExtraPaths, want)
	}
	// Push filter (GetAllTriggers) must include the component path and the extra path.
	got := api.Config.GetAllTriggers()
	want := []string{"libs/proto/**", "services/api/**"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("api triggers = %v, want %v", got, want)
	}

	// A sibling without extra_paths is unaffected.
	web, err := c.ResolveComponent("web")
	if err != nil {
		t.Fatalf("ResolveComponent(web): %v", err)
	}
	if len(web.ExtraPaths) != 0 {
		t.Errorf("web.ExtraPaths = %v, want empty", web.ExtraPaths)
	}
	if g := web.Config.GetAllTriggers(); len(g) != 1 || g[0] != "services/web/**" {
		t.Errorf("web triggers = %v, want [services/web/**]", g)
	}
}

// TestResolveComponent_SharedPathsFanOut proves a top-level shared_paths entry is
// fanned into every component's effective path set and push filter, and unions
// with a component's own extra_paths without duplication.
func TestResolveComponent_SharedPathsFanOut(t *testing.T) {
	c := baseComponentConfig()
	c.SharedPaths = []string{"go.mod", "libs/shared/**"}
	comp := c.Components["api"]
	comp.ExtraPaths = []string{"libs/proto/**", "go.mod"} // go.mod overlaps shared_paths
	c.Components["api"] = comp

	api, err := c.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	wantAPI := []string{"go.mod", "libs/proto/**", "libs/shared/**"}
	if !reflect.DeepEqual(api.ExtraPaths, wantAPI) {
		t.Errorf("api.ExtraPaths = %v, want %v (deduped, sorted)", api.ExtraPaths, wantAPI)
	}

	web, err := c.ResolveComponent("web")
	if err != nil {
		t.Fatalf("ResolveComponent(web): %v", err)
	}
	wantWeb := []string{"go.mod", "libs/shared/**"}
	if !reflect.DeepEqual(web.ExtraPaths, wantWeb) {
		t.Errorf("web.ExtraPaths = %v, want %v", web.ExtraPaths, wantWeb)
	}
	// Push filter for web: its own path plus the shared paths.
	if g := web.Config.GetAllTriggers(); !reflect.DeepEqual(g, []string{"go.mod", "libs/shared/**", "services/web/**"}) {
		t.Errorf("web triggers = %v, want [go.mod libs/shared/** services/web/**]", g)
	}
}

// TestResolveComponent_NoSharedPathsByteIdentical proves that when neither
// extra_paths nor shared_paths is set, the resolved component carries no extra
// paths and the push filter is exactly the path-derived default, so the shape is
// unchanged from before the feature.
func TestResolveComponent_NoSharedPathsByteIdentical(t *testing.T) {
	c := baseComponentConfig()
	api, err := c.ResolveComponent("api")
	if err != nil {
		t.Fatalf("ResolveComponent(api): %v", err)
	}
	if api.ExtraPaths != nil {
		t.Errorf("api.ExtraPaths = %v, want nil when unset", api.ExtraPaths)
	}
	if g := api.Config.GetAllTriggers(); len(g) != 1 || g[0] != "services/api/**" {
		t.Errorf("api triggers = %v, want [services/api/**]", g)
	}
}

// TestSharedPathsRejectedPerComponent proves shared_paths is a top-level-only key:
// setting it under a component is a configuration error, not a silent no-op.
func TestSharedPathsRejectedPerComponent(t *testing.T) {
	if _, ok := globalOnlyComponentFields["shared_paths"]; !ok {
		t.Fatalf("shared_paths must be a global-only component field")
	}
}

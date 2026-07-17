package hotfix

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeComponentOnlyManifest writes a manifest whose ONLY recorded state lives
// under state.components.<name>.<env> for two declared components (api, web); it
// never populates the flat state.<env> node a real multi-component manifest never
// writes to for a declared component (orchestrate and promote always record via
// WriteScopedState). It reproduces the exact shape a per-component hotfix plans
// against on a real repo, so a planner that reads only the flat map fails closed
// with "no recorded state SHA" here just as it did on the fleet.
func writeComponentOnlyManifest(t *testing.T, envs []string, componentState map[string]map[string]string) string {
	t.Helper()

	names := make([]string, 0, len(componentState))
	for name := range componentState {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("ci:\n")
	b.WriteString("  config:\n")
	b.WriteString("    environments:\n")
	for _, e := range envs {
		b.WriteString("      - " + e + "\n")
	}
	// Declare every seeded component with a path subtree and no overrides, so each
	// inherits the global ladder and tag grammar. The real generated shape carries
	// both halves: config.components declares the component (which is why its
	// hotfix workflow exists at all) and state.components records its rows. Only
	// the state half is exercised below; the declaration is what a component-scoped
	// planner resolves its ladder out of.
	b.WriteString("    components:\n")
	for _, name := range names {
		b.WriteString("      " + name + ":\n")
		b.WriteString("        path: services/" + name + "\n")
	}
	b.WriteString("  state:\n")
	b.WriteString("    components:\n")
	for comp, state := range componentState {
		b.WriteString("      " + comp + ":\n")
		for env, sha := range state {
			b.WriteString("        " + env + ":\n")
			b.WriteString("          sha: " + sha + "\n")
			b.WriteString("          version: v1.0.0-rc.1\n")
		}
	}

	path := filepath.Join(".", "manifest.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// TestPlan_Component_UsesComponentScopedEnvBranch proves a planner scoped to a
// component names the integration branch env/<component>/<env> and creates it at
// the recorded state SHA, so a per-component hotfix operates in its own branch
// namespace and agrees with the component-aware finalize path.
func TestPlan_Component_UsesComponentScopedEnvBranch(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev":  fix,
		"test": base,
		"prod": base,
	}, "web")

	p := newPlanner(t, manifest, WithPlanComponent("web"))
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.NoOp {
		t.Fatal("expected non-noop plan")
	}
	if res.Branch != "env/web/test" {
		t.Errorf("branch = %q, want env/web/test", res.Branch)
	}
	if !res.BranchCreated {
		t.Error("expected BranchCreated=true when branch absent")
	}
	// The local branch must actually be created at the recorded base SHA under
	// the component-scoped name.
	got := gitOut(t, "rev-parse", "env/web/test")
	if got != base {
		t.Errorf("env/web/test tip = %q, want %q", got, base)
	}
}

// TestPlan_SingleComponent_UsesFlatEnvBranch pins the default (no component)
// planner to the historical flat env/<env> name, so the single-component path
// stays byte-identical to the pre-component behavior.
func TestPlan_SingleComponent_UsesFlatEnvBranch(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev":  fix,
		"test": base,
		"prod": base,
	})

	p := newPlanner(t, manifest)
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Branch != "env/test" {
		t.Errorf("branch = %q, want env/test", res.Branch)
	}
	// No component-scoped branch is created for the single-component path.
	if err := exec.Command("git", "rev-parse", "--verify", "env/web/test").Run(); err == nil {
		t.Error("single-component plan created a component-scoped env/web/test branch")
	}
}

// TestPlan_Component_SingleFlightQueriesComponentBranch proves the whole plan
// path, not just the reported branch, operates on the component-scoped branch:
// the single-flight open-PR gate is queried with env/<component>/<env>, so a
// per-component hotfix can never be blocked or unblocked by another component's
// resolution PR.
func TestPlan_Component_SingleFlightQueriesComponentBranch(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev":  fix,
		"test": base,
		"prod": base,
	}, "web")

	stub := &stubPRChecker{}
	p := newPlanner(t, manifest, WithPlanComponent("web"), WithPRChecker(stub))
	if _, err := p.Plan(fix, "test"); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if stub.calledWith != "env/web/test" {
		t.Errorf("single-flight queried %q, want env/web/test", stub.calledWith)
	}
}

// TestPlan_Component_ReadsComponentScopedStateOnly is the regression test for the
// bug the isolation e2e scenario caught: a component-scoped plan must resolve its
// base SHA from state.components.<component>.<env>, not the flat state.<env> node,
// which a multi-component manifest never populates for a declared component. The
// manifest here carries ONLY the components subtree (api and web, both at "test"),
// reproducing the real generated shape; a planner that reads the flat map alone
// fails with "no recorded state SHA" even though the component's row exists.
func TestPlan_Component_ReadsComponentScopedStateOnly(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")

	manifest := writeComponentOnlyManifest(t, []string{"dev", "test", "prod"}, map[string]map[string]string{
		"api": {"test": base, "prod": base},
		"web": {"test": base, "prod": base},
	})

	p := newPlanner(t, manifest, WithPlanComponent("api"))
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v (component-scoped state must be readable with no flat state.<env> node)", err)
	}
	if res.BaseSHA != base {
		t.Errorf("base_sha = %q, want %q (api's own state.components.api.test.sha)", res.BaseSHA, base)
	}
	if res.Branch != "env/api/test" {
		t.Errorf("branch = %q, want env/api/test", res.Branch)
	}
}

// TestPlan_Component_IgnoresSiblingComponentState proves the overlay reads only
// the named component's subtree: api and web are seeded at DIFFERENT base SHAs, so
// a plan scoped to api must resolve api's own base, never web's.
func TestPlan_Component_IgnoresSiblingComponentState(t *testing.T) {
	newScratchRepo(t)
	apiBase := commitFile(t, "a.txt", "one", "api base")
	webBase := commitFile(t, "b.txt", "two", "web base")
	fix := commitFile(t, "c.txt", "three", "fix")

	manifest := writeComponentOnlyManifest(t, []string{"dev", "test", "prod"}, map[string]map[string]string{
		"api": {"test": apiBase},
		"web": {"test": webBase},
	})

	p := newPlanner(t, manifest, WithPlanComponent("api"))
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.BaseSHA != apiBase {
		t.Errorf("base_sha = %q, want api's own base %q (must not read web's %q)", res.BaseSHA, apiBase, webBase)
	}
}

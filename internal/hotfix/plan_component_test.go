package hotfix

import (
	"os/exec"
	"testing"
)

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
	})

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
	})

	stub := &stubPRChecker{}
	p := newPlanner(t, manifest, WithPlanComponent("web"), WithPRChecker(stub))
	if _, err := p.Plan(fix, "test"); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if stub.calledWith != "env/web/test" {
		t.Errorf("single-flight queried %q, want env/web/test", stub.calledWith)
	}
}

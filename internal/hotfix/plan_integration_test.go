package hotfix

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPlan_Integration_FullFlow exercises the plan verb end to end against a
// real temporary git repository: branch reconciliation creates env/<target> at
// the recorded state SHA, every emitted output is populated, and a subsequent
// --dry-run run over a fresh repo mutates nothing. This is the scratch-repo
// integration coverage that complements the focused unit tests; full act/gitea
// e2e for the plan verb through the generated workflow is owned by the e2e
// harness unit.
func TestPlan_Integration_FullFlow(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	// fix lands on trunk after the env's recorded base, so it is a real hotfix.
	fix := commitFile(t, "b.txt", "two", "fix on trunk")

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

	// Reconciliation created env/test at the recorded state SHA.
	if !res.BranchCreated {
		t.Error("expected BranchCreated=true")
	}
	if got := gitOut(t, "rev-parse", "env/test"); got != base {
		t.Errorf("env/test tip = %q, want recorded base %q", got, base)
	}

	// Every emitted output the workflow consumes is populated.
	if res.Branch != "env/test" {
		t.Errorf("branch = %q, want env/test", res.Branch)
	}
	if res.BaseSHA != base {
		t.Errorf("base_sha = %q, want %q", res.BaseSHA, base)
	}
	if res.FixSHA != fix {
		t.Errorf("fix_sha = %q, want %q", res.FixSHA, fix)
	}
	if res.HotfixVersionCandidate != "v1.0.0-rc.1.hotfix.1" {
		t.Errorf("hotfix_version_candidate = %q, want v1.0.0-rc.1.hotfix.1", res.HotfixVersionCandidate)
	}
	if len(res.ProtectionSuggestions) == 0 {
		t.Error("expected protection_suggestions to be populated")
	}
	if joined := strings.Join(res.ProtectionSuggestions, "\n"); !strings.Contains(joined, "env/test") {
		t.Errorf("protection_suggestions should target env/test:\n%s", joined)
	}
	if res.NoOp {
		t.Error("expected non-noop plan")
	}
}

// TestPlan_Integration_DryRunMutatesNothing confirms a full dry-run plan over a
// real repository computes the same outputs but creates no env branch.
func TestPlan_Integration_DryRunMutatesNothing(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix on trunk")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev":  fix,
		"test": base,
		"prod": base,
	})

	p := newPlanner(t, manifest, WithDryRun(true))
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if !res.DryRun {
		t.Error("expected DryRun=true on the result")
	}
	if !res.BranchCreated {
		t.Error("dry-run should still report it WOULD create the branch")
	}
	if res.BaseSHA != base {
		t.Errorf("base_sha = %q, want %q", res.BaseSHA, base)
	}
	if res.HotfixVersionCandidate != "v1.0.0-rc.1.hotfix.1" {
		t.Errorf("hotfix_version_candidate = %q, want v1.0.0-rc.1.hotfix.1", res.HotfixVersionCandidate)
	}
	// The defining dry-run property: no env branch on disk.
	if err := exec.Command("git", "rev-parse", "--verify", "env/test").Run(); err == nil {
		t.Error("dry-run created env/test; a dry-run plan must mutate nothing")
	}
}

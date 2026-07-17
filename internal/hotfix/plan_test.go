package hotfix

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newScratchRepo initializes a git repository in a temp directory, chdirs into
// it for the duration of the test, and returns the repo path. The original
// working directory is restored via t.Cleanup.
func newScratchRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	runGit(t, "init", "-b", "main")
	runGit(t, "config", "user.email", "test@example.com")
	runGit(t, "config", "user.name", "Test User")
	runGit(t, "config", "commit.gpgsign", "false")

	return dir
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(".", name), []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, "add", name)
	runGit(t, "commit", "-m", message)
	return gitOut(t, "rev-parse", "HEAD")
}

// writeManifest writes a manifest file with the given environments and state map
// and returns its path. State entries are "env:sha" pairs.
//
// components, when given, are declared under config.components with a path
// subtree and no overrides, so each inherits the global ladder and tag grammar
// verbatim. A component-scoped planner resolves its ladder and grammar out of
// that declaration, and only a declared component can be resolved, so any test
// passing WithPlanComponent must declare it here. This mirrors production: the
// generator emits a component's hotfix workflow only for a component the config
// declares, so a component-scoped hotfix always runs against a declaration.
func writeManifest(t *testing.T, envs []string, state map[string]string, components ...string) string {
	t.Helper()

	var b strings.Builder
	b.WriteString("ci:\n")
	b.WriteString("  config:\n")
	b.WriteString("    environments:\n")
	for _, e := range envs {
		b.WriteString("      - " + e + "\n")
	}
	if len(components) > 0 {
		b.WriteString("    components:\n")
		for _, c := range components {
			b.WriteString("      " + c + ":\n")
			b.WriteString("        path: services/" + c + "\n")
		}
	}
	b.WriteString("  state:\n")
	for e, sha := range state {
		b.WriteString("    " + e + ":\n")
		b.WriteString("      sha: " + sha + "\n")
		b.WriteString("      version: v1.0.0-rc.1\n")
	}

	path := filepath.Join(".", "manifest.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// stubPRChecker records lookups and returns a fixed list of open PRs.
type stubPRChecker struct {
	prs        []OpenPR
	calledWith string
}

func (s *stubPRChecker) OpenHotfixPRs(baseBranch string) ([]OpenPR, error) {
	s.calledWith = baseBranch
	return s.prs, nil
}

func newPlanner(t *testing.T, manifest string, opts ...Option) *Planner {
	t.Helper()
	p, err := NewPlanner(PlannerOptions{ConfigPath: manifest, ManifestKey: "ci", Actor: "tester"}, opts...)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	return p
}

func TestPlan_RejectsNonTrunkCommit(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	// Side commit that is not on trunk.
	runGit(t, "checkout", "-b", "side")
	side := commitFile(t, "b.txt", "two", "side fix")
	runGit(t, "checkout", "main")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev":  base,
		"test": base,
		"prod": base,
	})

	p := newPlanner(t, manifest)
	_, err := p.Plan(side, "test")
	if err == nil {
		t.Fatal("expected error for non-trunk commit, got nil")
		return
	}
	if !strings.Contains(err.Error(), "trunk") {
		t.Errorf("error %q should mention trunk", err.Error())
	}
}

func TestPlan_RejectsFirstEnv(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix on trunk")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev":  base,
		"test": base,
		"prod": base,
	})

	p := newPlanner(t, manifest)
	_, err := p.Plan(fix, "dev")
	if err == nil {
		t.Fatal("expected error for first env, got nil")
		return
	}
	if !strings.Contains(err.Error(), "first environment") {
		t.Errorf("error %q should mention first environment", err.Error())
	}
}

func TestPlan_RejectsUnknownEnv(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev": base, "test": base, "prod": base,
	})

	p := newPlanner(t, manifest)
	if _, err := p.Plan(fix, "staging"); err == nil {
		t.Fatal("expected error for unknown env")
	}
}

func TestPlan_NoOpWhenFixAlreadyInTarget(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	tip := commitFile(t, "c.txt", "three", "later")

	// test points at tip, which already contains the fix.
	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev":  base,
		"test": tip,
		"prod": base,
	})

	p := newPlanner(t, manifest)
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !res.NoOp {
		t.Errorf("expected NoOp=true, got %+v", res)
	}
}

func TestPlan_CreatesEnvBranchAtStateSHA(t *testing.T) {
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
	if res.NoOp {
		t.Fatal("expected non-noop plan")
	}
	if res.Branch != "env/test" {
		t.Errorf("branch = %q, want env/test", res.Branch)
	}
	if res.BaseSHA != base {
		t.Errorf("base sha = %q, want %q", res.BaseSHA, base)
	}
	if !res.BranchCreated {
		t.Error("expected BranchCreated=true when branch absent")
	}
	// Verify the local branch was actually created at base SHA (not dry-run).
	got := gitOut(t, "rev-parse", "env/test")
	if got != base {
		t.Errorf("env/test tip = %q, want %q", got, base)
	}
}

func TestPlan_DryRunMutatesNothing(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev": fix, "test": base, "prod": base,
	})

	p := newPlanner(t, manifest, WithDryRun(true))
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.NoOp {
		t.Fatal("expected non-noop plan")
	}
	// In dry-run the branch must NOT be created.
	if err := exec.Command("git", "rev-parse", "--verify", "env/test").Run(); err == nil {
		t.Error("dry-run created env/test branch; should mutate nothing")
	}
	if !res.BranchCreated {
		t.Error("dry-run should still report it WOULD create the branch")
	}
}

func TestPlan_ExistingBranchTipMismatch_FailsClosedWithoutRealChecker(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	other := commitFile(t, "b.txt", "two", "other")
	fix := commitFile(t, "c.txt", "three", "fix")

	// env/test exists but its tip is "other", not the recorded state SHA (base).
	runGit(t, "branch", "env/test", other)

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev":  fix,
		"test": base,
		"prod": base,
	})

	// No PRChecker is injected, so the single-flight gate did not run with a real
	// checker. The planner must fail closed (never self-heal) even though the env
	// is not diverged: it cannot prove no resolution PR is open.
	p := newPlanner(t, manifest)
	_, err := p.Plan(fix, "test")
	if err == nil {
		t.Fatal("expected tip-mismatch error")
		return
	}
	msg := err.Error()
	if !strings.Contains(msg, "abandoned hotfix branch") {
		t.Errorf("error %q should describe the abandoned branch", msg)
	}
	if !strings.Contains(msg, "--repo") {
		t.Errorf("error %q should point at re-running with --repo", msg)
	}
	// The local env/test branch must not have been reset to base.
	if got := gitOut(t, "rev-parse", "env/test"); got != other {
		t.Errorf("env/test tip = %q, want it left at %q (no naive reset)", got, other)
	}
}

func TestPlan_ExistingBranchTipMatch_OK(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "c.txt", "three", "fix")
	runGit(t, "branch", "env/test", base)

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev": fix, "test": base, "prod": base,
	})

	p := newPlanner(t, manifest)
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.BranchCreated {
		t.Error("branch already existed; BranchCreated should be false")
	}
}

func TestPlan_SingleFlight_OpenPRBlocks(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "c.txt", "three", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev": fix, "test": base, "prod": base,
	})

	checker := &stubPRChecker{prs: []OpenPR{{Number: 42, URL: "https://example.test/pr/42"}}}
	p := newPlanner(t, manifest, WithPRChecker(checker))
	_, err := p.Plan(fix, "test")
	if err == nil {
		t.Fatal("expected single-flight error")
		return
	}
	if checker.calledWith != "env/test" {
		t.Errorf("PR checker queried %q, want env/test", checker.calledWith)
	}
	if !strings.Contains(err.Error(), "42") {
		t.Errorf("error %q should reference the open PR number", err.Error())
	}
	// A blocked plan must leave no git state: env/test must NOT have been
	// created by the aborted reconciliation.
	if err := exec.Command("git", "rev-parse", "--verify", "env/test").Run(); err == nil {
		t.Error("single-flight block created env/test branch; a blocked plan must mutate nothing")
	}
}

func TestPlan_SingleFlight_NoOpenPRAllows(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "c.txt", "three", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev": fix, "test": base, "prod": base,
	})

	checker := &stubPRChecker{prs: nil}
	p := newPlanner(t, manifest, WithPRChecker(checker))
	if _, err := p.Plan(fix, "test"); err != nil {
		t.Fatalf("Plan: %v", err)
	}
}

func TestPlan_ProdTargetAllowed(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "c.txt", "three", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev": fix, "test": fix, "prod": base,
	})

	p := newPlanner(t, manifest)
	res, err := p.Plan(fix, "prod")
	if err != nil {
		t.Fatalf("prod target should be eligible: %v", err)
	}
	if res.Branch != "env/prod" {
		t.Errorf("branch = %q, want env/prod", res.Branch)
	}
}

// TestPlan_ProdTarget_CandidateIsPatchBump proves a plan against an env whose
// recorded version is a published base (no pre-release segment) reports the
// patch bump finalize will allocate, not the already-published version. The
// plan's Version line, its --json hotfix_version_candidate field, and the GHA
// output all read this one PlanResult field.
func TestPlan_ProdTarget_CandidateIsPatchBump(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "c.txt", "three", "fix")

	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: fix},
		"test": {sha: fix},
		"prod": {sha: base, version: "v1.3.0"},
	})

	p := newPlanner(t, manifest)
	res, err := p.Plan(fix, "prod")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.HotfixVersionCandidate != "v1.3.1" {
		t.Errorf("version candidate = %q, want v1.3.1 (finalize patch-bumps a published base)", res.HotfixVersionCandidate)
	}
}

func TestPlan_VersionCandidateAndProtectionSuggestions(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "c.txt", "three", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev": fix, "test": base, "prod": base,
	})

	p := newPlanner(t, manifest)
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// state[test].Version is v1.0.0-rc.1, so the candidate is the first hotfix.
	if res.HotfixVersionCandidate != "v1.0.0-rc.1.hotfix.1" {
		t.Errorf("version candidate = %q, want v1.0.0-rc.1.hotfix.1", res.HotfixVersionCandidate)
	}

	if len(res.ProtectionSuggestions) == 0 {
		t.Fatal("expected protection_suggestions to be populated (Q6)")
	}
	joined := strings.Join(res.ProtectionSuggestions, "\n")
	if !strings.Contains(joined, "gh api") {
		t.Errorf("protection suggestions should include a gh api command:\n%s", joined)
	}
	if !strings.Contains(joined, "env/test") {
		t.Errorf("protection suggestions should target env/test:\n%s", joined)
	}
}

func TestPlan_JSONOutputGolden(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "c.txt", "three", "fix")

	manifest := writeManifest(t, []string{"dev", "test", "prod"}, map[string]string{
		"dev": fix, "test": base, "prod": base,
	})

	p := newPlanner(t, manifest, WithDryRun(true))
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)

	for _, want := range []string{
		`"target_env":"test"`,
		`"branch":"env/test"`,
		`"no_op":false`,
		`"dry_run":true`,
		`"hotfix_version_candidate":"v1.0.0-rc.1.hotfix.1"`,
		`"protection_suggestions":`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON output missing %s\ngot: %s", want, s)
		}
	}
}

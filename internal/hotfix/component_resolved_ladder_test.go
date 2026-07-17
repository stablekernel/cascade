package hotfix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// componentLadderManifest declares a repo-global four-env ladder and an "api"
// component that narrows it to a three-env subset, skipping the global-only
// "test" env in the middle. "web" inherits the full global ladder, so a fix that
// resolved the wrong component is distinguishable from one that resolved none.
//
// The generator emits each component's hotfix workflow from that component's
// RESOLVED config (internal/generate/plan.go builds the component
// HotfixGenerator from resolved.Config), so the workflow's target_env choices
// and its apply lane come from the component's own ladder. A runtime that reads
// the ROOT ladder therefore validates and elevates along environments the
// workflow it is driving never offered.
//
// api's versions live in its own "api-" tag namespace, the hyphenated prefix
// style the components guide documents, which the root's permissive read-side
// grammar cannot parse at all: prefixPattern is "[a-zA-Z]*" and does not admit
// the hyphen. web's "web" prefix is purely alphabetic and does parse under both
// grammars, which is the shape where the grammar sink is invisible.
const componentLadderManifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, test, staging, prod]
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-
        environments: [dev, staging, prod]
      web:
        path: services/web
        tag_grammar:
          prefix: web
  state:
    components:
      api:
        dev:
          sha: %[1]s
          version: api-1.2.0-rc.1
        staging:
          sha: %[2]s
          version: api-1.2.0-rc.1
        prod:
          sha: %[2]s
          version: api-1.2.0-rc.1
      web:
        dev:
          sha: %[1]s
          version: web1.2.0-rc.1
        test:
          sha: %[2]s
          version: web1.2.0-rc.1
        staging:
          sha: %[2]s
          version: web1.2.0-rc.1
        prod:
          sha: %[2]s
          version: web1.2.0-rc.1
`

// writeLadderManifest renders componentLadderManifest with the given head and
// base SHAs into the scratch repo's working directory and returns its path.
func writeLadderManifest(t *testing.T, headSHA, baseSHA string) string {
	t.Helper()
	raw := strings.NewReplacer("%[1]s", headSHA, "%[2]s", baseSHA).Replace(componentLadderManifest)
	path := filepath.Join(".", "ladder-manifest.yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// TestPlanChain_Component_LaddersAlongComponentEnvironments pins the sink on the
// WORKFLOW path: PlanChain is what the generated hotfix workflow invokes
// (internal/generate/hotfix.go emits `cascade hotfix plan --commits`, and the
// plural flag selects PlanChain). Its elevation sequence must walk the
// component's own ladder.
//
// api's ladder is [dev, staging, prod], so a chain into prod elevates through
// [staging, prod]. The ROOT ladder is [dev, test, staging, prod], so a runtime
// reading it expands to [test, staging, prod] and plans a hop through "test", an
// environment api does not have and has no state for.
func TestPlanChain_Component_LaddersAlongComponentEnvironments(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	manifest := writeLadderManifest(t, fix, base)

	p := newPlanner(t, manifest, WithPlanComponent("api"))
	res, err := p.PlanChain([]string{fix}, "prod")
	if err != nil {
		t.Fatalf("PlanChain: %v", err)
	}

	got := make([]string, 0, len(res.Envs))
	for _, e := range res.Envs {
		got = append(got, e.Env)
	}
	if strings.Join(got, ",") != "staging,prod" {
		t.Errorf("PlanChain envs = %v, want [staging prod]: a component chain must elevate along its OWN ladder, never the global-only tail", got)
	}
}

// TestPlanChain_Component_SiblingKeepsInheritedLadder proves the ladder
// resolution reads the NAMED component rather than an arbitrary one: web
// declares no environments override, so it inherits the full global ladder and
// its chain into prod still walks the global-only "test" hop.
func TestPlanChain_Component_SiblingKeepsInheritedLadder(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	manifest := writeLadderManifest(t, fix, base)

	p := newPlanner(t, manifest, WithPlanComponent("web"))
	res, err := p.PlanChain([]string{fix}, "prod")
	if err != nil {
		t.Fatalf("PlanChain: %v", err)
	}

	got := make([]string, 0, len(res.Envs))
	for _, e := range res.Envs {
		got = append(got, e.Env)
	}
	if strings.Join(got, ",") != "test,staging,prod" {
		t.Errorf("PlanChain envs = %v, want [test staging prod]: an inheriting component keeps the global ladder", got)
	}
}

// TestPlan_Component_RejectsEnvOutsideComponentLadder covers the single-env
// eligibility sink. "test" is on the root ladder but not on api's, so the
// generated api workflow never offers it as a target_env. A runtime validating
// against the root ladder accepts it and plans a hotfix into an environment the
// component does not have.
func TestPlan_Component_RejectsEnvOutsideComponentLadder(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	manifest := writeLadderManifest(t, fix, base)

	p := newPlanner(t, manifest, WithPlanComponent("api"))
	_, err := p.Plan(fix, "test")
	if err == nil {
		t.Fatal("Plan into an env outside the component's ladder must be refused")
	}
	if !strings.Contains(err.Error(), "not a configured environment") {
		t.Errorf("error = %v, want a not-configured-environment refusal", err)
	}
}

// TestFinalize_Component_RejectsEnvOutsideComponentLadder covers the same
// eligibility sink on the finalize verb, which the generated workflow reaches
// from the resolution PR merge.
func TestFinalize_Component_RejectsEnvOutsideComponentLadder(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	manifest := writeLadderManifest(t, fix, base)

	f, err := NewFinalizer(FinalizerOptions{ConfigPath: manifest, ManifestKey: "ci", Actor: "tester"}, WithComponent("api"))
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	err = f.Finalize("test", fix, []string{fix}, base)
	if err == nil {
		t.Fatal("Finalize into an env outside the component's ladder must be refused")
	}
	if !strings.Contains(err.Error(), "not a configured environment") {
		t.Errorf("error = %v, want a not-configured-environment refusal", err)
	}
}

// TestPlan_Component_VersionCandidateUsesComponentGrammar pins the grammar sink
// (plan.go's hotfixVersionCandidate call). api's recorded version lives in its
// own "api-" namespace, which the ROOT permissive grammar cannot parse at all:
// prefixPattern is "[a-zA-Z]*" and does not admit the hyphen.
//
// Finalize already resolves this correctly via resolveFinalizeSpec, so a plan
// reading the root grammar disagrees with the finalize that follows it.
func TestPlan_Component_VersionCandidateUsesComponentGrammar(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	manifest := writeLadderManifest(t, fix, base)

	p := newPlanner(t, manifest, WithPlanComponent("api"))
	res, err := p.Plan(fix, "staging")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.HotfixVersionCandidate != "api-1.2.0-rc.1.hotfix.1" {
		t.Errorf("HotfixVersionCandidate = %q, want api-1.2.0-rc.1.hotfix.1: the plan must render the candidate under the component's own grammar, the same one finalize emits under",
			res.HotfixVersionCandidate)
	}
}

// TestPlan_Component_VersionCandidateAgreesWhereGrammarsOverlap is the
// neutrality half of the grammar swap. web's prefix is purely alphabetic, so
// web1.2.0-rc.1 parses under BOTH the root's permissive "[a-zA-Z]*" pattern and
// the component's strict one, and both render the same candidate: the parse
// carries the prefix it read, so a permissive read of an alphabetic prefix is
// not silently re-rendered under the root's.
//
// This shape is therefore the one where the sink is invisible, and it is why the
// bug reaches only the hyphenated prefixes the components guide documents. It
// passes before the swap and must keep passing after it.
func TestPlan_Component_VersionCandidateAgreesWhereGrammarsOverlap(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	manifest := writeLadderManifest(t, fix, base)

	p := newPlanner(t, manifest, WithPlanComponent("web"))
	res, err := p.Plan(fix, "staging")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.HotfixVersionCandidate != "web1.2.0-rc.1.hotfix.1" {
		t.Errorf("HotfixVersionCandidate = %q, want web1.2.0-rc.1.hotfix.1: an alphabetic component prefix must survive the plan, not be re-rendered under the root prefix",
			res.HotfixVersionCandidate)
	}
}

// TestResolveEnvLadder_UndeclaredComponentRefused pins the hotfix package's
// undeclared-component policy. resolveFinalizeSpec already refuses a component
// the config does not declare, and the ladder resolver matches it: a hotfix
// plans and elevates along a component's resolved ladder, and an undeclared
// component has none to resolve. Silently falling back to the root ladder would
// let a typo'd --component run a full hotfix chain against the global ladder
// under a component-scoped branch namespace.
func TestResolveEnvLadder_UndeclaredComponentRefused(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	manifest := writeLadderManifest(t, fix, base)

	p := newPlanner(t, manifest, WithPlanComponent("nope"))
	_, err := p.Plan(fix, "staging")
	if err == nil {
		t.Fatal("a plan scoped to an undeclared component must be refused")
	}
	if !strings.Contains(err.Error(), "not declared") {
		t.Errorf("error = %v, want an undeclared-component refusal", err)
	}
}

// TestResolveEnvLadder_EmptyComponentKeepsGlobalLadder pins the
// single-component path as byte-identical: an empty component resolves the
// global ladder verbatim.
func TestResolveEnvLadder_EmptyComponentKeepsGlobalLadder(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	manifest := writeLadderManifest(t, fix, base)

	p := newPlanner(t, manifest)
	ladder, err := resolveEnvLadder(p.cicd, "")
	if err != nil {
		t.Fatalf("resolveEnvLadder: %v", err)
	}
	if strings.Join(ladder, ",") != "dev,test,staging,prod" {
		t.Errorf("ladder = %v, want the full global ladder", ladder)
	}
}

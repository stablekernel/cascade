package hotfix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// envSpec describes one environment row for writeManifestFull: its recorded
// state SHA, optional version override, and optional applied patch SHAs.
type envSpec struct {
	sha     string
	version string
	patches []string
}

// writeManifestFull writes a manifest with per-env state including optional
// version overrides and applied patch lists, and returns its path. It mirrors
// writeManifest but exposes the patches field the idempotency checks consult.
func writeManifestFull(t *testing.T, envs []string, state map[string]envSpec) string {
	t.Helper()

	var b strings.Builder
	b.WriteString("ci:\n")
	b.WriteString("  config:\n")
	b.WriteString("    environments:\n")
	for _, e := range envs {
		b.WriteString("      - " + e + "\n")
	}
	b.WriteString("  state:\n")
	for _, e := range envs {
		spec, ok := state[e]
		if !ok {
			continue
		}
		version := spec.version
		if version == "" {
			version = "v1.0.0-rc.1"
		}
		b.WriteString("    " + e + ":\n")
		b.WriteString("      sha: " + spec.sha + "\n")
		b.WriteString("      version: " + version + "\n")
		if len(spec.patches) > 0 {
			b.WriteString("      patches:\n")
			for _, p := range spec.patches {
				b.WriteString("        - " + p + "\n")
			}
		}
	}

	path := filepath.Join(".", "manifest.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// --- T1: parseCommitRefs ---------------------------------------------------

func TestParseCommitRefs(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr bool
	}{
		{name: "single ref", input: "abc123", want: []string{"abc123"}},
		{name: "two refs", input: "abc,def", want: []string{"abc", "def"}},
		{name: "trims whitespace", input: " abc , def ", want: []string{"abc", "def"}},
		{name: "preserves order", input: "c,a,b", want: []string{"c", "a", "b"}},
		{name: "empty input", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "empty entry", input: "abc,,def", wantErr: true},
		{name: "trailing comma", input: "abc,", wantErr: true},
		{name: "duplicate entry", input: "abc,def,abc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCommitRefs(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseCommitRefs(%q) = %v, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCommitRefs(%q): %v", tt.input, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseCommitRefs(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseCommitRefs(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- T3: expandTargetEnvs --------------------------------------------------

func TestExpandTargetEnvs(t *testing.T) {
	envs := []string{"dev", "test", "staging", "prod"}

	tests := []struct {
		name      string
		targetEnv string
		want      []string
		wantErr   bool
	}{
		{name: "staging stops at staging", targetEnv: "staging", want: []string{"test", "staging"}},
		{name: "prod includes whole chain above first", targetEnv: "prod", want: []string{"test", "staging", "prod"}},
		{name: "second env alone", targetEnv: "test", want: []string{"test"}},
		{name: "first env rejected", targetEnv: "dev", wantErr: true},
		{name: "unknown env rejected", targetEnv: "qa", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.TrunkConfig{Environments: envs}
			got, err := expandTargetEnvs(cfg, tt.targetEnv)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expandTargetEnvs(%q) = %v, want error", tt.targetEnv, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandTargetEnvs(%q): %v", tt.targetEnv, err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("expandTargetEnvs(%q) = %v, want %v", tt.targetEnv, got, tt.want)
			}
		})
	}
}

// --- T4: per-(commit,env) idempotent skip ----------------------------------

func TestPlan_SkipsCommitAlreadyInPatches(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix on trunk")

	// test's recorded state SHA is base (does NOT contain fix as an ancestor),
	// but the fix SHA is already listed in patches: this is the core gap. The
	// old no-op check only consulted state.SHA ancestry and would have planned
	// the cherry-pick again.
	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: fix},
		"test": {sha: base, patches: []string{fix}},
		"prod": {sha: base},
	})

	p := newPlanner(t, manifest)
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !res.NoOp {
		t.Errorf("expected NoOp=true when fix is already in state.patches, got %+v", res)
	}
}

func TestPlan_SkipsCommitAlreadyInStateSHA(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	tip := commitFile(t, "c.txt", "three", "later")

	// test points at tip, which already contains fix as an ancestor.
	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: base},
		"test": {sha: tip},
		"prod": {sha: base},
	})

	p := newPlanner(t, manifest)
	res, err := p.Plan(fix, "test")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !res.NoOp {
		t.Errorf("expected NoOp=true when fix is an ancestor of state.SHA, got %+v", res)
	}
}

// --- T5: PlanChain orchestration -------------------------------------------

func TestPlanChain_CleanSet(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix1 := commitFile(t, "b.txt", "two", "fix one")
	fix2 := commitFile(t, "c.txt", "three", "fix two")

	// envs: dev (first, excluded), test, staging, prod. Target staging => the
	// chain is [test, staging]. Neither env contains the fixes, so both are
	// planned with both commits in ref order.
	manifest := writeManifestFull(t, []string{"dev", "test", "staging", "prod"}, map[string]envSpec{
		"dev":     {sha: fix2},
		"test":    {sha: base},
		"staging": {sha: base},
		"prod":    {sha: base},
	})

	p := newPlanner(t, manifest)
	res, err := p.PlanChain([]string{fix1, fix2}, "staging")
	if err != nil {
		t.Fatalf("PlanChain: %v", err)
	}
	if len(res.Envs) != 2 {
		t.Fatalf("expected 2 env plans, got %d: %+v", len(res.Envs), res.Envs)
	}
	if res.Envs[0].Env != "test" || res.Envs[1].Env != "staging" {
		t.Errorf("env order = [%s,%s], want [test,staging]", res.Envs[0].Env, res.Envs[1].Env)
	}
	for _, ep := range res.Envs {
		if ep.NoOp {
			t.Errorf("env %s should not be a no-op", ep.Env)
		}
		if len(ep.Commits) != 2 {
			t.Errorf("env %s commits = %v, want 2 in ref order", ep.Env, ep.Commits)
		}
		if len(ep.Commits) == 2 && (ep.Commits[0] != fix1 || ep.Commits[1] != fix2) {
			t.Errorf("env %s commit order = %v, want [%s,%s]", ep.Env, ep.Commits, fix1, fix2)
		}
		if ep.Branch != "env/"+ep.Env {
			t.Errorf("env %s branch = %q", ep.Env, ep.Branch)
		}
		if ep.BaseSHA != base {
			t.Errorf("env %s base = %q, want %q", ep.Env, ep.BaseSHA, base)
		}
	}
}

func TestPlanChain_FullyPresentEnvIsNoOp(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix1 := commitFile(t, "b.txt", "two", "fix one")
	fix2 := commitFile(t, "c.txt", "three", "fix two")

	// test already carries both fixes via patches; staging carries neither.
	manifest := writeManifestFull(t, []string{"dev", "test", "staging", "prod"}, map[string]envSpec{
		"dev":     {sha: fix2},
		"test":    {sha: base, patches: []string{fix1, fix2}},
		"staging": {sha: base},
		"prod":    {sha: base},
	})

	p := newPlanner(t, manifest)
	res, err := p.PlanChain([]string{fix1, fix2}, "staging")
	if err != nil {
		t.Fatalf("PlanChain: %v", err)
	}
	if len(res.Envs) != 2 {
		t.Fatalf("expected 2 env plans, got %d", len(res.Envs))
	}
	test := res.Envs[0]
	if test.Env != "test" || !test.NoOp || len(test.Commits) != 0 {
		t.Errorf("test env should be a whole-set no-op, got %+v", test)
	}
	staging := res.Envs[1]
	if staging.Env != "staging" || staging.NoOp || len(staging.Commits) != 2 {
		t.Errorf("staging env should plan both commits, got %+v", staging)
	}
}

func TestPlanChain_MixedSetSkipsPerEnv(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix1 := commitFile(t, "b.txt", "two", "fix one")
	fix2 := commitFile(t, "c.txt", "three", "fix two")

	// test already has fix1 in patches; staging has neither. Both commits are
	// requested. Per (commit,env): test plans only fix2; staging plans both.
	manifest := writeManifestFull(t, []string{"dev", "test", "staging", "prod"}, map[string]envSpec{
		"dev":     {sha: fix2},
		"test":    {sha: base, patches: []string{fix1}},
		"staging": {sha: base},
		"prod":    {sha: base},
	})

	p := newPlanner(t, manifest)
	res, err := p.PlanChain([]string{fix1, fix2}, "staging")
	if err != nil {
		t.Fatalf("PlanChain: %v", err)
	}
	test := res.Envs[0]
	if test.Env != "test" || test.NoOp {
		t.Fatalf("test env should still plan a partial set, got %+v", test)
	}
	if len(test.Commits) != 1 || test.Commits[0] != fix2 {
		t.Errorf("test commits = %v, want [%s] (fix1 already applied)", test.Commits, fix2)
	}
	staging := res.Envs[1]
	if len(staging.Commits) != 2 {
		t.Errorf("staging commits = %v, want both", staging.Commits)
	}
}

func TestChainGHAOutputs_CarriesSequence(t *testing.T) {
	result := &PlanChainResult{
		Envs: []EnvPlan{
			{Env: "test", Branch: "env/test", BaseSHA: "base", Commits: []string{"aaa", "bbb"}},
			{Env: "staging", Branch: "env/staging", BaseSHA: "base", Commits: nil, NoOp: true},
		},
	}

	simple, _ := chainGHAOutputs(result)

	want := map[string]string{
		"env_sequence":              "test,staging",
		"env_count":                 "2",
		"commits_test":              "aaa,bbb",
		"no_op_test":                "false",
		"conflict_expected_test":    "false",
		"commits_staging":           "",
		"no_op_staging":             "true",
		"conflict_expected_staging": "false",
	}
	for k, v := range want {
		if got := simple[k]; got != v {
			t.Errorf("chainGHAOutputs[%q] = %q, want %q", k, got, v)
		}
	}
}

func TestPlanChain_RejectsNonTrunkCommit(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix1 := commitFile(t, "b.txt", "two", "fix one")
	runGit(t, "checkout", "-b", "side")
	side := commitFile(t, "d.txt", "four", "side fix")
	runGit(t, "checkout", "main")

	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: fix1},
		"test": {sha: base},
		"prod": {sha: base},
	})

	p := newPlanner(t, manifest)
	_, err := p.PlanChain([]string{fix1, side}, "test")
	if err == nil {
		t.Fatal("expected error citing the non-trunk ref")
	}
	if !strings.Contains(err.Error(), "trunk") {
		t.Errorf("error %q should mention trunk", err.Error())
	}
}

// setRemoteEnvTip points the remote-tracking ref the generated workflow fetches
// (refs/remotes/origin/env/<env>) at sha, simulating a fetched remote env branch
// whose tip the planner can inspect. Using update-ref avoids needing a real
// second repository to act as origin.
func setRemoteEnvTip(t *testing.T, env, sha string) {
	t.Helper()
	runGit(t, "update-ref", "refs/remotes/origin/env/"+env, sha)
}

// short7 mirrors the planner's short() truncation so the divergence assertions
// can check that both SHAs surface in the operator-facing error.
func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// TestPlanChain_RemoteEnvTipDiverged_FailsWithGuidance asserts the planner
// aborts when the fetched remote env/<env> tip no longer matches the recorded
// state SHA the cherry-pick base is derived from. Without this guard the apply
// job cherry-picks onto the stale base and opens a PR GitHub finds unmergeable,
// surfacing only as a merge-poll timeout.
func TestPlanChain_RemoteEnvTipDiverged_FailsWithGuidance(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	diverged := commitFile(t, "b.txt", "two", "out-of-band env edit")
	fix := commitFile(t, "c.txt", "three", "fix on trunk")

	// test records base as its state SHA, but the remote env/test tip has moved
	// on to diverged: recorded state and the branch no longer agree.
	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: fix},
		"test": {sha: base},
		"prod": {sha: base},
	})
	setRemoteEnvTip(t, "test", diverged)

	p := newPlanner(t, manifest)
	_, err := p.PlanChain([]string{fix}, "test")
	if err == nil {
		t.Fatal("expected divergence error when remote env tip differs from recorded state SHA")
	}
	msg := err.Error()
	if !strings.Contains(msg, "env/test") {
		t.Errorf("error %q should name the diverged env branch", msg)
	}
	if !strings.Contains(msg, short7(diverged)) || !strings.Contains(msg, short7(base)) {
		t.Errorf("error %q should report both the branch tip %s and the recorded SHA %s",
			msg, short7(diverged), short7(base))
	}
	// With no real PRChecker injected the single-flight gate did not run, so the
	// planner must stay fail-closed (no self-heal) and surface actionable
	// recovery guidance.
	if !strings.Contains(msg, "abandoned hotfix branch") {
		t.Errorf("error %q should describe the abandoned branch", msg)
	}
	if !strings.Contains(msg, "--repo") {
		t.Errorf("error %q should point at re-running with --repo", msg)
	}
}

// TestPlanChain_RemoteEnvTipInSync_PlansCleanly is the negative control: when
// the remote env/<env> tip equals the recorded state SHA the plan proceeds
// exactly as before, proving the guard fires only on genuine divergence.
func TestPlanChain_RemoteEnvTipInSync_PlansCleanly(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "c.txt", "three", "fix on trunk")

	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: fix},
		"test": {sha: base},
		"prod": {sha: base},
	})
	// Remote env tip agrees with recorded state: no divergence.
	setRemoteEnvTip(t, "test", base)

	p := newPlanner(t, manifest)
	res, err := p.PlanChain([]string{fix}, "test")
	if err != nil {
		t.Fatalf("PlanChain with in-sync remote tip should succeed: %v", err)
	}
	if len(res.Envs) != 1 || res.Envs[0].Env != "test" {
		t.Fatalf("expected a single test env plan, got %+v", res.Envs)
	}
	ep := res.Envs[0]
	if ep.NoOp || len(ep.Commits) != 1 || ep.Commits[0] != fix {
		t.Errorf("test env should plan the fix unchanged, got %+v", ep)
	}
	if ep.BaseSHA != base {
		t.Errorf("base SHA = %q, want %q", ep.BaseSHA, base)
	}
}

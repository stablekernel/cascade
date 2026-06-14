package rollback

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/promote"
)

// seedRing sets the deploy-history ring on the env's live state, newest first.
// Tests use it to populate the Previous ring that resolveTarget consults
// between live state and git history.
func seedRing(t *testing.T, rb *Rollbacker, env string, ring []config.EnvStateSnapshot) {
	t.Helper()
	st := rb.cicdFile.State[env]
	if st == nil {
		t.Fatalf("no live state for env %q to seed ring", env)
	}
	st.Previous = ring
}

func TestResolveTarget_RingDepth1(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "currentsha12345", "v3.0.0")
	rb := newRollbacker(t, path, fakeHistory{})
	seedRing(t, rb, "prod", []config.EnvStateSnapshot{
		{SHA: "ringsha0000001", Version: "v2.0.0"},
	})

	plan, err := rb.Plan("prod", "v2.0.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "ringsha0000001" {
		t.Errorf("target sha = %q, want ringsha0000001", plan.Target.SHA)
	}
	if plan.Target.Source != "previous-ring" {
		t.Errorf("source = %q, want previous-ring", plan.Target.Source)
	}
}

func TestResolveTarget_RingDepthN(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "currentsha12345", "v4.0.0")
	rb := newRollbacker(t, path, fakeHistory{})
	seedRing(t, rb, "prod", []config.EnvStateSnapshot{
		{SHA: "ringsha0000003", Version: "v3.0.0"},
		{SHA: "ringsha0000002", Version: "v2.0.0"},
		{SHA: "ringsha0000001", Version: "v1.0.0"},
	})

	// Match the deepest entry (an N-3) by version.
	plan, err := rb.Plan("prod", "v1.0.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "ringsha0000001" {
		t.Errorf("target sha = %q, want ringsha0000001", plan.Target.SHA)
	}
	if plan.Target.Source != "previous-ring" {
		t.Errorf("source = %q, want previous-ring", plan.Target.Source)
	}
}

func TestResolveTarget_ExplicitVersionStillWorks(t *testing.T) {
	dir := t.TempDir()
	// Live state itself carries the requested version; live state wins.
	path := writeManifest(t, dir, "currentsha12345", "v3.0.0")
	rb := newRollbacker(t, path, fakeHistory{})
	seedRing(t, rb, "prod", []config.EnvStateSnapshot{
		{SHA: "ringsha0000001", Version: "v2.0.0"},
	})

	plan, err := rb.Plan("prod", "v3.0.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.Source != "state" {
		t.Errorf("source = %q, want state (live state wins over ring)", plan.Target.Source)
	}
	if plan.Target.SHA != "currentsha12345" {
		t.Errorf("target sha = %q, want currentsha12345", plan.Target.SHA)
	}
}

func TestResolveTarget_ExplicitShaPrefixStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "currentsha12345", "v4.0.0")
	rb := newRollbacker(t, path, fakeHistory{})
	seedRing(t, rb, "prod", []config.EnvStateSnapshot{
		{SHA: "ringsha0000001", Version: "v1.0.0"},
	})

	// Short (>=7 char) SHA prefix resolves the full ring SHA.
	plan, err := rb.Plan("prod", "ringsha", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "ringsha0000001" {
		t.Errorf("target sha = %q, want ringsha0000001", plan.Target.SHA)
	}
	if plan.Target.Source != "previous-ring" {
		t.Errorf("source = %q, want previous-ring", plan.Target.Source)
	}
}

func TestResolveTarget_GitFallbackWhenRingMissing(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "currentsha12345", "v4.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {
			{SHA: "gitsha00000001", Version: "v1.0.0"},
		},
	}}
	rb := newRollbacker(t, path, hist)
	// Ring holds an unrelated version; the requested one lives only in git.
	seedRing(t, rb, "prod", []config.EnvStateSnapshot{
		{SHA: "ringsha0000002", Version: "v2.0.0"},
	})

	plan, err := rb.Plan("prod", "v1.0.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "gitsha00000001" {
		t.Errorf("target sha = %q, want gitsha00000001", plan.Target.SHA)
	}
	if plan.Target.Source != "git-history" {
		t.Errorf("source = %q, want git-history", plan.Target.Source)
	}
}

func TestResolveTarget_DefaultPicksN1FromRing(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "currentsha12345", "v3.0.0")
	rb := newRollbacker(t, path, fakeHistory{})
	seedRing(t, rb, "prod", []config.EnvStateSnapshot{
		{SHA: "ringsha0000002", Version: "v2.0.0"},
		{SHA: "ringsha0000001", Version: "v1.0.0"},
	})

	// Empty --to: default to the newest distinct ring entry (the N-1).
	plan, err := rb.Plan("prod", "", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "ringsha0000002" {
		t.Errorf("default target sha = %q, want ringsha0000002 (N-1)", plan.Target.SHA)
	}
	if plan.Target.Source != "previous-ring" {
		t.Errorf("source = %q, want previous-ring", plan.Target.Source)
	}
}

func TestResolveTarget_DefaultFallsBackToGitWhenRingEmpty(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "currentsha12345", "v3.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {
			{SHA: "gitsha00000001", Version: "v2.0.0"},
		},
	}}
	rb := newRollbacker(t, path, hist)
	// No ring: default falls back to the newest distinct git-history entry.

	plan, err := rb.Plan("prod", "", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "gitsha00000001" {
		t.Errorf("default target sha = %q, want gitsha00000001", plan.Target.SHA)
	}
	if plan.Target.Source != "git-history" {
		t.Errorf("source = %q, want git-history", plan.Target.Source)
	}
}

func TestResolveTarget_UnresolvableReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "currentsha12345", "v3.0.0")
	rb := newRollbacker(t, path, fakeHistory{})
	// Empty ring, empty git history.

	_, err := rb.Plan("prod", "", "")
	if err == nil {
		t.Fatal("expected error when no prior version to roll back to, got nil")
	}
	if !strings.Contains(err.Error(), "no prior version to roll back to") {
		t.Errorf("error = %q, want it to mention no prior version to roll back to", err.Error())
	}
}

func TestApply_MarksEnvDivergedWithRollbackRef(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "currentsha12345", "v3.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {
			{SHA: "priorgoodsha01", Version: "v2.0.0",
				Deploys: map[string]*config.DeployState{
					"services": {SHA: "priorgoodsha01", Version: "v2.0.0"},
				}},
		},
	}}
	rb := newRollbacker(t, path, hist)

	plan, err := rb.Plan("prod", "v2.0.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := rb.Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	prod := file.State["prod"]
	if !prod.IsDiverged() {
		t.Errorf("expected env to be diverged after rollback Apply")
	}
	if !strings.HasPrefix(prod.Ref, promote.RollbackRefPrefix) {
		t.Errorf("ref = %q, want prefix %q", prod.Ref, promote.RollbackRefPrefix)
	}
	if prod.Ref != "rollback/prod" {
		t.Errorf("ref = %q, want rollback/prod", prod.Ref)
	}
	// BaseSHA records the pre-rollback (outgoing) current SHA.
	if prod.BaseSHA != "currentsha12345" {
		t.Errorf("base_sha = %q, want currentsha12345 (pre-rollback SHA)", prod.BaseSHA)
	}
	if len(prod.Patches) != 0 {
		t.Errorf("patches = %v, want empty (rollback sets no patches)", prod.Patches)
	}
}

func TestApply_DeployableScopedDoesNotMarkEnvDiverged(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "currentsha12345", "v3.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {
			{SHA: "priorgoodsha01", Version: "v2.0.0",
				Deploys: map[string]*config.DeployState{
					"services": {SHA: "svcsha111", Version: "v2.0.0-svc"},
				}},
		},
	}}
	rb := newRollbacker(t, path, hist)

	plan, err := rb.Plan("prod", "v2.0.0-svc", "services")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := rb.Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	prod := file.State["prod"]
	// A deployable-scoped rollback must not mark the env-level state diverged.
	if prod.Ref != "" {
		t.Errorf("env ref = %q, want empty (deployable scope must not touch env-level divergence)", prod.Ref)
	}
	if prod.IsDiverged() {
		t.Errorf("env unexpectedly diverged after deployable-scoped rollback")
	}
}

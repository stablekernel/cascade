package rollback

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// writeManifest writes a manifest with two environments and per-deployable
// state, and returns its path. prodSHA/prodVersion seed prod's live state.
func writeManifest(t *testing.T, dir, prodSHA, prodVersion string) string {
	t.Helper()
	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    deploys:
      - name: services
        workflow: .github/workflows/deploy.yaml
        triggers:
          - "deploy/**"
  state:
    dev:
      sha: devsha1234567
      version: v2.0.0-rc.1
      committed_at: "2026-02-01T10:00:00Z"
      committed_by: alice
    prod:
      sha: ` + prodSHA + `
      version: ` + prodVersion + `
      committed_at: "2026-02-01T11:00:00Z"
      committed_by: alice
      deploys:
        services:
          sha: ` + prodSHA + `
          version: ` + prodVersion + `
          deployed_at: "2026-02-01T11:05:00Z"
          deployed_by: alice
`
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// fakeHistory is a deterministic HistoryReader for tests.
type fakeHistory struct {
	states map[string][]*config.EnvState
}

func (f fakeHistory) PriorStates(env string) ([]*config.EnvState, error) {
	return f.states[env], nil
}

func newRollbacker(t *testing.T, path string, hist HistoryReader) *Rollbacker {
	t.Helper()
	rb, err := New(Options{ConfigPath: path, Actor: "tester", HistoryReader: hist})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return rb
}

func TestPlan_ResolvesTargetFromState_ByVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	plan, err := rb.Plan("prod", "v1.9.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "prodsha9999999" {
		t.Errorf("target sha = %q, want prodsha9999999", plan.Target.SHA)
	}
	if plan.Target.Source != "state" {
		t.Errorf("source = %q, want state", plan.Target.Source)
	}
	if !plan.NoOp {
		t.Errorf("expected NoOp (target matches current state)")
	}
}

func TestPlan_ResolvesTargetFromState_ByShaPrefix(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "abcdef1234567", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	plan, err := rb.Plan("prod", "abcdef1", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "abcdef1234567" {
		t.Errorf("target sha = %q, want abcdef1234567", plan.Target.SHA)
	}
}

func TestPlan_ResolvesTargetFromHistory(t *testing.T) {
	dir := t.TempDir()
	// Live prod is at v2.0.0; the rollback target lives only in history.
	path := writeManifest(t, dir, "newprodsha1234", "v2.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {
			{SHA: "oldprodsha5678", Version: "v1.8.0",
				Deploys: map[string]*config.DeployState{
					"services": {SHA: "oldprodsha5678", Version: "v1.8.0"},
				}},
		},
	}}
	rb := newRollbacker(t, path, hist)

	plan, err := rb.Plan("prod", "v1.8.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "oldprodsha5678" {
		t.Errorf("target sha = %q, want oldprodsha5678", plan.Target.SHA)
	}
	if plan.Target.Source != "git-history" {
		t.Errorf("source = %q, want git-history", plan.Target.Source)
	}
	if plan.NoOp {
		t.Errorf("did not expect NoOp; prod is at a different version")
	}
	if plan.CurrentSHA != "newprodsha1234" {
		t.Errorf("current sha = %q, want newprodsha1234", plan.CurrentSHA)
	}
}

func TestPlan_DeployableScoping(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "newprodsha1234", "v2.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {
			{SHA: "oldprodsha5678", Version: "v1.8.0",
				Deploys: map[string]*config.DeployState{
					"services": {SHA: "svcsha111", Version: "v1.8.0-svc"},
				}},
		},
	}}
	rb := newRollbacker(t, path, hist)

	plan, err := rb.Plan("prod", "v1.8.0-svc", "services")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Deployable != "services" {
		t.Errorf("deployable = %q, want services", plan.Deployable)
	}
	if plan.Target.SHA != "svcsha111" {
		t.Errorf("target sha = %q, want svcsha111 (per-deployable)", plan.Target.SHA)
	}
	if plan.Target.Deployable != "services" {
		t.Errorf("target deployable = %q, want services", plan.Target.Deployable)
	}
}

func TestPlan_UnknownTargetError(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	_, err := rb.Plan("prod", "v9.9.9-does-not-exist", "")
	if err == nil {
		t.Fatal("expected error for unknown target, got nil")
	}
}

func TestPlan_UnknownEnvironmentError(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	_, err := rb.Plan("nonexistent-env", "v1.9.0", "")
	if err == nil {
		t.Fatal("expected error for unknown environment, got nil")
	}
}

func TestPlan_UnknownDeployableError(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	_, err := rb.Plan("prod", "v1.9.0", "ghost-deployable")
	if err == nil {
		t.Fatal("expected error for unknown deployable, got nil")
	}
}

func TestPlan_RequiresEnvAndTo(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	if _, err := rb.Plan("", "v1.9.0", ""); err == nil {
		t.Error("expected error for empty env")
	}
	if _, err := rb.Plan("prod", "", ""); err == nil {
		t.Error("expected error for empty --to")
	}
}

func TestApply_WritesEnvAndDeployState(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "newprodsha1234", "v2.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {
			{SHA: "oldprodsha5678", Version: "v1.8.0",
				Deploys: map[string]*config.DeployState{
					"services": {SHA: "oldprodsha5678", Version: "v1.8.0"},
				}},
		},
	}}
	rb := newRollbacker(t, path, hist)

	plan, err := rb.Plan("prod", "v1.8.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := rb.Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Re-parse from disk to confirm persistence.
	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	prod := file.State["prod"]
	if prod.SHA != "oldprodsha5678" {
		t.Errorf("prod sha = %q, want oldprodsha5678", prod.SHA)
	}
	if prod.Version != "v1.8.0" {
		t.Errorf("prod version = %q, want v1.8.0", prod.Version)
	}
	if prod.CommittedBy != "tester" {
		t.Errorf("committed_by = %q, want tester", prod.CommittedBy)
	}
	// The env-scoped rollback mirrors onto recorded deployables.
	if ds := prod.Deploys["services"]; ds == nil || ds.SHA != "oldprodsha5678" {
		t.Errorf("deployable services not mirrored: %+v", ds)
	}
}

func TestApply_DeployableScopedDoesNotTouchEnvPointer(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "newprodsha1234", "v2.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {
			{SHA: "oldprodsha5678", Version: "v1.8.0",
				Deploys: map[string]*config.DeployState{
					"services": {SHA: "svcsha111", Version: "v1.8.0-svc"},
				}},
		},
	}}
	rb := newRollbacker(t, path, hist)

	plan, err := rb.Plan("prod", "v1.8.0-svc", "services")
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
	// Env pointer is untouched by a deployable-scoped rollback.
	if prod.SHA != "newprodsha1234" {
		t.Errorf("env pointer changed: sha = %q, want newprodsha1234", prod.SHA)
	}
	if ds := prod.Deploys["services"]; ds == nil || ds.SHA != "svcsha111" {
		t.Errorf("deployable not rolled back: %+v", ds)
	}
}

func TestApply_NoOpIsNonDestructive(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	plan, err := rb.Plan("prod", "v1.9.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.NoOp {
		t.Fatal("expected NoOp plan")
	}
	if err := rb.Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Error("no-op Apply mutated the manifest")
	}
}

func TestShaMatches(t *testing.T) {
	cases := []struct {
		candidate, requested string
		want                 bool
	}{
		{"abcdef1234567", "abcdef1234567", true},
		{"abcdef1234567", "abcdef1", true},
		{"abcdef1234567", "abcde", false}, // too short for prefix match
		{"abcdef1234567", "zzzzzzz", false},
		{"", "abcdef1", false},
		{"abcdef1234567", "", false},
	}
	for _, c := range cases {
		if got := shaMatches(c.candidate, c.requested); got != c.want {
			t.Errorf("shaMatches(%q,%q) = %v, want %v", c.candidate, c.requested, got, c.want)
		}
	}
}

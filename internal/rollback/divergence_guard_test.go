package rollback

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// writeDivergedManifest writes a manifest whose prod env carries a divergence
// record (ref/base_sha/patches) alongside a deploy-history ring entry, so a
// rollback target is resolvable and the guard is the only thing standing
// between Plan and a successful resolution.
func writeDivergedManifest(t *testing.T, dir, ref string, patches []string) string {
	t.Helper()
	var patchBlock string
	if len(patches) > 0 {
		patchBlock = "      patches:\n"
		for _, p := range patches {
			patchBlock += "        - " + p + "\n"
		}
	}
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
      sha: hotfixmergesha
      version: v2.0.0-hf.1
      committed_at: "2026-02-01T11:00:00Z"
      committed_by: alice
      ref: ` + ref + `
      base_sha: hotfixbasesha1
` + patchBlock + `      previous:
        - sha: priorgoodsha01
          version: v1.9.0
          committed_at: "2026-01-01T10:00:00Z"
          committed_by: alice
`
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// A hotfix-diverged environment's divergence record (ref, base_sha, patches) is
// the only thing that authorizes the rejoin teardown of its integration branch,
// hotfix tags, and release objects. An env-scoped rollback overwrites ref and
// base_sha and would leave those artifacts stranded forever, so Plan must
// refuse it and direct the operator to rejoin first.
func TestPlan_RefusesEnvScopedRollbackOfHotfixDivergedEnv(t *testing.T) {
	dir := t.TempDir()
	path := writeDivergedManifest(t, dir, "env/prod", []string{"fixsha00000001"})
	rb := newRollbacker(t, path, fakeHistory{})

	_, err := rb.Plan("prod", "", "")
	if err == nil {
		t.Fatal("expected Plan to refuse an env-scoped rollback of a hotfix-diverged env, got nil")
	}
	for _, want := range []string{"diverged", "env/prod", "rejoin", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
	}
}

// The component-namespaced hotfix ref (env/<component>/<env>) is refused the
// same way as the single-component form.
func TestPlan_RefusesRollbackOfComponentHotfixDivergedEnv(t *testing.T) {
	dir := t.TempDir()
	path := writeDivergedManifest(t, dir, "env/api/prod", []string{"fixsha00000001"})
	rb := newRollbacker(t, path, fakeHistory{})

	_, err := rb.Plan("prod", "", "")
	if err == nil {
		t.Fatal("expected Plan to refuse a rollback of a hotfix-diverged env, got nil")
	}
	if !strings.Contains(err.Error(), "env/api/prod") {
		t.Errorf("error = %q, want it to name the divergence ref env/api/prod", err.Error())
	}
}

// Apply enforces the same guard as Plan, so a caller that constructs a Plan by
// hand (or a stale Plan raced by a concurrent hotfix finalize) cannot overwrite
// a hotfix divergence record either. The manifest must be left untouched.
func TestApply_RefusesEnvScopedRollbackOfHotfixDivergedEnv(t *testing.T) {
	dir := t.TempDir()
	path := writeDivergedManifest(t, dir, "env/prod", []string{"fixsha00000001"})
	rb := newRollbacker(t, path, fakeHistory{})

	plan := &Plan{
		Environment: "prod",
		Target:      Target{SHA: "priorgoodsha01", Version: "v1.9.0", Source: "previous-ring"},
	}
	if err := rb.Apply(plan); err == nil {
		t.Fatal("expected Apply to refuse an env-scoped rollback of a hotfix-diverged env, got nil")
	}

	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	prod := file.State["prod"]
	if prod.Ref != "env/prod" {
		t.Errorf("ref = %q, want env/prod (divergence record must survive a refused rollback)", prod.Ref)
	}
	if len(prod.Patches) != 1 || prod.Patches[0] != "fixsha00000001" {
		t.Errorf("patches = %v, want [fixsha00000001] intact", prod.Patches)
	}
	if prod.SHA != "hotfixmergesha" {
		t.Errorf("sha = %q, want hotfixmergesha (state must be unchanged)", prod.SHA)
	}
}

// A rollback-diverged env (ref rollback/<env>) may be rolled back again: its
// divergence record authorizes no teardown, so overwriting it loses nothing.
func TestPlanAndApply_AllowRollbackOfRollbackDivergedEnv(t *testing.T) {
	dir := t.TempDir()
	path := writeDivergedManifest(t, dir, "rollback/prod", nil)
	rb := newRollbacker(t, path, fakeHistory{})

	plan, err := rb.Plan("prod", "", "")
	if err != nil {
		t.Fatalf("Plan on a rollback-diverged env: %v", err)
	}
	if err := rb.Apply(plan); err != nil {
		t.Fatalf("Apply on a rollback-diverged env: %v", err)
	}

	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	prod := file.State["prod"]
	if prod.Ref != "rollback/prod" {
		t.Errorf("ref = %q, want rollback/prod", prod.Ref)
	}
	if prod.SHA != "priorgoodsha01" {
		t.Errorf("sha = %q, want priorgoodsha01", prod.SHA)
	}
	if len(prod.Patches) != 0 {
		t.Errorf("patches = %v, want empty", prod.Patches)
	}
}

// Patches assert that specific fix commits are deployed in the env. A rollback
// re-points the env at a prior SHA, so any recorded patches are stale the
// moment it applies; Apply clears them explicitly rather than trusting every
// possible input state (for example a hand-edited manifest carrying patches
// under a rollback ref).
func TestApply_ClearsStalePatchesOnRollbackDivergedEnv(t *testing.T) {
	dir := t.TempDir()
	path := writeDivergedManifest(t, dir, "rollback/prod", []string{"stalefixsha001"})
	rb := newRollbacker(t, path, fakeHistory{})

	plan, err := rb.Plan("prod", "", "")
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
	if len(prod.Patches) != 0 {
		t.Errorf("patches = %v, want empty (stale patches must not survive onto the rolled-back version)", prod.Patches)
	}
}

// A deployable-scoped rollback re-points one deployable's recorded SHA without
// touching the env-level pointer or its divergence record, so the hotfix
// teardown authorization survives and the guard does not apply.
func TestPlan_AllowsDeployableScopedRollbackOnHotfixDivergedEnv(t *testing.T) {
	dir := t.TempDir()
	path := writeDivergedManifest(t, dir, "env/prod", []string{"fixsha00000001"})
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {
			{SHA: "priordeploysha", Version: "v1.9.0",
				Deploys: map[string]*config.DeployState{
					"services": {SHA: "priordeploysha", Version: "v1.9.0"},
				}},
		},
	}}
	rb := newRollbacker(t, path, hist)

	plan, err := rb.Plan("prod", "", "services")
	if err != nil {
		t.Fatalf("Plan (deployable-scoped): %v", err)
	}
	if err := rb.Apply(plan); err != nil {
		t.Fatalf("Apply (deployable-scoped): %v", err)
	}

	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	prod := file.State["prod"]
	if prod.Ref != "env/prod" {
		t.Errorf("ref = %q, want env/prod (deployable scope must not touch the divergence record)", prod.Ref)
	}
	if len(prod.Patches) != 1 {
		t.Errorf("patches = %v, want the recorded patch intact", prod.Patches)
	}
}

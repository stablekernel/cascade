package rollback

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/promote"
	"github.com/stablekernel/cascade/internal/statewrite"
)

// ringManifest writes a manifest whose prod env carries a deploy-history ring
// with a distinct N-1 entry, so default-target (N-1) resolution and explicit
// resolution both have something to find without needing git history.
func ringManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	rel := filepath.Join(dir, "manifest.yaml")
	content := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    deploys:
      - name: services
        workflow: .github/workflows/deploy.yaml
  state:
    prod:
      sha: prodnew7654321
      version: v2.0.0
      committed_at: "2026-03-01T11:00:00Z"
      committed_by: alice
      previous:
        - sha: prodold1112223
          version: v1.9.0
          committed_at: "2026-02-15T11:00:00Z"
          committed_by: alice
`
	if err := os.WriteFile(rel, []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return rel
}

func TestRollbackPreflight_GHAOutput_EmitsTargetEnvShaVersion(t *testing.T) {
	path := ringManifest(t)
	outFile := filepath.Join(t.TempDir(), "gha_output")
	t.Setenv("GITHUB_OUTPUT", outFile)

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"preflight",
		"--config", path,
		"--env", "prod",
		"--to", "v1.9.0",
		"--gha-output",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read gha output: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"target_env=prod",
		"target_sha=prodold1112223",
		"target_version=v1.9.0",
		"can_proceed=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("gha output missing %q\n%s", want, got)
		}
	}
}

func TestRollbackPreflight_DefaultTarget_UsesRing(t *testing.T) {
	path := ringManifest(t)
	outFile := filepath.Join(t.TempDir(), "gha_output")
	t.Setenv("GITHUB_OUTPUT", outFile)

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"preflight",
		"--config", path,
		"--env", "prod",
		"--gha-output",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read gha output: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "target_sha=prodold1112223") {
		t.Errorf("default target did not resolve N-1 from ring\n%s", got)
	}
	if !strings.Contains(got, "can_proceed=true") {
		t.Errorf("can_proceed not true\n%s", got)
	}
}

func TestRollbackPreflight_UnresolvableEmitsCannotProceed(t *testing.T) {
	path := ringManifest(t)
	outFile := filepath.Join(t.TempDir(), "gha_output")
	t.Setenv("GITHUB_OUTPUT", outFile)

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"preflight",
		"--config", path,
		"--env", "prod",
		"--to", "v0.0.0-nope",
		"--gha-output",
	})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for unresolvable target")
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read gha output: %v", err)
	}
	if !strings.Contains(string(data), "can_proceed=false") {
		t.Errorf("expected can_proceed=false on failure\n%s", string(data))
	}
}

// noDeployManifest writes a manifest whose prod env has NO configured deploys,
// so a rollback is state-only and has no deploy job to gate on. Used to assert
// the deploy-less rollback path still applies.
func noDeployManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	rel := filepath.Join(dir, "manifest.yaml")
	content := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
  state:
    prod:
      sha: prodnew7654321
      version: v2.0.0
      committed_at: "2026-03-01T11:00:00Z"
      committed_by: alice
      previous:
        - sha: prodold1112223
          version: v1.9.0
          committed_at: "2026-02-15T11:00:00Z"
          committed_by: alice
`
	if err := os.WriteFile(rel, []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return rel
}

// twoDeployManifest writes a manifest whose prod env declares two deploys
// (services, web-api), used to assert deployable-scoped finalize only gates on
// the in-scope deploy and ignores the excluded (skipped) one.
func twoDeployManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	rel := filepath.Join(dir, "manifest.yaml")
	content := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    deploys:
      - name: services
        workflow: .github/workflows/deploy-services.yaml
      - name: web-api
        workflow: .github/workflows/deploy-web-api.yaml
  state:
    prod:
      sha: prodnew7654321
      version: v2.0.0
      committed_at: "2026-03-01T11:00:00Z"
      committed_by: alice
      deploys:
        services:
          sha: prodold1112223
          version: v1.9.0
          deployed_at: "2026-02-15T11:00:00Z"
          deployed_by: alice
`
	if err := os.WriteFile(rel, []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return rel
}

func TestRollbackFinalize_SkippedDeployableNotCounted(t *testing.T) {
	path := twoDeployManifest(t)

	// The rollback is scoped to "services" (--deployable services), which
	// succeeded. "web-api" is excluded by the filter and its job reports
	// "skipped"; that must not abort the in-scope rollback.
	t.Setenv("DEPLOY_RESULT_SERVICES", "success")
	t.Setenv("DEPLOY_RESULT_WEB_API", "skipped")

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"finalize",
		"--config", path,
		"--env", "prod",
		"--to", "prodold1112223",
		"--deployable", "services",
		"--actor", "oncall",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	prod := file.State["prod"]
	if prod == nil {
		t.Fatal("prod state missing after finalize")
	}
	// A deployable-scoped rollback re-applies the per-deployable SHA without
	// touching the env-level pointer. With "services" succeeding and "web-api"
	// skipped, the gate must let the write through and the deployable state is
	// re-applied at the prior SHA.
	ds := prod.Deploys["services"]
	if ds == nil {
		t.Fatal("services deploy state missing after finalize")
	}
	if ds.SHA != "prodold1112223" {
		t.Errorf("services sha = %q, want prodold1112223", ds.SHA)
	}
}

func TestRollbackFinalize_AppliesWhenDeploySucceeded(t *testing.T) {
	path := ringManifest(t)

	// The manifest declares a "services" deploy, so finalize gates on its
	// result. A successful deploy must let the state write through.
	t.Setenv("DEPLOY_RESULT_SERVICES", "success")

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"finalize",
		"--config", path,
		"--env", "prod",
		"--to", "prodold1112223",
		"--actor", "oncall",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	prod := file.State["prod"]
	if prod == nil {
		t.Fatal("prod state missing after finalize")
	}
	if prod.SHA != "prodold1112223" {
		t.Errorf("env sha = %q, want prodold1112223", prod.SHA)
	}
	if !prod.IsDiverged() {
		t.Error("env should be diverged after rollback finalize")
	}
	if !promote.IsRollbackRef(prod.Ref) {
		t.Errorf("ref %q is not a rollback ref", prod.Ref)
	}
}

func TestRollbackFinalize_AbortsWhenDeployFailed(t *testing.T) {
	path := ringManifest(t)

	// Capture the on-disk manifest before finalize so we can prove it is
	// untouched when the deploy failed.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest before: %v", err)
	}

	// The "services" deploy job failed: finalize must abort and not write state.
	t.Setenv("DEPLOY_RESULT_SERVICES", "failure")

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"finalize",
		"--config", path,
		"--env", "prod",
		"--to", "prodold1112223",
		"--actor", "oncall",
	})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error when a deploy failed, got nil")
	}
	if !strings.Contains(err.Error(), "services") || !strings.Contains(err.Error(), "did not succeed") {
		t.Errorf("error %q should name the failed deploy and say it did not succeed", err.Error())
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("manifest changed after aborted finalize:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestRollbackFinalize_AbortsWhenNoInScopeDeploySucceeded(t *testing.T) {
	path := ringManifest(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest before: %v", err)
	}

	// No DEPLOY_RESULT_* is set: the deploy reports no success at all (treated
	// as skipped/empty). Nothing deployed, so finalize must not mark rolled-back.
	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"finalize",
		"--config", path,
		"--env", "prod",
		"--to", "prodold1112223",
		"--actor", "oncall",
	})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when no in-scope deploy succeeded, got nil")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("manifest changed after aborted finalize:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestRollbackFinalize_NoDeploysConfigured_StillApplies(t *testing.T) {
	path := noDeployManifest(t)

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"finalize",
		"--config", path,
		"--env", "prod",
		"--to", "prodold1112223",
		"--actor", "oncall",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	prod := file.State["prod"]
	if prod == nil {
		t.Fatal("prod state missing after finalize")
	}
	if prod.SHA != "prodold1112223" {
		t.Errorf("env sha = %q, want prodold1112223", prod.SHA)
	}
	if !promote.IsRollbackRef(prod.Ref) {
		t.Errorf("ref %q is not a rollback ref", prod.Ref)
	}
}

// TestRollbackPreflight_GHAOutput_EmitsTargetSource_State asserts that when
// the requested --to resolves against the live state (Source="state"), the
// preflight gha-output includes target_source=state.
func TestRollbackPreflight_GHAOutput_EmitsTargetSource_State(t *testing.T) {
	path := ringManifest(t)
	outFile := filepath.Join(t.TempDir(), "gha_output")
	t.Setenv("GITHUB_OUTPUT", outFile)

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	// v2.0.0 is the live state version, so the resolver hits state first.
	cmd.SetArgs([]string{
		"preflight",
		"--config", path,
		"--env", "prod",
		"--to", "v2.0.0",
		"--gha-output",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read gha output: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "target_source=state") {
		t.Errorf("gha output missing target_source=state\n%s", got)
	}
	if !strings.Contains(got, "can_proceed=true") {
		t.Errorf("can_proceed not true\n%s", got)
	}
}

// TestRollbackPreflight_GHAOutput_EmitsTargetSource_PreviousRing asserts that
// when the requested --to resolves against the deploy-history ring
// (Source="previous-ring"), the preflight gha-output includes
// target_source=previous-ring.
func TestRollbackPreflight_GHAOutput_EmitsTargetSource_PreviousRing(t *testing.T) {
	path := ringManifest(t)
	outFile := filepath.Join(t.TempDir(), "gha_output")
	t.Setenv("GITHUB_OUTPUT", outFile)

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	// v1.9.0 is in the ring but not the live state; resolver hits previous-ring.
	cmd.SetArgs([]string{
		"preflight",
		"--config", path,
		"--env", "prod",
		"--to", "v1.9.0",
		"--gha-output",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read gha output: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "target_source=previous-ring") {
		t.Errorf("gha output missing target_source=previous-ring\n%s", got)
	}
	if !strings.Contains(got, "can_proceed=true") {
		t.Errorf("can_proceed not true\n%s", got)
	}
}

// TestRollbackPreflight_GHAOutput_EmitsTargetSource_DefaultPreviousRing
// asserts that when no --to is given and the ring has a distinct N-1 entry,
// the default resolver picks it (Source="previous-ring") and the gha-output
// reflects that.
func TestRollbackPreflight_GHAOutput_EmitsTargetSource_DefaultPreviousRing(t *testing.T) {
	path := ringManifest(t)
	outFile := filepath.Join(t.TempDir(), "gha_output")
	t.Setenv("GITHUB_OUTPUT", outFile)

	cmd := NewCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	// No --to: default resolves N-1 from the ring.
	cmd.SetArgs([]string{
		"preflight",
		"--config", path,
		"--env", "prod",
		"--gha-output",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read gha output: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "target_source=previous-ring") {
		t.Errorf("gha output missing target_source=previous-ring\n%s", got)
	}
	if !strings.Contains(got, "can_proceed=true") {
		t.Errorf("can_proceed not true\n%s", got)
	}
}

// TestBuildStatePutArgs_StampsBotAuthor verifies the rollback Contents API state
// write attributes the commit to the github-actions[bot] default, not the token
// owner GitHub would otherwise stamp when author and committer are absent.
func TestBuildStatePutArgs_StampsBotAuthor(t *testing.T) {
	args := buildStatePutArgs("repos/acme/widgets/contents/m.yaml", "main", "deadbeef", "chore: update state after rollback of prod [skip ci]", "Y29udGVudA==", statewrite.Identity{})

	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		"author[name]=github-actions[bot]",
		"author[email]=github-actions[bot]@users.noreply.github.com",
		"committer[name]=github-actions[bot]",
		"committer[email]=github-actions[bot]@users.noreply.github.com",
		"sha=deadbeef",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildStatePutArgs missing %q in args %v", want, args)
		}
	}
}

// TestBuildStatePutArgs_HonorsCustomIdentity verifies a manifest git override
// flows into the rollback API author and committer fields.
func TestBuildStatePutArgs_HonorsCustomIdentity(t *testing.T) {
	id := statewrite.Identity{Name: "release-bot", Email: "release-bot@example.com"}
	args := buildStatePutArgs("repos/acme/widgets/contents/m.yaml", "main", "", "msg", "Y29udGVudA==", id)

	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		"author[name]=release-bot",
		"author[email]=release-bot@example.com",
		"committer[name]=release-bot",
		"committer[email]=release-bot@example.com",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildStatePutArgs missing %q in args %v", want, args)
		}
	}
	if strings.Contains(joined, "sha=") {
		t.Errorf("empty sha must not add a sha arg: %v", args)
	}
}

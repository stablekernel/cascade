package rollback

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/statewrite"
)

// stateOnlyManifest writes a manifest that declares no config block at all, only
// recorded state. It exercises the no-config branches (DeployNames returning nil,
// knownEnvironment falling back to recorded state).
func stateOnlyManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	content := `ci:
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
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func TestConfigPath_ReturnsResolvedPath(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})
	if got := rb.ConfigPath(); got != path {
		t.Errorf("ConfigPath() = %q, want %q", got, path)
	}
}

func TestGitIdentity_DefaultsToBot(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	id := rb.GitIdentity()
	if id.Name != "github-actions[bot]" {
		t.Errorf("Name = %q, want github-actions[bot]", id.Name)
	}
	if id.Email != "github-actions[bot]@users.noreply.github.com" {
		t.Errorf("Email = %q, want bot noreply", id.Email)
	}
}

func TestGitIdentity_HonorsManifestGitConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	content := `ci:
  config:
    trunk_branch: main
    environments:
      - prod
    git:
      user_name: release-bot
      user_email: release-bot@example.com
  state:
    prod:
      sha: prodsha9999999
      version: v1.9.0
      committed_at: "2026-02-01T11:00:00Z"
      committed_by: alice
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	rb := newRollbacker(t, path, fakeHistory{})

	id := rb.GitIdentity()
	if id.Name != "release-bot" {
		t.Errorf("Name = %q, want release-bot", id.Name)
	}
	if id.Email != "release-bot@example.com" {
		t.Errorf("Email = %q, want release-bot@example.com", id.Email)
	}
}

func TestDeployNames_WithAndWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	withConfig := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, withConfig, fakeHistory{})
	names := rb.DeployNames()
	if len(names) != 1 || names[0] != "services" {
		t.Errorf("DeployNames() = %v, want [services]", names)
	}

	noConfig := stateOnlyManifest(t)
	rb2 := newRollbacker(t, noConfig, fakeHistory{})
	if got := rb2.DeployNames(); got != nil {
		t.Errorf("DeployNames() with no config = %v, want nil", got)
	}
}

func TestKnownEnvironment_FallsBackToRecordedState(t *testing.T) {
	path := stateOnlyManifest(t)
	rb := newRollbacker(t, path, fakeHistory{})
	// prod has recorded state but no config.environments declaration.
	if !rb.knownEnvironment("prod") {
		t.Error("prod should be known via recorded state")
	}
	if rb.knownEnvironment("ghost") {
		t.Error("ghost should not be known")
	}
}

func TestOrDash(t *testing.T) {
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(\"\") = %q, want -", got)
	}
	if got := orDash("v1.0.0"); got != "v1.0.0" {
		t.Errorf("orDash(v1.0.0) = %q, want v1.0.0", got)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abcdef1234567", "abcdef1"},
		{"abc", "abc"},
		{"", "-"},
		{"1234567", "1234567"},
	}
	for _, c := range cases {
		if got := truncate(c.in); got != c.want {
			t.Errorf("truncate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGetEnv(t *testing.T) {
	t.Setenv("ROLLBACK_TEST_KEY", "value")
	if got := getEnv("ROLLBACK_TEST_KEY", "fallback"); got != "value" {
		t.Errorf("getEnv set = %q, want value", got)
	}
	if got := getEnv("ROLLBACK_TEST_UNSET_KEY", "fallback"); got != "fallback" {
		t.Errorf("getEnv unset = %q, want fallback", got)
	}
}

func TestMatchHelpers_NilInputs(t *testing.T) {
	if got := matchEnv(nil, "x", "state"); got != nil {
		t.Errorf("matchEnv(nil) = %+v, want nil", got)
	}
	if got := matchDeploy(nil, "x", "svc", "state"); got != nil {
		t.Errorf("matchDeploy(nil) = %+v, want nil", got)
	}
}

func TestResolveDefaultTarget_GitHistoryFallback(t *testing.T) {
	dir := t.TempDir()
	// Live prod is at v2.0.0 with no ring; the distinct prior lives in history.
	path := writeManifest(t, dir, "newprodsha1234", "v2.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {{SHA: "oldprodsha5678", Version: "v1.8.0"}},
	}}
	rb := newRollbacker(t, path, hist)

	plan, err := rb.Plan("prod", "", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "oldprodsha5678" {
		t.Errorf("target sha = %q, want oldprodsha5678", plan.Target.SHA)
	}
	if plan.Target.Source != "git-history" {
		t.Errorf("source = %q, want git-history", plan.Target.Source)
	}
}

func TestResolveDefaultTarget_DeployableScopedFromHistory(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "newprodsha1234", "v2.0.0")
	hist := fakeHistory{states: map[string][]*config.EnvState{
		"prod": {{SHA: "oldprodsha5678", Version: "v1.8.0",
			Deploys: map[string]*config.DeployState{
				"services": {SHA: "svcsha111", Version: "v1.8.0-svc"},
			}}},
	}}
	rb := newRollbacker(t, path, hist)

	plan, err := rb.Plan("prod", "", "services")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "svcsha111" {
		t.Errorf("target sha = %q, want svcsha111", plan.Target.SHA)
	}
	if plan.Target.Deployable != "services" {
		t.Errorf("deployable = %q, want services", plan.Target.Deployable)
	}
}

func TestResolveDefaultTarget_NoPriorErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "newprodsha1234", "v2.0.0")
	rb := newRollbacker(t, path, fakeHistory{})

	if _, err := rb.Plan("prod", "", ""); err == nil {
		t.Error("expected error when no prior version exists (env-scoped)")
	}
	if _, err := rb.Plan("prod", "", "services"); err == nil {
		t.Error("expected error when no prior version exists (deployable-scoped)")
	}
}

func TestReport_JSONOutput(t *testing.T) {
	plan := &Plan{
		Environment:    "prod",
		Target:         Target{SHA: "abcdef1234567", Version: "v1.8.0", Source: "git-history"},
		CurrentSHA:     "newsha7654321",
		CurrentVersion: "v2.0.0",
	}
	if err := report(plan, true, false); err != nil {
		t.Fatalf("report json: %v", err)
	}
}

func TestReport_TextNonNoOpAndDeployable(t *testing.T) {
	plan := &Plan{
		Environment:    "prod",
		Deployable:     "services",
		Target:         Target{SHA: "abcdef1234567", Version: "v1.8.0", Source: "previous-ring"},
		CurrentSHA:     "newsha7654321",
		CurrentVersion: "v2.0.0",
	}
	if err := report(plan, false, true); err != nil {
		t.Fatalf("report text dry-run: %v", err)
	}
	if err := report(plan, false, false); err != nil {
		t.Fatalf("report text apply: %v", err)
	}
}

func TestRun_ErrorOnBadConfig(t *testing.T) {
	err := run(runOptions{configPath: filepath.Join(t.TempDir(), "does-not-exist.yaml"), env: "prod"})
	if err == nil {
		t.Fatal("expected error for unreadable manifest")
	}
}

func TestRunPreflight_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	err := runPreflight(preflightOptions{configPath: path, env: "prod", to: "v1.9.0", jsonOutput: true})
	if err != nil {
		t.Fatalf("runPreflight json: %v", err)
	}
}

func TestRunPreflight_NewErrorWritesCannotProceed(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "gha_output")
	t.Setenv("GITHUB_OUTPUT", outFile)

	err := runPreflight(preflightOptions{
		configPath: filepath.Join(t.TempDir(), "missing.yaml"),
		env:        "prod",
		ghaOutput:  true,
	})
	if err == nil {
		t.Fatal("expected error for unreadable manifest")
	}
	data, readErr := os.ReadFile(outFile)
	if readErr != nil {
		t.Fatalf("read gha output: %v", readErr)
	}
	if string(data) == "" {
		t.Error("expected can_proceed=false written on New failure")
	}
}

func TestRunFinalize_ErrorOnBadConfig(t *testing.T) {
	err := runFinalize(finalizeOptions{
		configPath: filepath.Join(t.TempDir(), "missing.yaml"),
		env:        "prod",
	})
	if err == nil {
		t.Fatal("expected error for unreadable manifest")
	}
}

func TestIsRealGitHub(t *testing.T) {
	t.Setenv("GITHUB_SERVER_URL", "")
	if !isRealGitHub() {
		t.Error("empty GITHUB_SERVER_URL should be treated as real GitHub")
	}
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	if !isRealGitHub() {
		t.Error("github.com should be real GitHub")
	}
	t.Setenv("GITHUB_SERVER_URL", "https://gitea.local")
	if isRealGitHub() {
		t.Error("gitea host should not be real GitHub")
	}
}

func TestTrunkBranchFromEnv(t *testing.T) {
	t.Setenv("GITHUB_REF", "refs/heads/release")
	if got := trunkBranchFromEnv(); got != "release" {
		t.Errorf("trunkBranchFromEnv() = %q, want release", got)
	}
	t.Setenv("GITHUB_REF", "some-branch")
	if got := trunkBranchFromEnv(); got != "some-branch" {
		t.Errorf("trunkBranchFromEnv() = %q, want some-branch", got)
	}
	t.Setenv("GITHUB_REF", "")
	if got := trunkBranchFromEnv(); got != "main" {
		t.Errorf("trunkBranchFromEnv() = %q, want main", got)
	}
}

func TestWriteStateViaAPI_RequiresRepository(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	err := writeStateViaAPI("manifest.yaml", "msg", statewrite.Identity{})
	if err == nil {
		t.Fatal("expected error when GITHUB_REPOSITORY is unset")
	}
}

func TestCommitAndPush_NoChangesIsNoOp(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	gitCommitFile(t, dir, "manifest.yaml", manifestAt("prodsha9999999", "v1.9.0"), "seed")

	t.Chdir(dir)
	// The committed manifest is unchanged, so commitAndPush observes a clean tree
	// and returns nil without attempting any push.
	if err := commitAndPush("manifest.yaml", "prod", statewrite.Identity{}); err != nil {
		t.Fatalf("commitAndPush no-op: %v", err)
	}
}

func TestExtractEnvState_EdgeCases(t *testing.T) {
	// Invalid YAML returns nil.
	if s := extractEnvState([]byte("::: not yaml :::"), config.DefaultManifestKey, "prod"); s != nil {
		t.Errorf("invalid yaml = %+v, want nil", s)
	}
	// Missing top-level key returns nil.
	if s := extractEnvState([]byte("other:\n  state: {}\n"), config.DefaultManifestKey, "prod"); s != nil {
		t.Errorf("missing key = %+v, want nil", s)
	}
	// Env absent returns nil.
	missingEnv := []byte("ci:\n  state:\n    dev:\n      sha: x\n")
	if s := extractEnvState(missingEnv, config.DefaultManifestKey, "prod"); s != nil {
		t.Errorf("absent env = %+v, want nil", s)
	}
	// Empty (zero-value) env state returns nil.
	emptyEnv := []byte("ci:\n  state:\n    prod: {}\n")
	if s := extractEnvState(emptyEnv, config.DefaultManifestKey, "prod"); s != nil {
		t.Errorf("empty env state = %+v, want nil", s)
	}
	// A populated env state is returned, and an empty manifest key defaults.
	good := []byte("ci:\n  state:\n    prod:\n      sha: prodsha9999999\n      version: v1.9.0\n")
	s := extractEnvState(good, "", "prod")
	if s == nil || s.SHA != "prodsha9999999" {
		t.Errorf("populated env state not returned: %+v", s)
	}
}

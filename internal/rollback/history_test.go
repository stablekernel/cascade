package rollback

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// gitInit creates a throwaway git repo in dir with a minimal identity.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func gitCommitFile(t *testing.T, dir, rel, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	for _, args := range [][]string{
		{"add", rel},
		{"commit", "-q", "-m", msg},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func manifestAt(prodSHA, prodVersion string) string {
	return `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
  state:
    prod:
      sha: ` + prodSHA + `
      version: ` + prodVersion + `
      committed_at: "2026-04-01T11:00:00Z"
      committed_by: alice
`
}

// TestGitHistoryReader_RecoversPriorVersion proves a target that only exists in
// a prior manifest commit is recoverable through the real git-backed reader.
func TestGitHistoryReader_RecoversPriorVersion(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)

	rel := "manifest.yaml"
	gitCommitFile(t, dir, rel, manifestAt("oldsha1112223", "v1.5.0"), "prod v1.5.0")
	gitCommitFile(t, dir, rel, manifestAt("newsha4445556", "v2.0.0"), "prod v2.0.0")

	path := filepath.Join(dir, rel)

	// Live manifest is at v2.0.0; resolve a rollback to the historical v1.5.0
	// using the real git history reader (no fake).
	rb, err := New(Options{ConfigPath: path, Actor: "oncall"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plan, err := rb.Plan("prod", "v1.5.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "oldsha1112223" {
		t.Errorf("target sha = %q, want oldsha1112223", plan.Target.SHA)
	}
	if plan.Target.Source != "git-history" {
		t.Errorf("source = %q, want git-history", plan.Target.Source)
	}

	if err := rb.Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	file, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if file.State["prod"].SHA != "oldsha1112223" {
		t.Errorf("prod not rolled back: %q", file.State["prod"].SHA)
	}
}

// TestGitHistoryReader_RecoversPriorVersion_Subdir proves the reader recovers
// prior states when the manifest lives in a subdirectory (e.g. .github/) rather
// than at the repo root. `git show <sha>:<path>` resolves <path> relative to the
// repo root, so a subdir manifest must be addressed by its repo-root-relative
// path, not a basename combined with `-C <subdir>`.
func TestGitHistoryReader_RecoversPriorVersion_Subdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitInit(t, dir)

	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	rel := filepath.Join(".github", "manifest.yaml")
	gitCommitFile(t, dir, rel, manifestAt("oldsha1112223", "v1.5.0"), "prod v1.5.0")
	gitCommitFile(t, dir, rel, manifestAt("newsha4445556", "v2.0.0"), "prod v2.0.0")

	path := filepath.Join(dir, rel)

	reader := newGitHistoryReader(path, config.DefaultManifestKey, "")
	states, err := reader.PriorStates("prod")
	if err != nil {
		t.Fatalf("PriorStates: %v", err)
	}
	if len(states) == 0 {
		t.Fatalf("PriorStates returned empty; subdir manifest history not recovered")
	}
	// Newest first: v2.0.0 then v1.5.0.
	if states[0].Version != "v2.0.0" {
		t.Errorf("states[0].Version = %q, want v2.0.0", states[0].Version)
	}
	var foundPrior bool
	for _, s := range states {
		if s.SHA == "oldsha1112223" && s.Version == "v1.5.0" {
			foundPrior = true
		}
	}
	if !foundPrior {
		t.Errorf("prior state v1.5.0/oldsha1112223 not found in %+v", states)
	}

	// End-to-end: the historical target must be resolvable and applicable.
	rb, err := New(Options{ConfigPath: path, Actor: "oncall"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plan, err := rb.Plan("prod", "v1.5.0", "")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Target.SHA != "oldsha1112223" {
		t.Errorf("target sha = %q, want oldsha1112223", plan.Target.SHA)
	}
	if plan.Target.Source != "git-history" {
		t.Errorf("source = %q, want git-history", plan.Target.Source)
	}
}

package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
)

// seedComponentRemote builds a bare remote seeded with a component-scoped
// manifest (one unrelated "seed" component leaf, so the manifest is
// unambiguously in the component form and there is a sibling subtree to
// preserve), plus a working clone whose upstream tracks it. The clone's local
// manifest is the seed; the caller lands a concurrent sibling leaf on trunk to
// make a subsequent push non-fast-forward.
func seedComponentRemote(t *testing.T) (clone, statePath string) {
	t.Helper()

	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "main")

	const seed = "ci:\n  state:\n    components:\n      seed:\n        prod:\n          version: v0.0.1\n"

	clone = t.TempDir()
	runGit(t, clone, "clone", remote, ".")
	runGit(t, clone, "checkout", "-b", "main")
	writeFile(t, clone, ".github/manifest.yaml", seed)
	runGit(t, clone, "add", ".github/manifest.yaml")
	runGit(t, clone, "commit", "-m", "chore: seed state")
	runGit(t, clone, "push", "-u", "origin", "main")

	statePath = filepath.Join(clone, ".github", "manifest.yaml")
	return clone, statePath
}

// landSiblingLeaf clones the remote behind clone, node-patches component "b"'s
// prod leaf onto trunk via the same scoped-write the finalizers use, and pushes
// it first. That advances origin so clone's parent is stale and its own adjacent
// component-"a" leaf collides on a textual rebase.
func landSiblingLeaf(t *testing.T, clone string) {
	t.Helper()

	remote := runGit(t, clone, "config", "--get", "remote.origin.url")
	writer := t.TempDir()
	runGit(t, writer, "clone", remote, ".")

	path := filepath.Join(writer, ".github", "manifest.yaml")
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read writer manifest: %v", err)
	}
	patched, err := config.WriteScopedState(current, "ci", config.StateWrite{
		Component: "b", Env: "prod", State: &config.EnvState{Version: "v2.0.0", SHA: "bbbbbbb"},
	})
	if err != nil {
		t.Fatalf("scoped write for sibling leaf: %v", err)
	}
	if err := os.WriteFile(path, patched, 0o644); err != nil {
		t.Fatalf("write writer manifest: %v", err)
	}
	runGit(t, writer, "add", ".github/manifest.yaml")
	runGit(t, writer, "commit", "-m", "b: land prod leaf")
	runGit(t, writer, "push", "origin", "main")
}

// remoteManifest returns the manifest at the tip of origin/main as seen from
// clone, so a test asserts what actually converged on trunk (not the local tree).
func remoteManifest(t *testing.T, clone string) string {
	t.Helper()
	runGit(t, clone, "fetch", "origin", "main")
	return runGit(t, clone, "show", "origin/main:.github/manifest.yaml")
}

// TestCommitAndPush_ComponentReappliesAdjacentSiblingLeaf is the convergence
// regression test for the multi-component finalize race: two component lanes
// append adjacent leaves into one shared manifest on a busy trunk. Component "b"
// lands its leaf first; component "a"'s orchestrator then finds its push rejected
// non-fast-forward. The re-apply path must re-derive only "a"'s leaf onto the
// re-fetched trunk (which already carries "b"), so BOTH leaves survive. A textual
// rebase would conflict on the adjacent insertion; the sibling regression guard
// below proves that.
func TestCommitAndPush_ComponentReappliesAdjacentSiblingLeaf(t *testing.T) {
	clone, statePath := seedComponentRemote(t)
	landSiblingLeaf(t, clone)

	o := &Orchestrator{
		configPath:  statePath,
		environment: "prod",
		component:   "a",
		baseDir:     clone,
		pushBackoff: time.Millisecond,
		cicdFile: &config.CICDFile{
			State: map[string]*config.EnvState{
				"prod": {Version: "v1.0.0", SHA: "aaaaaaa"},
			},
		},
	}

	// Finalize writes the owned leaf onto the (now stale) local manifest, then
	// commits and pushes. Mirror that order here.
	if err := o.writeConfig(); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	if err := o.commitAndPush("v1.0.0"); err != nil {
		t.Fatalf("commitAndPush must re-apply the owned leaf and converge, got: %v", err)
	}

	got := remoteManifest(t, clone)
	// Both racers' leaves and the pre-existing sibling must all be present: no
	// leaf was dropped and the concurrent "b" write was not clobbered.
	for _, want := range []string{"a:", "b:", "seed:", "v1.0.0", "v2.0.0", "v0.0.1"} {
		if !strings.Contains(got, want) {
			t.Errorf("converged origin manifest missing %q; got:\n%s", want, got)
		}
	}
}

// TestPushWithRebaseRetry_TextualPathConflictsOnAdjacentLeaf is the regression
// guard proving the defect the re-apply fixes: the historical textual
// "git pull --rebase" path (no WithReapply) cannot land component "a"'s leaf when
// a concurrent "b" leaf occupies the adjacent line, because the two insertions
// conflict. It must return an error and leave the clone clean (not mid-rebase).
// This is the "old path drops/conflicts" half of the red-before evidence.
func TestPushWithRebaseRetry_TextualPathConflictsOnAdjacentLeaf(t *testing.T) {
	clone, statePath := seedComponentRemote(t)
	landSiblingLeaf(t, clone)

	// Stage and commit "a"'s adjacent leaf on the stale local seed, then push
	// through the textual-rebase path with no re-apply hook.
	current, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read local manifest: %v", err)
	}
	patched, err := config.WriteScopedState(current, "ci", config.StateWrite{
		Component: "a", Env: "prod", State: &config.EnvState{Version: "v1.0.0", SHA: "aaaaaaa"},
	})
	if err != nil {
		t.Fatalf("scoped write for owned leaf: %v", err)
	}
	if err := os.WriteFile(statePath, patched, 0o644); err != nil {
		t.Fatalf("write local manifest: %v", err)
	}
	runGit(t, clone, "add", ".github/manifest.yaml")
	runGit(t, clone, "commit", "-m", "a: land prod leaf")

	err = git.PushWithRebaseRetry(git.WithDir(clone), git.WithBackoff(time.Millisecond))
	if err == nil {
		t.Fatal("textual rebase path unexpectedly converged adjacent leaves; expected a conflict error")
	}

	// The clone must not be left mid-rebase after the abort.
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, statErr := os.Stat(filepath.Join(clone, ".git", name)); statErr == nil {
			t.Fatalf("repository left mid-rebase: .git/%s still present", name)
		}
	}
}

package orchestrate

import (
	"path/filepath"
	"testing"
)

// TestReconcile_StateCommitAtHead_UnfixedAlsoSkips proves the masking case that
// explains why the bug was invisible in the normal harness/live-fleet flow.
//
// Scenario: a prior finalize HAS run and pushed a "chore: update state" commit,
// so the commit graph on the next dispatch is:
//
//	HEAD    = chore: update state  (manifest.yaml only, no src/**)
//	HEAD~1  = feat: add app        (src/app.go)
//
// dispatch fires at GITHUB_SHA == HEAD (the state commit). With the UNFIXED
// ladder, base = HEAD~1 = the src commit, and diff(HEAD~1..HEAD) touches only
// manifest.yaml, which does NOT match src/** -> build SKIPS even without the
// fix. This is why a same-graph re-dispatch through finalize never reproduced
// the bug: the intervening state commit at HEAD masks it.
//
// Therefore the live-fleet failure ("a no-change orchestrate cut a prerelease
// and Build(app) succeeded") can only be a dispatch where NO state commit sits
// at HEAD - i.e. GITHUB_SHA was the src commit itself (first-ever dispatch, a
// failed/never-run prior finalize, or a manual re-dispatch at the src SHA).
func TestReconcile_StateCommitAtHead_UnfixedAlsoSkips(t *testing.T) {
	repoDir, srcHead := initRepo(t) // HEAD = feat:add app (src/app.go)

	// Simulate a prior finalize: write the manifest with state.sha = srcHead and
	// commit it as a manifest-only "chore: update state" commit. That commit is
	// now HEAD; the src commit becomes HEAD~1.
	writeManifest(t, repoDir, srcHead)
	runGit(t, repoDir, "add", ".github/manifest.yaml")
	runGit(t, repoDir, "commit", "-m", "chore: update state for prerelease [skip ci]")
	stateHead := runGit(t, repoDir, "rev-parse", "HEAD")

	manifestPath := filepath.Join(repoDir, ".github", "manifest.yaml")
	orch, err := NewOrchestrator(manifestPath, "ci", "prerelease")
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	envState := orch.cicdFile.State["prerelease"]
	triggers := orch.cicdFile.Config.Builds[0].Triggers

	headParent, _ := orch.gitOutput("rev-parse", "HEAD~1")
	t.Logf("OBSERVED stateHead (HEAD) = %s", stateHead)
	t.Logf("OBSERVED HEAD~1 (srcHead) = %s", headParent)

	// Confirm HEAD is manifest-only.
	changed, _ := orch.gitOutput("diff", "--name-only", headParent, stateHead)
	t.Logf("OBSERVED diff(HEAD~1..HEAD) files = %q", changed)

	// UNFIXED ladder: base = HEAD~1 = srcHead. Dispatch at GITHUB_SHA = stateHead.
	unfixedBase := orch.unfixedBuildBaseSHA(envState, "app")
	unfixedRuns := orch.detectChanges(unfixedBase, stateHead, triggers)
	t.Logf("UNFIXED base=%s  detectChanges(base..stateHead)=%v", unfixedBase, unfixedRuns)

	if unfixedBase != headParent {
		t.Errorf("UNFIXED base = %s, want HEAD~1 (%s)", unfixedBase, headParent)
	}
	if unfixedRuns {
		t.Errorf("UNFIXED RunBuilds[app] = true; expected FALSE: with a state commit at HEAD the masking should make even the unfixed code skip")
	} else {
		t.Logf("RECONCILED: with a state commit at HEAD, the UNFIXED code ALSO skips (diff is manifest-only). The bug is masked whenever finalize pushed a state commit before re-dispatch.")
	}
}

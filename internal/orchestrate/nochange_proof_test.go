package orchestrate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// unfixedBuildBaseSHA reproduces the PRE-FIX build base-SHA ladder verbatim, as
// it existed before commit ee432d2 (see `git diff HEAD~1`). The old ladder had
// exactly two rungs for a build: (1) the dependent deploy's state SHA, then
// (2) defaultBase (HEAD~1). There was no per-build state lookup and no
// env-level envState.SHA fallback. This function lets us run the unfixed
// algorithm against the same real git fixture the fixed code runs against,
// proving empirically whether the no-change-skip bug was real.
func (o *Orchestrator) unfixedBuildBaseSHA(envState *config.EnvState, buildName string) string {
	defaultBase, _ := o.gitOutput("rev-parse", "HEAD~1")
	if defaultBase == "" {
		defaultBase, _ = o.gitOutput("rev-list", "--max-parents=0", "HEAD")
	}

	base := ""
	if envState != nil && envState.Deploys != nil {
		for _, deploy := range o.cicdFile.Config.Deploys {
			if contains(deploy.DependsOn, buildName) {
				if ds := envState.Deploys[deploy.Name]; ds != nil && ds.SHA != "" {
					base = ds.SHA
					break
				}
			}
		}
	}
	if base == "" {
		base = defaultBase
	}
	return base
}

// TestNoChangeReDispatch_ProvesUnfixedBugAndFix runs BOTH algorithms against one
// real git fixture representing a genuine no-change re-dispatch:
//
//   - Repo: HEAD~1 = chore:init (no src), HEAD = feat:add app (src/app.go).
//   - State: prerelease.sha == HEAD (the env was already orchestrated at HEAD).
//   - There is NO dependent deploy (deploys: []), and NO prior finalize state
//     commit sits at HEAD: HEAD is the src commit itself.
//
// This is the exact "same-HEAD no-change dispatch with no intervening state
// commit" scenario. We assert the OBSERVED values:
//
//	UNFIXED: base = HEAD~1 (chore:init) -> diff HEAD~1..HEAD touches src/app.go
//	         -> detectChanges == true -> build RE-RUNS  (THE BUG)
//	FIXED:   base = envState.SHA == HEAD -> diff HEAD..HEAD empty
//	         -> detectChanges == false -> build SKIPS    (THE FIX)
func TestNoChangeReDispatch_ProvesUnfixedBugAndFix(t *testing.T) {
	repoDir, headSHA := initRepo(t)
	manifestPath := writeManifest(t, repoDir, headSHA) // state.sha == HEAD

	orch, err := NewOrchestrator(manifestPath, "ci", "prerelease")
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	envState := orch.cicdFile.State["prerelease"]
	if envState == nil || envState.SHA != headSHA {
		t.Fatalf("fixture invariant broken: envState.SHA=%q want %q", func() string {
			if envState == nil {
				return "<nil>"
			}
			return envState.SHA
		}(), headSHA)
	}

	// --- Sanity: confirm HEAD~1 carries no src/** but HEAD~1..HEAD does. ---
	headParent, _ := orch.gitOutput("rev-parse", "HEAD~1")
	t.Logf("OBSERVED headSHA          = %s", headSHA)
	t.Logf("OBSERVED HEAD~1           = %s", headParent)
	t.Logf("OBSERVED envState.SHA     = %s", envState.SHA)

	triggers := orch.cicdFile.Config.Builds[0].Triggers // ["src/**"]

	// --- UNFIXED algorithm ---
	unfixedBase := orch.unfixedBuildBaseSHA(envState, "app")
	unfixedRuns := orch.detectChanges(unfixedBase, headSHA, triggers)
	t.Logf("UNFIXED base=%s  detectChanges(base..HEAD)=%v", unfixedBase, unfixedRuns)

	if unfixedBase != headParent {
		t.Errorf("UNFIXED base = %s, expected HEAD~1 (%s): the bug requires the ladder to fall to HEAD~1", unfixedBase, headParent)
	}
	if !unfixedRuns {
		t.Errorf("UNFIXED RunBuilds[app] = false, expected TRUE: the unfixed ladder anchors on HEAD~1 which still contains the src commit, so the build re-runs on a no-change dispatch (the bug was NOT reproduced)")
	} else {
		t.Logf("PROVEN: unfixed code RE-RUNS the build on a genuine no-change dispatch (RunBuilds[app]=true)")
	}

	// --- FIXED algorithm (the real Setup path) ---
	res, err := orch.Setup(headSHA)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	fixedBase := res.BaseSHAs["build_app"]
	t.Logf("FIXED   base=%s  RunBuilds[app]=%v", fixedBase, res.RunBuilds["app"])

	if fixedBase != headSHA {
		t.Errorf("FIXED base = %s, expected envState.SHA == HEAD (%s)", fixedBase, headSHA)
	}
	if res.RunBuilds["app"] {
		t.Errorf("FIXED RunBuilds[app] = true, expected FALSE: the fix should skip the build on a no-change dispatch")
	} else {
		t.Logf("PROVEN: fixed code SKIPS the build on a genuine no-change dispatch (RunBuilds[app]=false)")
	}
}

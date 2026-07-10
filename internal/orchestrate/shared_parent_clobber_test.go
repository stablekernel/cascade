package orchestrate

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stablekernel/cascade/internal/config"
)

// seedSharedParentTwoComponents builds a bare remote seeded at ONE shared parent
// commit that already carries BOTH component leaves (api + web) at v0.1.0, then
// returns two working clones checked out at that shared parent. This models the
// shared_paths wave: one commit fires two component orchestrate lanes, each of
// which finalizes its own leaf into the single shared manifest on trunk.
func seedSharedParentTwoComponents(t *testing.T) (remote, cloneAPI, cloneWEB, statePathAPI, statePathWEB string) {
	t.Helper()

	remote = t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "main")

	const seed = "ci:\n" +
		"  state:\n" +
		"    components:\n" +
		"      api:\n" +
		"        dev:\n" +
		"          version: api-0.1.0-rc.0\n" +
		"          sha: aaaa000\n" +
		"      web:\n" +
		"        dev:\n" +
		"          version: web-0.1.0-rc.0\n" +
		"          sha: wwww000\n"

	seeder := t.TempDir()
	runGit(t, seeder, "clone", remote, ".")
	runGit(t, seeder, "checkout", "-b", "main")
	writeFile(t, seeder, ".github/manifest.yaml", seed)
	runGit(t, seeder, "add", ".github/manifest.yaml")
	runGit(t, seeder, "commit", "-m", "chore: seed shared parent")
	runGit(t, seeder, "push", "-u", "origin", "main")

	cloneAPI = t.TempDir()
	runGit(t, cloneAPI, "clone", remote, ".")
	statePathAPI = filepath.Join(cloneAPI, ".github", "manifest.yaml")

	cloneWEB = t.TempDir()
	runGit(t, cloneWEB, "clone", remote, ".")
	statePathWEB = filepath.Join(cloneWEB, ".github", "manifest.yaml")

	return remote, cloneAPI, cloneWEB, statePathAPI, statePathWEB
}

func newComponentOrchestrator(statePath, component, baseDir, version string) *Orchestrator {
	return &Orchestrator{
		configPath:  statePath,
		environment: "dev",
		component:   component,
		baseDir:     baseDir,
		pushBackoff: time.Millisecond,
		cicdFile: &config.CICDFile{
			State: map[string]*config.EnvState{
				"dev": {Version: version, SHA: component + "-new"},
			},
		},
	}
}

// finalizeLane mirrors Finalize's write-then-commit-and-push sequence for one
// component lane.
func finalizeLane(t *testing.T, o *Orchestrator) {
	t.Helper()
	if err := o.writeConfig(); err != nil {
		t.Fatalf("%s writeConfig: %v", o.component, err)
	}
	if err := o.commitAndPush(o.cicdFile.State["dev"].Version); err != nil {
		t.Fatalf("%s commitAndPush: %v", o.component, err)
	}
}

func assertBothLeavesSurvive(t *testing.T, clone string, wantAPI, wantWEB string) {
	t.Helper()
	runGit(t, clone, "fetch", "origin", "main")
	got := runGit(t, clone, "show", "origin/main:.github/manifest.yaml")
	for _, want := range []string{wantAPI, wantWEB} {
		if !strings.Contains(got, want) {
			t.Errorf("converged trunk manifest missing %q; got:\n%s", want, got)
		}
	}
}

// TestSharedParent_SequentialLanes_ApiFirst: both lanes writeConfig on the shared
// parent, then api pushes first (ff) and web pushes second (reject -> reapply).
func TestSharedParent_SequentialLanes_ApiFirst(t *testing.T) {
	_, cloneAPI, cloneWEB, spAPI, spWEB := seedSharedParentTwoComponents(t)

	api := newComponentOrchestrator(spAPI, "api", cloneAPI, "api-0.2.0-rc.0")
	web := newComponentOrchestrator(spWEB, "web", cloneWEB, "web-0.2.0-rc.0")

	// Both derive their leaf against the shared-parent bytes BEFORE either pushes.
	if err := api.writeConfig(); err != nil {
		t.Fatalf("api writeConfig: %v", err)
	}
	if err := web.writeConfig(); err != nil {
		t.Fatalf("web writeConfig: %v", err)
	}
	if err := api.commitAndPush("api-0.2.0-rc.0"); err != nil {
		t.Fatalf("api commitAndPush: %v", err)
	}
	if err := web.commitAndPush("web-0.2.0-rc.0"); err != nil {
		t.Fatalf("web commitAndPush: %v", err)
	}

	assertBothLeavesSurvive(t, cloneAPI, "api-0.2.0-rc.0", "web-0.2.0-rc.0")
}

// TestSharedParent_SequentialLanes_WebFirst is the reverse ordering.
func TestSharedParent_SequentialLanes_WebFirst(t *testing.T) {
	_, cloneAPI, cloneWEB, spAPI, spWEB := seedSharedParentTwoComponents(t)

	api := newComponentOrchestrator(spAPI, "api", cloneAPI, "api-0.2.0-rc.0")
	web := newComponentOrchestrator(spWEB, "web", cloneWEB, "web-0.2.0-rc.0")

	if err := web.writeConfig(); err != nil {
		t.Fatalf("web writeConfig: %v", err)
	}
	if err := api.writeConfig(); err != nil {
		t.Fatalf("api writeConfig: %v", err)
	}
	if err := web.commitAndPush("web-0.2.0-rc.0"); err != nil {
		t.Fatalf("web commitAndPush: %v", err)
	}
	if err := api.commitAndPush("api-0.2.0-rc.0"); err != nil {
		t.Fatalf("api commitAndPush: %v", err)
	}

	assertBothLeavesSurvive(t, cloneAPI, "api-0.2.0-rc.0", "web-0.2.0-rc.0")
}

// TestSharedParent_FullLane_ApiFirst drives the full write+push per lane in
// strict sequence (api lane fully finalizes, then web lane fully finalizes),
// matching the empirical "two sequential state commits" observation.
func TestSharedParent_FullLane_ApiFirst(t *testing.T) {
	_, cloneAPI, cloneWEB, spAPI, spWEB := seedSharedParentTwoComponents(t)

	api := newComponentOrchestrator(spAPI, "api", cloneAPI, "api-0.2.0-rc.0")
	web := newComponentOrchestrator(spWEB, "web", cloneWEB, "web-0.2.0-rc.0")

	finalizeLane(t, api)
	finalizeLane(t, web)

	assertBothLeavesSurvive(t, cloneAPI, "api-0.2.0-rc.0", "web-0.2.0-rc.0")
}

// TestSharedParent_ConcurrentWave races N component lanes finalizing off one
// shared parent through real goroutines, mirroring a monorepo's shared_paths
// wave. Every owned leaf must survive on trunk regardless of the OS scheduler's
// interleaving.
func TestSharedParent_ConcurrentWave(t *testing.T) {
	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", "-b", "main")

	comps := []string{"api", "web", "worker", "cron", "gateway"}

	// Seed a shared parent carrying every component's old leaf.
	var seed strings.Builder
	seed.WriteString("ci:\n  state:\n    components:\n")
	for _, c := range comps {
		fmt.Fprintf(&seed, "      %s:\n        dev:\n          version: %s-0.1.0-rc.0\n          sha: %s000\n", c, c, c)
	}
	seeder := t.TempDir()
	runGit(t, seeder, "clone", remote, ".")
	runGit(t, seeder, "checkout", "-b", "main")
	writeFile(t, seeder, ".github/manifest.yaml", seed.String())
	runGit(t, seeder, "add", ".github/manifest.yaml")
	runGit(t, seeder, "commit", "-m", "chore: seed shared parent")
	runGit(t, seeder, "push", "-u", "origin", "main")

	var wg sync.WaitGroup
	errs := make(chan error, len(comps))
	for _, c := range comps {
		clone := t.TempDir()
		runGit(t, clone, "clone", remote, ".")
		sp := filepath.Join(clone, ".github", "manifest.yaml")
		o := newComponentOrchestrator(sp, c, clone, c+"-0.2.0-rc.0")
		wg.Add(1)
		go func(o *Orchestrator) {
			defer wg.Done()
			if err := o.writeConfig(); err != nil {
				errs <- err
				return
			}
			if err := o.commitAndPush(o.cicdFile.State["dev"].Version); err != nil {
				errs <- err
			}
		}(o)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("lane failed: %v", err)
		}
	}

	verify := t.TempDir()
	runGit(t, verify, "clone", remote, ".")
	got := runGit(t, verify, "show", "origin/main:.github/manifest.yaml")
	for _, c := range comps {
		want := c + "-0.2.0-rc.0"
		if !strings.Contains(got, want) {
			t.Errorf("converged trunk missing %q (clobbered); got:\n%s", want, got)
		}
	}
}

// TestSharedParent_LaneBehindTrunk models a lane whose local checkout is behind
// trunk: the sibling already landed its NEW leaf on trunk before this lane
// pushes. The lane's first push must reject and the re-apply must preserve the
// sibling's new value rather than reverting it to the checkout-time value.
func TestSharedParent_LaneBehindTrunk(t *testing.T) {
	remote, cloneAPI, cloneWEB, spAPI, spWEB := seedSharedParentTwoComponents(t)
	_ = remote

	// api lands its NEW leaf on trunk first (fully finalizes).
	api := newComponentOrchestrator(spAPI, "api", cloneAPI, "api-0.2.0-rc.0")
	finalizeLane(t, api)

	// web's clone is still at the shared parent (behind trunk): its on-disk
	// manifest shows api-0.1.0. It derives its leaf against that stale base.
	web := newComponentOrchestrator(spWEB, "web", cloneWEB, "web-0.2.0-rc.0")
	finalizeLane(t, web)

	// api's NEW leaf must NOT have been reverted by web's finalize.
	assertBothLeavesSurvive(t, cloneWEB, "api-0.2.0-rc.0", "web-0.2.0-rc.0")
	got := runGit(t, cloneWEB, "show", "origin/main:.github/manifest.yaml")
	if strings.Contains(got, "api-0.1.0-rc.0") {
		t.Errorf("web finalize reverted api to the stale checkout value; got:\n%s", got)
	}
}

// TestSharedParent_FirstAttemptReDerivesOntoFreshTrunk is the direct regression
// guard for the first-attempt clobber: a lane whose working manifest is stale
// (still shows the sibling's OLD value) while its local HEAD already sits at the
// trunk tip. A plain first push then fast-forwards with NO rejection, so the
// rejection-driven re-apply never runs and the stale sibling value lands,
// reverting the sibling. commitAndPush must re-derive the owned leaf onto fresh
// trunk before the first commit so the sibling survives. Fails before the fix,
// passes after.
func TestSharedParent_FirstAttemptReDerivesOntoFreshTrunk(t *testing.T) {
	_, cloneAPI, cloneWEB, spAPI, spWEB := seedSharedParentTwoComponents(t)

	// api fully finalizes first: trunk is now api-0.2.0, web-0.1.0.
	api := newComponentOrchestrator(spAPI, "api", cloneAPI, "api-0.2.0-rc.0")
	finalizeLane(t, api)

	// Sync web's clone so its local HEAD is the trunk tip (a plain push here would
	// fast-forward), then leave a stale working manifest showing api's OLD value.
	// This is the state a rejection can never surface.
	runGit(t, cloneWEB, "fetch", "origin", "main")
	runGit(t, cloneWEB, "reset", "--hard", "origin/main")
	stale := "ci:\n  state:\n    components:\n" +
		"      api:\n        dev:\n          version: api-0.1.0-rc.0\n          sha: aaaa000\n" +
		"      web:\n        dev:\n          version: web-0.2.0-rc.0\n          sha: web-new\n"
	writeFile(t, cloneWEB, ".github/manifest.yaml", stale)

	web := newComponentOrchestrator(spWEB, "web", cloneWEB, "web-0.2.0-rc.0")
	if err := web.commitAndPush("web-0.2.0-rc.0"); err != nil {
		t.Fatalf("web commitAndPush: %v", err)
	}

	runGit(t, cloneWEB, "fetch", "origin", "main")
	got := runGit(t, cloneWEB, "show", "origin/main:.github/manifest.yaml")
	if strings.Contains(got, "api-0.1.0-rc.0") {
		t.Fatalf("first-attempt clobber: api reverted to 0.1.0 with no reject to catch it:\n%s", got)
	}
	for _, want := range []string{"api-0.2.0-rc.0", "web-0.2.0-rc.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("converged trunk missing %q:\n%s", want, got)
		}
	}
}

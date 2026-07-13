package hotfix

import (
	"errors"
	"strings"
	"testing"
)

// resetCall records one ResetBranch invocation so tests can assert the orphan
// self-heal fired (or did not) and targeted the recorded base SHA.
type resetCall struct {
	remote string
	name   string
	sha    string
}

// fakeGitRunner is a fully programmable gitRunner for the branch-reconciliation
// unit tests. It mutates no real repository: every method returns the configured
// value and ResetBranch/CreateBranch only record their arguments. This keeps the
// self-heal gating logic under test isolated from a live remote (a real reset
// force-pushes, which a unit test cannot satisfy).
type fakeGitRunner struct {
	localExists  bool
	localSHA     string
	remoteSHA    string
	remoteExists bool

	creates []resetCall
	resets  []resetCall

	resetErr error
}

func (f *fakeGitRunner) ResolveSHA(ref string) (string, error) { return ref, nil }
func (f *fakeGitRunner) LocalBranchExists(string) (bool, error) {
	return f.localExists, nil
}
func (f *fakeGitRunner) LocalBranchSHA(string) (string, error) { return f.localSHA, nil }
func (f *fakeGitRunner) RemoteBranchSHA(string, string) (string, bool, error) {
	return f.remoteSHA, f.remoteExists, nil
}
func (f *fakeGitRunner) CreateBranch(name, sha string) error {
	f.creates = append(f.creates, resetCall{name: name, sha: sha})
	return nil
}
func (f *fakeGitRunner) ResetBranch(remote, name, sha string) error {
	f.resets = append(f.resets, resetCall{remote: remote, name: name, sha: sha})
	return f.resetErr
}

// recordingResetRunner wraps a real gitRunner (execGitRunner in the integration
// tests) and intercepts ResetBranch so a PlanChain run against a real scratch
// repo can assert the self-heal fired without force-pushing to a live remote.
type recordingResetRunner struct {
	GitRunner
	resets   []resetCall
	resetErr error
}

func (r *recordingResetRunner) ResetBranch(remote, name, sha string) error {
	r.resets = append(r.resets, resetCall{remote: remote, name: name, sha: sha})
	return r.resetErr
}

// --- (a) orphan + not diverged + single-flight ran -> self-heal -------------

func TestReconcileBranch_OrphanNotDivergedCheckedResets(t *testing.T) {
	fr := &fakeGitRunner{localExists: true, localSHA: "staletip"}
	p := &Planner{remote: "origin", gitRunner: fr}

	created, reset, err := p.reconcileBranch("env/test", "basesha", false /*diverged*/, true /*singleFlightChecked*/)
	if err != nil {
		t.Fatalf("reconcileBranch: %v", err)
	}
	if created {
		t.Error("created should be false on a self-heal of an existing branch")
	}
	if !reset {
		t.Error("reset should be true when the orphan branch was healed")
	}
	if len(fr.resets) != 1 {
		t.Fatalf("expected exactly one ResetBranch call, got %d", len(fr.resets))
	}
	got := fr.resets[0]
	if got.remote != "origin" || got.name != "env/test" || got.sha != "basesha" {
		t.Errorf("ResetBranch called with %+v, want {origin env/test basesha}", got)
	}
}

func TestReconcileBranch_OrphanDryRunDoesNotReset(t *testing.T) {
	fr := &fakeGitRunner{localExists: true, localSHA: "staletip"}
	p := &Planner{remote: "origin", gitRunner: fr, dryRun: true}

	created, reset, err := p.reconcileBranch("env/test", "basesha", false, true)
	if err != nil {
		t.Fatalf("reconcileBranch: %v", err)
	}
	if created {
		t.Error("created should be false in a dry-run self-heal")
	}
	if !reset {
		t.Error("dry-run should still report it WOULD reset the orphan branch")
	}
	if len(fr.resets) != 0 {
		t.Errorf("dry-run must not call ResetBranch, got %d calls", len(fr.resets))
	}
}

// --- (b) not diverged but single-flight NOT checked -> fail-closed ----------

// This is the critical safety case: without a real single-flight check the
// planner cannot prove the env is not carrying a live in-flight hotfix, so it
// must never reset even though the env is not diverged.
func TestReconcileBranch_NotCheckedFailsClosedNoReset(t *testing.T) {
	fr := &fakeGitRunner{localExists: true, localSHA: "staletip"}
	p := &Planner{remote: "origin", gitRunner: fr}

	created, reset, err := p.reconcileBranch("env/test", "basesha", false /*diverged*/, false /*singleFlightChecked*/)
	if err == nil {
		t.Fatal("expected fail-closed divergence error, got nil")
		return
	}
	if created || reset {
		t.Errorf("created=%v reset=%v, both must be false when failing closed", created, reset)
	}
	if len(fr.resets) != 0 {
		t.Fatalf("ResetBranch must NOT be called without a real single-flight check, got %d calls", len(fr.resets))
	}
	msg := err.Error()
	if !strings.Contains(msg, "abandoned hotfix branch") || !strings.Contains(msg, "--repo") {
		t.Errorf("error %q should give the not-diverged actionable guidance", msg)
	}
}

// --- (c) diverged + tip mismatch -> fail-closed (diverged variant) ----------

func TestReconcileBranch_DivergedFailsClosedNoReset(t *testing.T) {
	fr := &fakeGitRunner{localExists: true, localSHA: "staletip"}
	p := &Planner{remote: "origin", gitRunner: fr}

	// diverged=true models a live recorded divergence; even with a real checker
	// the branch legitimately leads its base and must not be reset.
	created, reset, err := p.reconcileBranch("env/test", "basesha", true /*diverged*/, true /*singleFlightChecked*/)
	if err == nil {
		t.Fatal("expected fail-closed divergence error, got nil")
		return
	}
	if created || reset {
		t.Errorf("created=%v reset=%v, both must be false when failing closed", created, reset)
	}
	if len(fr.resets) != 0 {
		t.Fatalf("ResetBranch must NOT be called for a diverged env, got %d calls", len(fr.resets))
	}
	if !strings.Contains(err.Error(), "in-progress hotfix") {
		t.Errorf("error %q should be the diverged variant", err.Error())
	}
}

// --- (d) stacked re-entry: tip == base -> untouched -------------------------

func TestReconcileBranch_TipMatchesBaseUntouched(t *testing.T) {
	fr := &fakeGitRunner{localExists: true, localSHA: "basesha"}
	p := &Planner{remote: "origin", gitRunner: fr}

	created, reset, err := p.reconcileBranch("env/test", "basesha", false, true)
	if err != nil {
		t.Fatalf("reconcileBranch: %v", err)
	}
	if created || reset {
		t.Errorf("created=%v reset=%v, both must be false when the tip already matches", created, reset)
	}
	if len(fr.resets) != 0 || len(fr.creates) != 0 {
		t.Errorf("an in-sync branch must not be created or reset; resets=%d creates=%d", len(fr.resets), len(fr.creates))
	}
}

func TestReconcileBranch_SelfHealReportsResetError(t *testing.T) {
	fr := &fakeGitRunner{localExists: true, localSHA: "staletip", resetErr: errors.New("push rejected")}
	p := &Planner{remote: "origin", gitRunner: fr}

	_, _, err := p.reconcileBranch("env/test", "basesha", false, true)
	if err == nil {
		t.Fatal("expected the ResetBranch failure to surface")
		return
	}
	if !strings.Contains(err.Error(), "self-healing orphan") {
		t.Errorf("error %q should wrap the self-heal failure", err.Error())
	}
}

// --- (e) chain.go mirror: verifyRemoteEnvTip --------------------------------

func TestVerifyRemoteEnvTip_OrphanNotDivergedCheckedResets(t *testing.T) {
	fr := &fakeGitRunner{remoteExists: true, remoteSHA: "staletip"}
	p := &Planner{remote: "origin", gitRunner: fr}

	reset, err := p.verifyRemoteEnvTip("env/test", "basesha", false /*diverged*/, true /*singleFlightChecked*/)
	if err != nil {
		t.Fatalf("verifyRemoteEnvTip: %v", err)
	}
	if !reset {
		t.Error("reset should be true when the remote orphan tip was healed")
	}
	if len(fr.resets) != 1 || fr.resets[0].sha != "basesha" {
		t.Fatalf("expected one ResetBranch to basesha, got %+v", fr.resets)
	}
}

func TestVerifyRemoteEnvTip_NotCheckedFailsClosedNoReset(t *testing.T) {
	fr := &fakeGitRunner{remoteExists: true, remoteSHA: "staletip"}
	p := &Planner{remote: "origin", gitRunner: fr}

	reset, err := p.verifyRemoteEnvTip("env/test", "basesha", false, false /*singleFlightChecked*/)
	if err == nil {
		t.Fatal("expected fail-closed divergence error, got nil")
	}
	if reset {
		t.Error("reset must be false when failing closed")
	}
	if len(fr.resets) != 0 {
		t.Fatalf("ResetBranch must NOT be called without a real single-flight check, got %d", len(fr.resets))
	}
}

func TestVerifyRemoteEnvTip_DivergedFailsClosedNoReset(t *testing.T) {
	fr := &fakeGitRunner{remoteExists: true, remoteSHA: "staletip"}
	p := &Planner{remote: "origin", gitRunner: fr}

	reset, err := p.verifyRemoteEnvTip("env/test", "basesha", true /*diverged*/, true)
	if err == nil {
		t.Fatal("expected fail-closed divergence error, got nil")
		return
	}
	if reset {
		t.Error("reset must be false for a diverged env")
	}
	if len(fr.resets) != 0 {
		t.Fatalf("ResetBranch must NOT be called for a diverged env, got %d", len(fr.resets))
	}
	if !strings.Contains(err.Error(), "in-progress hotfix") {
		t.Errorf("error %q should be the diverged variant", err.Error())
	}
}

func TestVerifyRemoteEnvTip_AbsentRemoteIsClean(t *testing.T) {
	fr := &fakeGitRunner{remoteExists: false}
	p := &Planner{remote: "origin", gitRunner: fr}

	reset, err := p.verifyRemoteEnvTip("env/test", "basesha", false, true)
	if err != nil {
		t.Fatalf("verifyRemoteEnvTip: %v", err)
	}
	if reset || len(fr.resets) != 0 {
		t.Errorf("an absent remote ref must not trigger a reset; reset=%v calls=%d", reset, len(fr.resets))
	}
}

// --- (e) chain.go mirror: PlanChain integration -----------------------------

// TestPlanChain_OrphanRemoteTipSelfHealsWithRealChecker proves the production
// path: a real PRChecker reports no open hotfix PR, the remote env tip is an
// abandoned orphan (not diverged), and PlanChain self-heals it back to the
// recorded base instead of aborting.
func TestPlanChain_OrphanRemoteTipSelfHealsWithRealChecker(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	orphanTip := commitFile(t, "b.txt", "two", "abandoned hotfix tip")
	fix := commitFile(t, "c.txt", "three", "fix on trunk")

	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: fix},
		"test": {sha: base},
		"prod": {sha: base},
	})
	// Remote env/test points at the abandoned orphan tip, not the recorded base.
	setRemoteEnvTip(t, "test", orphanTip)

	rr := &recordingResetRunner{GitRunner: execGitRunner{}}
	checker := &stubPRChecker{prs: nil}
	p := newPlanner(t, manifest, WithPRChecker(checker))
	p.gitRunner = rr

	res, err := p.PlanChain([]string{fix}, "test")
	if err != nil {
		t.Fatalf("PlanChain should self-heal the orphan, got: %v", err)
	}
	if len(res.Envs) != 1 {
		t.Fatalf("expected one env plan, got %d", len(res.Envs))
	}
	if !res.Envs[0].BranchReset {
		t.Error("expected BranchReset=true on the healed env plan")
	}
	if len(rr.resets) != 1 || rr.resets[0].name != "env/test" || rr.resets[0].sha != base {
		t.Fatalf("expected one ResetBranch(env/test -> base), got %+v", rr.resets)
	}
	if checker.calledWith != "env/test" {
		t.Errorf("single-flight checker queried %q, want env/test", checker.calledWith)
	}
}

// TestPlanChain_OrphanRemoteTipNoopCheckerFailsClosed is the safety control for
// the chain path: without a real checker the same stale remote tip must abort
// fail-closed and never reset.
func TestPlanChain_OrphanRemoteTipNoopCheckerFailsClosed(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	orphanTip := commitFile(t, "b.txt", "two", "abandoned hotfix tip")
	fix := commitFile(t, "c.txt", "three", "fix on trunk")

	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: fix},
		"test": {sha: base},
		"prod": {sha: base},
	})
	setRemoteEnvTip(t, "test", orphanTip)

	rr := &recordingResetRunner{GitRunner: execGitRunner{}}
	p := newPlanner(t, manifest) // no PRChecker: realPRChecker stays false
	p.gitRunner = rr

	_, err := p.PlanChain([]string{fix}, "test")
	if err == nil {
		t.Fatal("expected fail-closed divergence error without a real checker")
		return
	}
	if len(rr.resets) != 0 {
		t.Fatalf("ResetBranch must NOT be called without a real single-flight check, got %d", len(rr.resets))
	}
	if !strings.Contains(err.Error(), "abandoned hotfix branch") {
		t.Errorf("error %q should give the not-diverged guidance", err.Error())
	}
}

// TestPlanChain_AbortsOnOpenConflictPR proves that an open cascade-hotfix-conflict
// PR - a conflict resolution PR with active human work on env/<branch> - blocks
// the single-flight gate and prevents any branch reset, just as a cascade-hotfix
// PR does. The label distinction (cascade-hotfix vs cascade-hotfix-conflict) is
// resolved at the restPRChecker layer; above that layer both appear as an open
// OpenPR. This test drives the Plan path with a stubPRChecker that simulates the
// conflict-label case to assert the abort-no-reset behavior end-to-end.
func TestPlanChain_AbortsOnOpenConflictPR(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	orphanTip := commitFile(t, "b.txt", "two", "live conflict hotfix tip")
	fix := commitFile(t, "c.txt", "three", "fix on trunk")

	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: fix},
		"test": {sha: base},
		"prod": {sha: base},
	})
	setRemoteEnvTip(t, "test", orphanTip)

	rr := &recordingResetRunner{GitRunner: execGitRunner{}}
	// Stub returns a PR as if the restPRChecker found one labeled cascade-hotfix-conflict.
	checker := &stubPRChecker{prs: []OpenPR{{Number: 55, URL: "https://example.test/pr/55"}}}
	p := newPlanner(t, manifest, WithPRChecker(checker))
	p.gitRunner = rr

	_, err := p.PlanChain([]string{fix}, "test")
	if err == nil {
		t.Fatal("expected single-flight abort when a conflict-resolution hotfix PR is open")
		return
	}
	if !strings.Contains(err.Error(), "55") {
		t.Errorf("error %q should reference the open PR number", err.Error())
	}
	if len(rr.resets) != 0 {
		t.Fatalf("an open-conflict-PR abort must not reset any branch, got %d resets", len(rr.resets))
	}
}

// TestPlanChain_AbortsOnOpenHotfixPR proves PlanChain now runs the single-flight
// gate the chain path previously lacked: an open resolution PR aborts the plan
// before any branch is touched.
func TestPlanChain_AbortsOnOpenHotfixPR(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	orphanTip := commitFile(t, "b.txt", "two", "live hotfix tip")
	fix := commitFile(t, "c.txt", "three", "fix on trunk")

	manifest := writeManifestFull(t, []string{"dev", "test", "prod"}, map[string]envSpec{
		"dev":  {sha: fix},
		"test": {sha: base},
		"prod": {sha: base},
	})
	setRemoteEnvTip(t, "test", orphanTip)

	rr := &recordingResetRunner{GitRunner: execGitRunner{}}
	checker := &stubPRChecker{prs: []OpenPR{{Number: 77, URL: "https://example.test/pr/77"}}}
	p := newPlanner(t, manifest, WithPRChecker(checker))
	p.gitRunner = rr

	_, err := p.PlanChain([]string{fix}, "test")
	if err == nil {
		t.Fatal("expected single-flight abort when a hotfix PR is open")
		return
	}
	if !strings.Contains(err.Error(), "77") {
		t.Errorf("error %q should reference the open PR number", err.Error())
	}
	if len(rr.resets) != 0 {
		t.Fatalf("an open-PR abort must touch nothing, got %d resets", len(rr.resets))
	}
}

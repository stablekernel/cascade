package hotfix

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/release"
)

// stubReleaseManager records the release operations finalize performs so tests
// can assert on the tag, body, and action without a live GitHub API.
type stubReleaseManager struct {
	calls []release.Options
	err   error
}

func (s *stubReleaseManager) Manage(opts release.Options) (*release.Result, error) {
	s.calls = append(s.calls, opts)
	if s.err != nil {
		return nil, s.err
	}
	return &release.Result{ReleaseID: int64(len(s.calls)), HTMLURL: "https://example.test/releases/" + opts.Tag}, nil
}

// stubTagLister returns a fixed set of existing tags for version allocation.
type stubTagLister struct {
	tags []string
}

func (s stubTagLister) ListTags() ([]string, error) { return s.tags, nil }

// recordingPusher records that the manifest commit/push happened and how many
// times, so idempotency tests can assert the state write occurs exactly once.
type recordingPusher struct {
	calls    int
	messages []string
	branches []string
}

func (r *recordingPusher) CommitAndPush(path, branch, message string) error {
	r.calls++
	r.messages = append(r.messages, message)
	r.branches = append(r.branches, branch)
	return nil
}

// stubTrunkReader returns fixed manifest bytes as the trunk view, decoupling the
// prior-state read from the working tree. When bytes is set it is returned
// verbatim; otherwise the on-disk manifest at path is returned, modeling a trunk
// that matches the checked-out tree.
type stubTrunkReader struct {
	bytes  []byte
	branch string // records the trunk branch finalize resolved
	err    error
}

func (s *stubTrunkReader) ReadManifest(path, trunk string) ([]byte, error) {
	s.branch = trunk
	if s.err != nil {
		return nil, s.err
	}
	if s.bytes != nil {
		return s.bytes, nil
	}
	return os.ReadFile(path) //nolint:gosec // test path under t.TempDir
}

type envFixture struct {
	sha     string
	version string
	ref     string
	baseSHA string
	patches []string
}

// writeFinalizeManifest writes a manifest with the given environments and a rich
// per-env state block, returning its path.
func writeFinalizeManifest(t *testing.T, envs []string, states map[string]envFixture) string {
	t.Helper()

	var b strings.Builder
	b.WriteString("ci:\n")
	b.WriteString("  config:\n")
	b.WriteString("    trunk_branch: main\n")
	b.WriteString("    environments:\n")
	for _, e := range envs {
		b.WriteString("      - " + e + "\n")
	}
	b.WriteString("  state:\n")
	for e, f := range states {
		b.WriteString("    " + e + ":\n")
		b.WriteString("      sha: " + f.sha + "\n")
		b.WriteString("      version: " + f.version + "\n")
		if f.ref != "" {
			b.WriteString("      ref: " + f.ref + "\n")
			b.WriteString("      base_sha: " + f.baseSHA + "\n")
			if len(f.patches) > 0 {
				b.WriteString("      patches:\n")
				for _, p := range f.patches {
					b.WriteString("        - " + p + "\n")
				}
			}
		}
	}

	path := filepath.Join(".", "manifest.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// newFinalizer builds a Finalizer over the manifest with the supplied stubs. It
// injects a default trunk reader that returns the on-disk manifest bytes so
// tests that do not exercise the trunk-vs-tree distinction model a trunk that
// matches the checked-out tree. A caller-supplied WithTrunkStateReader appears
// later in opts and wins.
func newFinalizer(t *testing.T, manifest string, opts ...FinalizeOption) *Finalizer {
	t.Helper()
	all := append([]FinalizeOption{WithTrunkStateReader(&stubTrunkReader{})}, opts...)
	f, err := NewFinalizer(FinalizerOptions{ConfigPath: manifest, ManifestKey: "ci", Actor: "tester"}, all...)
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	return f
}

// loadState reparses the manifest from disk and returns the target env state.
func loadState(t *testing.T, manifest, env string) *config.EnvState {
	t.Helper()
	cicd, err := config.ParseManifestFile(manifest, "ci")
	if err != nil {
		t.Fatalf("reparse manifest: %v", err)
	}
	return cicd.State[env]
}

// TestFinalizeHotfixMutatePreservesUntouchedEnv verifies that applying the
// hotfix state mutation for one env on a manifest leaves every other env's
// recorded state untouched, so re-applying it against re-fetched trunk bytes in
// the optimistic-lock loop merges rather than clobbers concurrent writers.
func TestFinalizeHotfixMutatePreservesUntouchedEnv(t *testing.T) {
	cicd := &config.CICDFile{State: map[string]*config.EnvState{
		"dev":     {SHA: "dev-sha", Version: "v1.0.0"},
		"staging": {SHA: "stg-sha", Version: "v1.1.0"},
	}}
	f := &Finalizer{actor: "tester", manifestKey: "ci"}

	err := f.applyHotfixState(cicd, "staging", "merge-sha", "v1.1.1", "base-sha", "2026-01-01T00:00:00Z", []string{"fix-sha"})
	if err != nil {
		t.Fatalf("applyHotfixState: %v", err)
	}

	if cicd.State["dev"].SHA != "dev-sha" {
		t.Errorf("dev.sha = %q, want unchanged dev-sha", cicd.State["dev"].SHA)
	}
	if cicd.State["staging"].SHA != "merge-sha" {
		t.Errorf("staging.sha = %q, want merge-sha", cicd.State["staging"].SHA)
	}
	if cicd.State["staging"].Version != "v1.1.1" {
		t.Errorf("staging.version = %q, want v1.1.1", cicd.State["staging"].Version)
	}
}

func TestFinalize_WritesDivergedState(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix on trunk")

	// env/test exists at the merge SHA (cherry-pick of the fix onto base). For
	// the unit we model the merge SHA as a real commit on a hotfix branch.
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cherry-pick fix")
	runGit(t, "checkout", "main")

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	rm := &stubReleaseManager{}
	f := newFinalizer(t, manifest,
		WithReleaseManager(rm),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
	)
	f.SetDeployResult("api", "success")

	if err := f.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	st := loadState(t, manifest, "test")
	if st.SHA != merge {
		t.Errorf("state.sha = %q, want merge SHA %q", st.SHA, merge)
	}
	if st.Version != "v1.4.0-rc.2.hotfix.1" {
		t.Errorf("state.version = %q, want v1.4.0-rc.2.hotfix.1", st.Version)
	}
	if st.Ref != "env/test" {
		t.Errorf("state.ref = %q, want env/test", st.Ref)
	}
	if st.BaseSHA != base {
		t.Errorf("state.base_sha = %q, want %q", st.BaseSHA, base)
	}
	if len(st.Patches) != 1 || st.Patches[0] != fix {
		t.Errorf("state.patches = %v, want [%s]", st.Patches, fix)
	}
	if !st.IsDiverged() {
		t.Error("finalized hotfix state should report IsDiverged")
	}
	if st.CommittedBy != "tester" {
		t.Errorf("committed_by = %q, want tester", st.CommittedBy)
	}

	// A hotfix tag/release was created for the merge SHA.
	if len(rm.calls) == 0 {
		t.Fatal("expected at least one release Manage call")
	}
	create := rm.calls[0]
	if create.Action != release.ActionCreate {
		t.Errorf("first action = %q, want create", create.Action)
	}
	if create.Tag != "v1.4.0-rc.2.hotfix.1" {
		t.Errorf("release tag = %q, want v1.4.0-rc.2.hotfix.1", create.Tag)
	}
	if !create.CreateTag {
		t.Error("hotfix release must create the git tag")
	}
	if create.SHA != merge {
		t.Errorf("release SHA = %q, want merge SHA %q", create.SHA, merge)
	}
	if !strings.Contains(create.Changelog, "based on v1.4.0-rc.2,") {
		t.Errorf("release body should reference the base version with exact phrase: %q", create.Changelog)
	}
	if strings.Contains(create.Changelog, "based on v1.4.0-rc.2.hotfix.1") {
		t.Errorf("release body must not use the hotfix version as the base version: %q", create.Changelog)
	}
	if !strings.Contains(create.Changelog, short(fix)) {
		t.Errorf("release body should reference the carried trunk commit: %q", create.Changelog)
	}
}

func TestFinalize_PreviousRingSnapshot(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	f := newFinalizer(t, manifest,
		WithReleaseManager(&stubReleaseManager{}),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
	)
	if err := f.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	st := loadState(t, manifest, "test")
	if len(st.Previous) != 1 {
		t.Fatalf("expected exactly one Previous snapshot, got %d", len(st.Previous))
	}
	prev := st.Previous[0]
	if prev.SHA != base {
		t.Errorf("snapshot sha = %q, want prior sha %q", prev.SHA, base)
	}
	if prev.Version != "v1.4.0-rc.2" {
		t.Errorf("snapshot version = %q, want prior version v1.4.0-rc.2", prev.Version)
	}
}

func TestFinalize_PreviousRingBounded(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")

	// Seed test with a deploy-history ring already at the cap, then finalize a
	// genuine transition: the new snapshot prepends and the oldest is dropped, so
	// the ring stays bounded at MaxPreviousSnapshots.
	var b strings.Builder
	b.WriteString("ci:\n  config:\n    trunk_branch: main\n    environments:\n")
	for _, e := range []string{"dev", "test", "prod"} {
		b.WriteString("      - " + e + "\n")
	}
	b.WriteString("  state:\n")
	b.WriteString("    dev:\n      sha: " + fix + "\n      version: v1.4.0-rc.2\n")
	b.WriteString("    prod:\n      sha: " + base + "\n      version: v1.4.0-rc.2\n")
	b.WriteString("    test:\n      sha: " + base + "\n      version: v1.4.0-rc.2\n")
	b.WriteString("      previous:\n")
	for i := 0; i < config.MaxPreviousSnapshots; i++ {
		b.WriteString("        - sha: seed" + strconv.Itoa(i) + "\n          version: v0.0." + strconv.Itoa(i) + "\n")
	}
	path := filepath.Join(".", "manifest.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	f := newFinalizer(t, path,
		WithReleaseManager(&stubReleaseManager{}),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
	)
	if err := f.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	st := loadState(t, path, "test")
	if len(st.Previous) != config.MaxPreviousSnapshots {
		t.Fatalf("previous ring len = %d, want %d (bounded)", len(st.Previous), config.MaxPreviousSnapshots)
	}
	// Newest snapshot is the outgoing pre-hotfix state; oldest seed was dropped.
	if st.Previous[0].SHA != base {
		t.Errorf("newest snapshot sha = %q, want prior sha %q", st.Previous[0].SHA, base)
	}
	if st.Previous[len(st.Previous)-1].SHA == "seed0" {
		t.Errorf("oldest seed snapshot was not evicted: %+v", st.Previous)
	}
}

func TestFinalize_RecordsAllCommitsOfMultiCommitSet(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix1 := commitFile(t, "b.txt", "two", "first fix")
	fix2 := commitFile(t, "d.txt", "four", "second fix")

	// env/test exists at base; the resolution merged a cherry-pick of both fixes,
	// so its tip is the merge SHA the workflow finalizes against.
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	commitFile(t, "c.txt", "fixed1", "cp first")
	merge := commitFile(t, "e.txt", "fixed2", "cp second")
	runGit(t, "checkout", "main")

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix2, version: "v1.4.0-rc.2"},
		"test": {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	f := newFinalizer(t, manifest,
		WithReleaseManager(&stubReleaseManager{}),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
	)

	// A two-commit hotfix set must record BOTH original trunk SHAs, not just the
	// first. Finalizing an env with empty patches using both commits yields two
	// patch entries.
	if err := f.Finalize("test", merge, []string{fix1, fix2}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	st := loadState(t, manifest, "test")
	if len(st.Patches) != 2 {
		t.Fatalf("state.patches = %v, want both commits recorded (len 2)", st.Patches)
	}
	if st.Patches[0] != fix1 || st.Patches[1] != fix2 {
		t.Errorf("state.patches = %v, want [%s %s]", st.Patches, fix1, fix2)
	}
}

func TestFinalize_StacksSecondHotfix(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix1 := commitFile(t, "b.txt", "two", "first fix")
	fix2 := commitFile(t, "d.txt", "four", "second fix")

	// env/test already carries the first hotfix; its tip is merge1. The second
	// hotfix stacks another commit (merge2) on top.
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	commitFile(t, "c.txt", "fixed", "cp first")
	merge2 := commitFile(t, "e.txt", "fixed2", "cp second")
	tip := gitOut(t, "rev-parse", "env/test")
	runGit(t, "checkout", "main")
	_ = merge2

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev": {sha: fix2, version: "v1.4.0-rc.2"},
		"test": {
			sha:     gitOut(t, "rev-parse", "env/test~1"),
			version: "v1.4.0-rc.2.hotfix.1",
			ref:     "env/test",
			baseSHA: base,
			patches: []string{fix1},
		},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	f := newFinalizer(t, manifest,
		WithReleaseManager(&stubReleaseManager{}),
		WithTagLister(stubTagLister{tags: []string{"v1.4.0-rc.2.hotfix.1"}}),
		WithStatePusher(&recordingPusher{}),
	)
	if err := f.Finalize("test", tip, []string{fix2}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	st := loadState(t, manifest, "test")
	if st.Version != "v1.4.0-rc.2.hotfix.2" {
		t.Errorf("second hotfix version = %q, want v1.4.0-rc.2.hotfix.2", st.Version)
	}
	if st.BaseSHA != base {
		t.Errorf("base_sha = %q, want carried-forward %q", st.BaseSHA, base)
	}
	if len(st.Patches) != 2 || st.Patches[0] != fix1 || st.Patches[1] != fix2 {
		t.Errorf("patches = %v, want [%s %s]", st.Patches, fix1, fix2)
	}
}

func TestFinalize_MergeSHATipMismatch_Fails(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")
	// other is NOT the tip of env/test.
	other := commitFile(t, "z.txt", "zee", "unrelated")

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	f := newFinalizer(t, manifest,
		WithReleaseManager(&stubReleaseManager{}),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
	)
	err := f.Finalize("test", other, []string{fix}, base)
	if err == nil {
		t.Fatal("expected mismatch error when merge SHA is not env/test tip")
		return
	}
	if !strings.Contains(strings.ToLower(err.Error()), "tip") {
		t.Errorf("error %q should mention the branch tip mismatch", err.Error())
	}
}

func TestFinalize_Idempotent_Rerun(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	pusher := &recordingPusher{}
	rm := &stubReleaseManager{}

	// First run.
	f1 := newFinalizer(t, manifest,
		WithReleaseManager(rm),
		WithTagLister(stubTagLister{}),
		WithStatePusher(pusher),
	)
	if err := f1.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("first Finalize: %v", err)
	}

	st1 := loadState(t, manifest, "test")
	if len(st1.Patches) != 1 {
		t.Fatalf("after first run patches = %v, want one", st1.Patches)
	}

	// Second run with identical inputs; the tag now exists.
	f2 := newFinalizer(t, manifest,
		WithReleaseManager(rm),
		WithTagLister(stubTagLister{tags: []string{"v1.4.0-rc.2.hotfix.1"}}),
		WithStatePusher(pusher),
	)
	if err := f2.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("second Finalize (idempotent): %v", err)
	}

	st2 := loadState(t, manifest, "test")
	if len(st2.Patches) != 1 {
		t.Errorf("rerun double-applied patches: %v", st2.Patches)
	}
	if st2.Version != "v1.4.0-rc.2.hotfix.1" {
		t.Errorf("rerun changed version to %q, want stable v1.4.0-rc.2.hotfix.1", st2.Version)
	}
	if len(st2.Previous) != 1 {
		t.Errorf("rerun double-snapshotted Previous: %d entries", len(st2.Previous))
	}
}

// TestFinalize_RerunAfterReleaseFailure_CreatesRelease reproduces the
// partial-failure interleaving where finalize reports green with the release
// permanently missing: the first run commits the state marker to trunk and the
// release API then fails, so the job goes red with state already recording the
// merge SHA. The rerun lands on the idempotency gate; it must complete the
// missing tag/release with the version the first run recorded rather than
// early-return success and leave the release absent forever.
func TestFinalize_RerunAfterReleaseFailure_CreatesRelease(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	// First run: the state marker lands, then the release API blips.
	pusher1 := &recordingPusher{}
	rm1 := &stubReleaseManager{err: errors.New("API error 503: upstream blip")}
	f1 := newFinalizer(t, manifest,
		WithReleaseManager(rm1),
		WithTagLister(stubTagLister{}),
		WithStatePusher(pusher1),
	)
	if err := f1.Finalize("test", merge, []string{fix}, base); err == nil {
		t.Fatal("first Finalize must surface the release failure")
	}
	if pusher1.calls != 1 {
		t.Fatalf("first run state pushes = %d, want 1", pusher1.calls)
	}
	if st := loadState(t, manifest, "test"); st.SHA != merge {
		t.Fatalf("state marker sha = %q, want merge SHA %q recorded before the release step", st.SHA, merge)
	}

	// Rerun with the API healthy: the idempotency gate must converge the missing
	// tag/release instead of skipping it.
	pusher2 := &recordingPusher{}
	rm2 := &stubReleaseManager{}
	f2 := newFinalizer(t, manifest,
		WithReleaseManager(rm2),
		WithTagLister(stubTagLister{}),
		WithStatePusher(pusher2),
	)
	if err := f2.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("rerun Finalize: %v", err)
	}

	if len(rm2.calls) == 0 {
		t.Fatal("rerun skipped the release step; the hotfix tag/release stays permanently missing")
	}
	got := rm2.calls[0]
	if got.Action != release.ActionUpdate {
		t.Errorf("rerun action = %q, want find-or-create update", got.Action)
	}
	if got.Tag != "v1.4.0-rc.2.hotfix.1" {
		t.Errorf("rerun tag = %q, want the recorded v1.4.0-rc.2.hotfix.1, not a re-allocation", got.Tag)
	}
	if got.SHA != merge {
		t.Errorf("rerun release SHA = %q, want merge SHA %q", got.SHA, merge)
	}
	if !got.CreateTag {
		t.Error("rerun must still materialize the git tag")
	}
	if !strings.Contains(got.Changelog, "based on v1.4.0-rc.2,") {
		t.Errorf("rerun body should reference the base version from the Previous ring: %q", got.Changelog)
	}
	if !strings.Contains(got.Changelog, short(fix)) {
		t.Errorf("rerun body should reference the carried trunk commit: %q", got.Changelog)
	}
	// test is the prerelease env (second from top), so the converged release is
	// still promoted to a GitHub prerelease.
	if len(rm2.calls) != 2 || rm2.calls[1].Action != release.ActionPrerelease {
		t.Errorf("rerun release actions = %+v, want [update prerelease]", releaseActions(rm2.calls))
	}

	// The rerun neither re-pushes state nor double-applies it.
	if pusher2.calls != 0 {
		t.Errorf("rerun state pushes = %d, want 0", pusher2.calls)
	}
	st := loadState(t, manifest, "test")
	if len(st.Patches) != 1 {
		t.Errorf("rerun double-applied patches: %v", st.Patches)
	}
	if len(st.Previous) != 1 {
		t.Errorf("rerun double-snapshotted Previous: %d entries", len(st.Previous))
	}
	if st.Version != "v1.4.0-rc.2.hotfix.1" {
		t.Errorf("rerun changed version to %q, want stable v1.4.0-rc.2.hotfix.1", st.Version)
	}
}

// TestFinalize_RerunDryRun_TouchesNothing guards the dry-run contract on the
// idempotency-gate path: a dry-run rerun over already-recorded state must not
// invoke the release API at all.
func TestFinalize_RerunDryRun_TouchesNothing(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: merge, version: "v1.4.0-rc.2.hotfix.1"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	pusher := &recordingPusher{}
	rm := &stubReleaseManager{}
	f := newFinalizer(t, manifest,
		WithReleaseManager(rm),
		WithTagLister(stubTagLister{}),
		WithStatePusher(pusher),
		WithFinalizeDryRun(true),
	)
	if err := f.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("dry-run rerun Finalize: %v", err)
	}
	if len(rm.calls) != 0 {
		t.Errorf("dry-run rerun made %d release calls, want 0", len(rm.calls))
	}
	if pusher.calls != 0 {
		t.Errorf("dry-run rerun made %d state pushes, want 0", pusher.calls)
	}
}

// releaseActions projects the Manage options to their actions for messages.
func releaseActions(calls []release.Options) []release.Action {
	actions := make([]release.Action, len(calls))
	for i, c := range calls {
		actions[i] = c.Action
	}
	return actions
}

// TestFinalize_StateWriteTargetsTrunkBranch guards that the manifest state write
// targets the configured trunk branch, not the env branch the hotfix PR merged
// into. The finalize job runs on the merged pull_request event whose base is
// env/<target>, so deriving the push branch from the event would write state to
// the wrong branch.
func TestFinalize_StateWriteTargetsTrunkBranch(t *testing.T) {
	newScratchRepo(t)
	runGit(t, "branch", "-m", "trunk")
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "trunk")

	var b strings.Builder
	b.WriteString("ci:\n  config:\n    trunk_branch: trunk\n    environments:\n")
	for _, e := range []string{"dev", "test", "prod"} {
		b.WriteString("      - " + e + "\n")
	}
	b.WriteString("  state:\n")
	for _, e := range []string{"dev", "test", "prod"} {
		sha := base
		if e == "dev" {
			sha = fix
		}
		b.WriteString("    " + e + ":\n      sha: " + sha + "\n      version: v1.4.0-rc.2\n")
	}
	path := filepath.Join(".", "manifest.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	pusher := &recordingPusher{}
	f := newFinalizer(t, path,
		WithReleaseManager(&stubReleaseManager{}),
		WithTagLister(stubTagLister{}),
		WithStatePusher(pusher),
	)
	if err := f.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if len(pusher.branches) != 1 || pusher.branches[0] != "trunk" {
		t.Errorf("state write branch = %v, want [trunk]", pusher.branches)
	}
}

// TestFinalize_ReadsPriorStateFromTrunk guards the trunk-state read: the
// checked-out (env-branch) manifest records no state SHA for the target env,
// because promote finalize writes env state only to trunk. Finalize must read
// prior state from the trunk manifest the reader returns and succeed, rather
// than erroring on the stale env-branch view.
func TestFinalize_ReadsPriorStateFromTrunk(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix on trunk")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cherry-pick fix")
	runGit(t, "checkout", "main")

	// On-disk (env-branch) manifest: test has an EMPTY state block (no sha).
	onDisk := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {}, // empty: no recorded SHA on the lagging env branch
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	// Trunk manifest: test records the real prior state SHA.
	trunkBytes := []byte("ci:\n  config:\n    environments:\n" +
		"      - dev\n      - test\n      - prod\n" +
		"  state:\n" +
		"    dev:\n      sha: " + fix + "\n      version: v1.4.0-rc.2\n" +
		"    test:\n      sha: " + base + "\n      version: v1.4.0-rc.2\n" +
		"    prod:\n      sha: " + base + "\n      version: v1.4.0-rc.2\n")

	reader := &stubTrunkReader{bytes: trunkBytes}
	rm := &stubReleaseManager{}
	f := newFinalizer(t, onDisk,
		WithReleaseManager(rm),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
		WithTrunkStateReader(reader),
	)

	if err := f.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("Finalize should succeed reading prior state from trunk: %v", err)
	}

	st := loadState(t, onDisk, "test")
	if st.SHA != merge {
		t.Errorf("state.sha = %q, want merge SHA %q", st.SHA, merge)
	}
	// The Previous snapshot must derive from the trunk prior SHA (base), proving
	// the prior state came from trunk and not the empty env-branch view.
	if len(st.Previous) != 1 || st.Previous[0].SHA != base {
		t.Errorf("Previous snapshot = %+v, want one entry with prior trunk sha %q", st.Previous, base)
	}
}

// TestFinalize_TrunkIsSourceOfPriorState proves trunk wins over the working
// tree: the on-disk manifest records sha=A for the target env while trunk
// records sha=B. The snapshot's Previous entry must derive from B (trunk), not A.
func TestFinalize_TrunkIsSourceOfPriorState(t *testing.T) {
	newScratchRepo(t)
	shaA := commitFile(t, "a.txt", "one", "A on env branch")
	shaB := commitFile(t, "b.txt", "two", "B on trunk")
	fix := commitFile(t, "f.txt", "fix", "fix on trunk")
	runGit(t, "branch", "env/test", shaA)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cherry-pick")
	runGit(t, "checkout", "main")

	// On-disk env-branch manifest records the stale sha=A.
	onDisk := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: shaA, version: "v1.4.0-rc.2"},
		"prod": {sha: shaA, version: "v1.4.0-rc.2"},
	})

	// Trunk records the real prior sha=B.
	trunkBytes := []byte("ci:\n  config:\n    environments:\n" +
		"      - dev\n      - test\n      - prod\n" +
		"  state:\n" +
		"    test:\n      sha: " + shaB + "\n      version: v1.4.0-rc.2\n")

	f := newFinalizer(t, onDisk,
		WithReleaseManager(&stubReleaseManager{}),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
		WithTrunkStateReader(&stubTrunkReader{bytes: trunkBytes}),
	)
	if err := f.Finalize("test", merge, []string{fix}, shaB); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	st := loadState(t, onDisk, "test")
	if len(st.Previous) != 1 {
		t.Fatalf("expected one Previous snapshot, got %d: %+v", len(st.Previous), st.Previous)
	}
	if st.Previous[0].SHA != shaB {
		t.Errorf("Previous snapshot sha = %q, want trunk prior sha %q (not on-disk %q)", st.Previous[0].SHA, shaB, shaA)
	}
}

// TestFinalize_DoesNotClobberOtherEnvsTrunkState proves the WRITE basis is the
// trunk manifest, not the lagging env-branch one. Trunk records populated state
// for dev, staging (the target), and prod with distinct SHAs. The checked-out
// env-branch manifest carries empty/stale state for those envs (promote writes
// state only to trunk, so the env branch lags; post-reset it can be empty).
//
// After finalize, the written manifest must (a) update staging to the hotfix
// result and (b) RETAIN dev and prod exactly as trunk recorded them. Writing the
// env-branch manifest to trunk would wipe dev and prod; this guards against that.
func TestFinalize_DoesNotClobberOtherEnvsTrunkState(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix on trunk")
	runGit(t, "branch", "env/staging", base)
	runGit(t, "checkout", "env/staging")
	merge := commitFile(t, "c.txt", "fixed", "cherry-pick fix")
	runGit(t, "checkout", "main")

	devTrunkSHA := commitFile(t, "dev.txt", "dev", "dev trunk state")
	prodTrunkSHA := commitFile(t, "prod.txt", "prod", "prod trunk state")

	// On-disk (env-branch) manifest: dev and prod carry stale/empty state, and
	// staging is empty. This is the lagging tree that must NOT become the write
	// basis.
	onDisk := writeFinalizeManifest(t, []string{"dev", "staging", "prod"}, map[string]envFixture{
		"dev":     {sha: "stale-dev", version: "v0.0.1"},
		"staging": {}, // empty on the lagging env branch
		"prod":    {}, // empty on the lagging env branch
	})

	// Trunk manifest: every env has real, distinct recorded state.
	trunkBytes := []byte("ci:\n  config:\n    environments:\n" +
		"      - dev\n      - staging\n      - prod\n" +
		"  state:\n" +
		"    dev:\n      sha: " + devTrunkSHA + "\n      version: v1.4.0-rc.3\n" +
		"    staging:\n      sha: " + base + "\n      version: v1.4.0-rc.2\n" +
		"    prod:\n      sha: " + prodTrunkSHA + "\n      version: v1.4.0-rc.1\n")

	f := newFinalizer(t, onDisk,
		WithReleaseManager(&stubReleaseManager{}),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
		WithTrunkStateReader(&stubTrunkReader{bytes: trunkBytes}),
	)
	if err := f.Finalize("staging", merge, []string{fix}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// (a) staging updated to the hotfix result.
	staging := loadState(t, onDisk, "staging")
	if staging.SHA != merge {
		t.Errorf("staging.sha = %q, want merge SHA %q", staging.SHA, merge)
	}
	if staging.Version != "v1.4.0-rc.2.hotfix.1" {
		t.Errorf("staging.version = %q, want v1.4.0-rc.2.hotfix.1", staging.Version)
	}

	// (b) dev and prod retain their TRUNK state, not the stale env-branch view.
	dev := loadState(t, onDisk, "dev")
	if dev == nil || dev.SHA != devTrunkSHA {
		t.Errorf("dev.sha = %q, want trunk SHA %q (env-branch state must not clobber trunk)", devSHA(dev), devTrunkSHA)
	}
	if dev != nil && dev.Version != "v1.4.0-rc.3" {
		t.Errorf("dev.version = %q, want trunk version v1.4.0-rc.3", dev.Version)
	}
	prod := loadState(t, onDisk, "prod")
	if prod == nil || prod.SHA != prodTrunkSHA {
		t.Errorf("prod.sha = %q, want trunk SHA %q (env-branch state must not clobber trunk)", devSHA(prod), prodTrunkSHA)
	}
	if prod != nil && prod.Version != "v1.4.0-rc.1" {
		t.Errorf("prod.version = %q, want trunk version v1.4.0-rc.1", prod.Version)
	}
}

// devSHA returns the SHA of a possibly-nil EnvState for tidy error messages.
func devSHA(s *config.EnvState) string {
	if s == nil {
		return "<nil>"
	}
	return s.SHA
}

// TestFinalize_TrunkStateAbsent_Errors confirms that when the target env has no
// recorded state on trunk either, finalize still reports the missing-state error
// rather than silently proceeding.
func TestFinalize_TrunkStateAbsent_Errors(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")

	onDisk := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	// Trunk manifest has test present but with no sha recorded.
	trunkBytes := []byte("ci:\n  config:\n    environments:\n" +
		"      - dev\n      - test\n      - prod\n" +
		"  state:\n" +
		"    dev:\n      sha: " + fix + "\n      version: v1.4.0-rc.2\n")

	f := newFinalizer(t, onDisk,
		WithReleaseManager(&stubReleaseManager{}),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
		WithTrunkStateReader(&stubTrunkReader{bytes: trunkBytes}),
	)
	err := f.Finalize("test", merge, []string{fix}, base)
	if err == nil {
		t.Fatal("expected missing-state error when trunk has no recorded SHA for the target env")
		return
	}
	if !strings.Contains(err.Error(), "no recorded state SHA") {
		t.Errorf("error %q should report the missing recorded state SHA", err.Error())
	}
}

// TestReleaseToken_FallsBackThroughEnvVars verifies the token resolution order:
// RELEASE_TOKEN, then GITHUB_TOKEN, then GH_TOKEN. GH_TOKEN is the reliable
// fallback because the runner does not always propagate the reserved
// GITHUB_TOKEN name as a step env var.
func TestReleaseToken_FallsBackThroughEnvVars(t *testing.T) {
	tests := []struct {
		name string
		set  map[string]string
		want string
	}{
		{name: "release token wins", set: map[string]string{"RELEASE_TOKEN": "rel", "GITHUB_TOKEN": "gh", "GH_TOKEN": "ghx"}, want: "rel"},
		{name: "github token next", set: map[string]string{"GITHUB_TOKEN": "gh", "GH_TOKEN": "ghx"}, want: "gh"},
		{name: "gh token fallback", set: map[string]string{"GH_TOKEN": "ghx"}, want: "ghx"},
		{name: "none set", set: map[string]string{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RELEASE_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")
			t.Setenv("GH_TOKEN", "")
			for k, v := range tt.set {
				t.Setenv(k, v)
			}
			if got := releaseToken(); got != tt.want {
				t.Errorf("releaseToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFinalize_VersionAllocation_SkipsExistingTags(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	// hotfix.1 and hotfix.2 tags already exist; allocation must skip to hotfix.3.
	rm := &stubReleaseManager{}
	f := newFinalizer(t, manifest,
		WithReleaseManager(rm),
		WithTagLister(stubTagLister{tags: []string{"v1.4.0-rc.2.hotfix.1", "v1.4.0-rc.2.hotfix.2"}}),
		WithStatePusher(&recordingPusher{}),
	)
	if err := f.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	st := loadState(t, manifest, "test")
	if st.Version != "v1.4.0-rc.2.hotfix.3" {
		t.Errorf("version = %q, want v1.4.0-rc.2.hotfix.3 (skipping existing tags)", st.Version)
	}
	if rm.calls[0].Tag != "v1.4.0-rc.2.hotfix.3" {
		t.Errorf("release tag = %q, want v1.4.0-rc.2.hotfix.3", rm.calls[0].Tag)
	}
}

func TestFinalize_PublishedBase_PatchBump_NoCollision(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/test", base)
	runGit(t, "checkout", "env/test")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")

	// test holds a PUBLISHED version v1.3.0 (no rc segment).
	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.1"},
		"test": {sha: base, version: "v1.3.0"},
		"prod": {sha: base, version: "v1.3.0"},
	})

	// v1.3.1 already exists as a tag (e.g. the normal release flow minted it);
	// allocation must skip it and choose v1.3.2 to avoid a collision.
	rm := &stubReleaseManager{}
	f := newFinalizer(t, manifest,
		WithReleaseManager(rm),
		WithTagLister(stubTagLister{tags: []string{"v1.3.0", "v1.3.1"}}),
		WithStatePusher(&recordingPusher{}),
	)
	if err := f.Finalize("test", merge, []string{fix}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	st := loadState(t, manifest, "test")
	if st.Version != "v1.3.2" {
		t.Errorf("published-base hotfix version = %q, want v1.3.2 (patch bump skipping existing v1.3.1)", st.Version)
	}
	if strings.Contains(st.Version, "hotfix") {
		t.Errorf("published-base hotfix must NOT use a -hotfix.M segment: %q", st.Version)
	}
}

func TestFinalize_PrereleaseEnv_ReplacesPrerelease(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "b.txt", "two", "fix")
	runGit(t, "branch", "env/uat", base)
	runGit(t, "checkout", "env/uat")
	merge := commitFile(t, "c.txt", "fixed", "cp")
	runGit(t, "checkout", "main")

	// uat is the prerelease env (second from top). A hotfix there must promote
	// the release object to a GitHub prerelease, replacing the env's current one.
	manifest := writeFinalizeManifest(t, []string{"dev", "test", "uat", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: fix, version: "v1.4.0-rc.2"},
		"uat":  {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	rm := &stubReleaseManager{}
	f := newFinalizer(t, manifest,
		WithReleaseManager(rm),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
	)
	if err := f.Finalize("uat", merge, []string{fix}, base); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// The release flow must reach a prerelease action for the hotfix tag.
	var sawPrerelease bool
	for _, c := range rm.calls {
		if c.Action == release.ActionPrerelease {
			sawPrerelease = true
		}
	}
	if !sawPrerelease {
		t.Errorf("prerelease-env hotfix should promote the release to a prerelease; calls=%+v", rm.calls)
	}
}

// TestEnvTipReader_ResolvesLocalAndRemoteRefs verifies the finalize tip reader
// resolves an env branch from a local ref when present and falls back to the
// origin-tracking ref otherwise (the shape the finalize job's checkout has,
// where env/<target> is fetched into refs/remotes/origin/* but not checked out
// as a local branch).
func TestEnvTipReader_ResolvesLocalAndRemoteRefs(t *testing.T) {
	reader := envTipReader{}

	t.Run("local ref", func(t *testing.T) {
		newScratchRepo(t)
		sha := commitFile(t, "a.txt", "one", "first commit")
		runGit(t, "branch", "env/test")

		got, err := reader.LocalBranchSHA("env/test")
		if err != nil {
			t.Fatalf("LocalBranchSHA(local): %v", err)
		}
		if got != sha {
			t.Errorf("LocalBranchSHA(local) = %q, want %q", got, sha)
		}
	})

	t.Run("origin-tracking ref only", func(t *testing.T) {
		newScratchRepo(t)
		sha := commitFile(t, "a.txt", "one", "first commit")
		// Simulate the finalize-job checkout: env/test exists only as a
		// remote-tracking ref, not as a local branch.
		runGit(t, "update-ref", "refs/remotes/origin/env/test", sha)

		got, err := reader.LocalBranchSHA("env/test")
		if err != nil {
			t.Fatalf("LocalBranchSHA(remote): %v", err)
		}
		if got != sha {
			t.Errorf("LocalBranchSHA(remote) = %q, want %q", got, sha)
		}
	})

	t.Run("missing ref errors", func(t *testing.T) {
		newScratchRepo(t)
		commitFile(t, "a.txt", "one", "first commit")

		if _, err := reader.LocalBranchSHA("env/absent"); err == nil {
			t.Fatalf("LocalBranchSHA(absent) expected error, got nil")
		}
	})
}

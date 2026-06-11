package hotfix

import (
	"os"
	"path/filepath"
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
}

func (r *recordingPusher) CommitAndPush(path, message string) error {
	r.calls++
	r.messages = append(r.messages, message)
	return nil
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

// newFinalizer builds a Finalizer over the manifest with the supplied stubs.
func newFinalizer(t *testing.T, manifest string, opts ...FinalizeOption) *Finalizer {
	t.Helper()
	f, err := NewFinalizer(FinalizerOptions{ConfigPath: manifest, ManifestKey: "ci", Actor: "tester"}, opts...)
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

	if err := f.Finalize("test", merge, fix, base); err != nil {
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
	if err := f.Finalize("test", merge, fix, base); err != nil {
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
	if err := f.Finalize("test", tip, fix2, base); err != nil {
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
	err := f.Finalize("test", other, fix, base)
	if err == nil {
		t.Fatal("expected mismatch error when merge SHA is not env/test tip")
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
	if err := f1.Finalize("test", merge, fix, base); err != nil {
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
	if err := f2.Finalize("test", merge, fix, base); err != nil {
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
	if err := f.Finalize("test", merge, fix, base); err != nil {
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
	if err := f.Finalize("test", merge, fix, base); err != nil {
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
	if err := f.Finalize("uat", merge, fix, base); err != nil {
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

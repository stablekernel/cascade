package simulate

import (
	"fmt"
	"os"

	"github.com/stablekernel/cascade/internal/hotfix"
	"github.com/stablekernel/cascade/internal/release"
)

// HotfixAction replays the real hotfix finalize orchestration against a cloned
// manifest. It drives hotfix.Finalizer in record-only mode so the genuine
// divergence state machine runs: it allocates the next hotfix version, snapshots
// the prior state into the deploy-history ring, and writes the divergence fields
// (Ref, BaseSHA, Patches) to the clone only. Git, the trunk read, the manifest
// push, and the release API are all replaced with record-only seams, so the
// simulation touches no git and no network.
type HotfixAction struct {
	targetEnv string
	fixSHAs   []string
	mergeSHA  string
}

// NewHotfixAction builds a HotfixAction that simulates landing fixSHAs as a
// hotfix on targetEnv. mergeSHA is the resolution-branch tip the hotfix merges
// to; when empty it defaults to the first fix SHA, which is the common case for
// a single-commit hotfix.
func NewHotfixAction(targetEnv string, fixSHAs []string, mergeSHA string) *HotfixAction {
	return &HotfixAction{targetEnv: targetEnv, fixSHAs: fixSHAs, mergeSHA: mergeSHA}
}

// Name returns the action identifier.
func (a *HotfixAction) Name() string { return "hotfix" }

// Describe returns a one-line summary of the hotfix being simulated.
func (a *HotfixAction) Describe() string {
	return fmt.Sprintf("hotfix (env=%s, commits=%d)", a.targetEnv, len(a.fixSHAs))
}

// Apply drives the real hotfix Finalizer against the clone manifest in
// record-only mode and returns the divergence after-state plus an ordered effect
// sequence. The Finalizer writes the diverged state to the clone via os.WriteFile
// only; the injected pusher and release manager record what the workflow would
// commit and publish without performing it.
func (a *HotfixAction) Apply(ctx ActionContext) (*ActionOutcome, error) {
	if len(a.fixSHAs) == 0 {
		return nil, fmt.Errorf("hotfix needs at least one fix commit")
	}

	mergeSHA := a.mergeSHA
	if mergeSHA == "" {
		mergeSHA = a.fixSHAs[0]
	}

	before, err := parseState(ctx.ClonePath)
	if err != nil {
		return nil, fmt.Errorf("parse clone state: %w", err)
	}
	cur := before[a.targetEnv]
	if cur == nil || cur.SHA == "" {
		return nil, fmt.Errorf("environment %q has no recorded state SHA to diverge from", a.targetEnv)
	}
	baseSHA := cur.SHA

	pusher := &recordingPusher{}
	relMgr := &recordingReleaseManager{}

	finalizer, err := hotfix.NewFinalizer(
		hotfix.FinalizerOptions{ConfigPath: ctx.ClonePath, Actor: ctx.Actor},
		hotfix.WithFinalizeDryRun(false),
		hotfix.WithTipReader(fixedTip(mergeSHA)),
		hotfix.WithTrunkStateReader(cloneTrunkReader{}),
		hotfix.WithTagLister(noTags{}),
		hotfix.WithStatePusher(pusher),
		hotfix.WithReleaseManager(relMgr),
	)
	if err != nil {
		return nil, fmt.Errorf("build finalizer: %w", err)
	}

	if err := finalizer.Finalize(a.targetEnv, mergeSHA, a.fixSHAs, baseSHA); err != nil {
		return nil, fmt.Errorf("finalize hotfix: %w", err)
	}

	return &ActionOutcome{
		Effects:        a.effects(pusher, relMgr),
		AfterStatePath: ctx.ClonePath,
	}, nil
}

// effects assembles the ordered effect sequence from the fix commits the hotfix
// carries and the calls the record-only seams captured. A finalize that recorded
// no commit/push was an idempotent no-op and yields a single skip effect.
func (a *HotfixAction) effects(pusher *recordingPusher, relMgr *recordingReleaseManager) []Effect {
	if len(pusher.messages) == 0 {
		return []Effect{{
			Disposition: DispositionSkip,
			Action:      "hotfix",
			Target:      a.targetEnv,
			Detail:      "already recorded; no change",
		}}
	}

	effects := make([]Effect, 0, len(a.fixSHAs)+1+len(relMgr.calls))
	for _, sha := range a.fixSHAs {
		effects = append(effects, Effect{
			Disposition: DispositionRun,
			Action:      "apply patch",
			Target:      a.targetEnv,
			Detail:      fmt.Sprintf("commit %s", shortOrNone(sha)),
		})
	}
	effects = append(effects, Effect{
		Disposition: DispositionRun,
		Action:      "write state",
		Target:      a.targetEnv,
		Detail:      "diverge env onto integration branch",
	})
	for _, opts := range relMgr.calls {
		effects = append(effects, Effect{
			Disposition: DispositionRun,
			Action:      "release " + string(opts.Action),
			Target:      a.targetEnv,
			Detail:      fmt.Sprintf("tag %s", orNone(opts.Tag)),
		})
	}
	return effects
}

// fixedTip is a record-only env-branch tip reader that always reports the merge
// SHA, so the Finalizer's merge-SHA-equals-tip cross-check passes without a git
// checkout.
type fixedTip string

// LocalBranchSHA returns the fixed merge SHA for any branch name.
func (t fixedTip) LocalBranchSHA(string) (string, error) { return string(t), nil }

// cloneTrunkReader is a record-only trunk-state reader that returns the clone
// manifest bytes, so the Finalizer reads prior state from the clone rather than
// from a real trunk branch.
type cloneTrunkReader struct{}

// ReadManifest returns the bytes of the manifest at path, ignoring the trunk
// branch name. The path is the clone the engine handed the action.
func (cloneTrunkReader) ReadManifest(path, _ string) ([]byte, error) {
	return os.ReadFile(path)
}

// noTags is a record-only tag lister that reports no existing tags, so hotfix
// version allocation is deterministic and never reads a git repository.
type noTags struct{}

// ListTags always returns an empty slice.
func (noTags) ListTags() ([]string, error) { return nil, nil }

// recordingPusher captures the manifest commit messages the Finalizer would push
// to trunk without performing any git operation.
type recordingPusher struct {
	messages []string
}

// CommitAndPush records the commit message and reports success without touching
// git. The Finalizer has already written the clone before calling this.
func (p *recordingPusher) CommitAndPush(_, _, message string) error {
	p.messages = append(p.messages, message)
	return nil
}

// recordingReleaseManager captures the release operations the Finalizer would
// run against GitHub without performing any network call.
type recordingReleaseManager struct {
	calls []release.Options
}

// Manage records the release options and returns an empty success result.
func (m *recordingReleaseManager) Manage(opts release.Options) (*release.Result, error) {
	m.calls = append(m.calls, opts)
	return &release.Result{}, nil
}

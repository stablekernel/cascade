package simulate

import (
	"fmt"

	"github.com/stablekernel/cascade/internal/promote"
)

// ReleaseAction replays the real promotion state-machine across the release
// boundary against a cloned manifest. The promoter is the orchestration brain
// that decides whether a crossing is a prerelease promotion or a publish, and it
// computes the rc-to-publish version carry; this action drives it and surfaces
// that decision as the release-marker effect.
//
// The network-bound release executor (internal/release Manager) is not invoked:
// the simulator validates orchestration, meaning the state transitions and the
// release decision, not the GitHub release API calls. The marker effect labels
// the step with the internal/release action vocabulary so the simulator and the
// executor name the step identically.
type ReleaseAction struct{}

// NewReleaseAction builds a ReleaseAction.
func NewReleaseAction() *ReleaseAction { return &ReleaseAction{} }

// Name returns the action identifier.
func (a *ReleaseAction) Name() string { return "release" }

// Describe returns a one-line summary of the release crossing being simulated.
func (a *ReleaseAction) Describe() string { return "release (prerelease/publish crossing)" }

// Apply runs the real promoter in default mode against the clone manifest and
// returns the effect sequence including the release marker. It reports a clear
// error when the manifest state is not at a release boundary, so the operator
// learns the crossing would not produce a release rather than seeing an empty
// result.
func (a *ReleaseAction) Apply(ctx ActionContext) (*ActionOutcome, error) {
	promoter, err := promote.NewPromoter(promote.PromoterOptions{
		ConfigPath: ctx.ClonePath,
		DryRun:     false,
		Actor:      ctx.Actor,
	})
	if err != nil {
		return nil, fmt.Errorf("build promoter: %w", err)
	}

	result, err := promoter.Promote(promote.ModeDefault, "")
	if err != nil {
		return nil, fmt.Errorf("run release promotion: %w", err)
	}
	if result != nil && !result.Success && result.Error != "" {
		return nil, fmt.Errorf("release promotion failed: %s", result.Error)
	}
	if result == nil || result.ReleaseAction == "" {
		return nil, fmt.Errorf("no release crossing: the manifest state is not at the prerelease or publish boundary")
	}

	return &ActionOutcome{
		Effects:        effectsFromResult(result, ctx.Deploys),
		AfterStatePath: ctx.ClonePath,
	}, nil
}

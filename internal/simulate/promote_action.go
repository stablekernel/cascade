package simulate

import (
	"fmt"

	"github.com/stablekernel/cascade/internal/promote"
	"github.com/stablekernel/cascade/internal/release"
)

// PromoteAction replays the real promotion orchestration against a cloned
// manifest. It drives promote.NewPromoter in non-dry-run mode so the genuine
// state-machine computes transitions and writes them to the clone only.
type PromoteAction struct {
	mode   promote.PromotionMode
	target string
}

// NewPromoteAction builds a PromoteAction for the given promotion mode and
// target. The target is only consulted for cascade mode.
func NewPromoteAction(mode promote.PromotionMode, target string) *PromoteAction {
	return &PromoteAction{mode: mode, target: target}
}

// Name returns the action identifier.
func (a *PromoteAction) Name() string { return "promote" }

// Describe returns a one-line summary of the promotion being simulated.
func (a *PromoteAction) Describe() string {
	if a.target != "" {
		return fmt.Sprintf("promote (mode=%s, target=%s)", a.mode, a.target)
	}
	return fmt.Sprintf("promote (mode=%s)", a.mode)
}

// Apply runs the real promoter against the clone manifest in non-dry-run mode
// and maps the returned PromotionResult into an ordered effect sequence. The
// promoter writes its transitions to the clone path via os.WriteFile only; no
// git or network call is made.
func (a *PromoteAction) Apply(ctx ActionContext) (*ActionOutcome, error) {
	promoter, err := promote.NewPromoter(promote.PromoterOptions{
		ConfigPath: ctx.ClonePath,
		DryRun:     false,
		Actor:      ctx.Actor,
	})
	if err != nil {
		return nil, fmt.Errorf("build promoter: %w", err)
	}

	result, err := promoter.Promote(a.mode, a.target)
	if err != nil {
		return nil, fmt.Errorf("run promotion: %w", err)
	}
	if result != nil && !result.Success && result.Error != "" {
		return nil, fmt.Errorf("promotion failed: %s", result.Error)
	}

	return &ActionOutcome{
		Effects:        effectsFromResult(result),
		AfterStatePath: ctx.ClonePath,
	}, nil
}

// effectsFromResult translates a PromotionResult into the ordered effect
// sequence. Each promotion yields a deploy effect (unless it is a state-only
// marker advance) followed by a write-state effect; skipped envs yield a single
// skip effect. The mapping stays faithful to the result and invents no steps it
// does not contain.
func effectsFromResult(result *promote.PromotionResult) []Effect {
	if result == nil {
		return nil
	}

	var effects []Effect
	for _, p := range result.Promotions {
		if p.NeedsDeploy {
			effects = append(effects, Effect{
				Disposition: DispositionRun,
				Action:      "deploy",
				Target:      p.Environment,
				Detail:      fmt.Sprintf("from %s (sha %s, version %s)", p.SourceEnv, shortOrNone(p.SHA), orNone(p.Version)),
			})
		}
		effects = append(effects, Effect{
			Disposition: DispositionRun,
			Action:      "write state",
			Target:      p.Environment,
			Detail:      fmt.Sprintf("sha %s, version %s", shortOrNone(p.SHA), orNone(p.Version)),
		})
	}

	if marker, ok := releaseMarkerEffect(result); ok {
		effects = append(effects, marker)
	}

	for _, env := range result.SkippedEnvs {
		effects = append(effects, Effect{
			Disposition: DispositionSkip,
			Action:      "promote",
			Target:      env,
			Detail:      "no change required",
		})
	}

	return effects
}

// releaseMarkerEffect translates the publish-marker step of a promotion into a
// distinct effect. The promoter sets result.ReleaseAction to "prerelease" or
// "publish" when a crossing reaches the release boundary; the review of the
// engine seam noted that step was folded into the generic write-state effect and
// could not be told apart. The action label is taken from the internal/release
// vocabulary so the simulator and the real release executor agree on the names.
// The ok return is false when the result carries no release marker.
func releaseMarkerEffect(result *promote.PromotionResult) (Effect, bool) {
	if result == nil || result.ReleaseAction == "" {
		return Effect{}, false
	}

	act, err := release.ValidateAction(result.ReleaseAction)
	if err != nil {
		// An unrecognized marker is surfaced verbatim rather than dropped, so
		// the operator still sees that a release step would run.
		act = release.Action(result.ReleaseAction)
	}

	target := "release"
	detail := "release marker advance"
	if data := result.ReleaseData; data != nil {
		if data.SemVersion != "" {
			target = data.SemVersion
		}
		detail = fmt.Sprintf("rc %s, sha %s", orNone(data.RCVersion), shortOrNone(data.SHA))
	}

	return Effect{
		Disposition: DispositionRun,
		Action:      "release " + string(act),
		Target:      target,
		Detail:      detail,
	}, true
}

// shortOrNone renders the first 7 characters of a SHA, or (none) when empty.
func shortOrNone(sha string) string {
	if sha == "" {
		return noneValue
	}
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

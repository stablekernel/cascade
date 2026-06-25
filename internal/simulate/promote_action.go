package simulate

import (
	"fmt"

	"github.com/stablekernel/cascade/internal/promote"
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

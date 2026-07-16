package simulate

import (
	"fmt"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/rollback"
)

// RollbackAction replays the real rollback orchestration against a cloned
// manifest. It drives rollback.New, Plan, and Apply so the genuine target
// resolution and state revert run, writing the reverted state to the clone only.
type RollbackAction struct {
	env        string
	to         string
	deployable string
}

// NewRollbackAction builds a RollbackAction for the given environment. The to
// argument selects a prior SHA or version; when empty the previous distinct
// state is resolved from the deploy-history ring. The deployable argument scopes
// the rollback to a single deploy when set.
func NewRollbackAction(env, to, deployable string) *RollbackAction {
	return &RollbackAction{env: env, to: to, deployable: deployable}
}

// Name returns the action identifier.
func (a *RollbackAction) Name() string { return "rollback" }

// Describe returns a one-line summary of the rollback being simulated.
func (a *RollbackAction) Describe() string {
	target := a.to
	if target == "" {
		target = "previous"
	}
	if a.deployable != "" {
		return fmt.Sprintf("rollback (env=%s, to=%s, deployable=%s)", a.env, target, a.deployable)
	}
	return fmt.Sprintf("rollback (env=%s, to=%s)", a.env, target)
}

// Apply resolves the rollback target against the clone manifest and applies it.
// History resolution is pinned to the in-state deploy-history ring by injecting
// an empty history reader, so the simulation never shells out to git. Apply
// writes the reverted state to the clone path via os.WriteFile only.
func (a *RollbackAction) Apply(ctx ActionContext) (*ActionOutcome, error) {
	rb, err := rollback.New(rollback.Options{
		ConfigPath:    ctx.ClonePath,
		Actor:         ctx.Actor,
		Component:     ctx.Component,
		HistoryReader: emptyHistory{},
	})
	if err != nil {
		return nil, fmt.Errorf("build rollbacker: %w", err)
	}

	plan, err := rb.Plan(a.env, a.to, a.deployable)
	if err != nil {
		return nil, fmt.Errorf("plan rollback: %w", err)
	}

	// Mirror the real rollback finalize, which runs the deploy-result gate
	// between Plan and Apply (gateOnDeployResults in internal/rollback): a
	// deploy that did not succeed refuses the state write. The simulated gate
	// reads the injected outcomes instead of DEPLOY_RESULT_* env vars, honors
	// the same deployable scoping, and holds back the revert, so the preview
	// never asserts a state write the real operation would refuse.
	if gated := ctx.Deploys.gateScoped(a.deployable); gated != "" {
		return &ActionOutcome{
			Effects:        gatedEffectsFromRollback(plan, gated),
			AfterStatePath: ctx.ClonePath,
		}, nil
	}

	if err := rb.Apply(plan); err != nil {
		return nil, fmt.Errorf("apply rollback: %w", err)
	}

	return &ActionOutcome{
		Effects:        effectsFromRollback(plan),
		AfterStatePath: ctx.ClonePath,
	}, nil
}

// effectsFromRollback translates a rollback plan into the ordered effect
// sequence. A no-op plan yields a single skip effect; a real rollback yields a
// revert effect followed by a write-state effect. The mapping reads only the
// plan the real package computed and invents no steps.
func effectsFromRollback(plan *rollback.Plan) []Effect {
	if plan == nil {
		return nil
	}

	target := rollbackTarget(plan)

	if plan.NoOp {
		return []Effect{{
			Disposition: DispositionSkip,
			Action:      "rollback",
			Target:      target,
			Detail:      "already at target",
		}}
	}

	return []Effect{
		{
			Disposition: DispositionRun,
			Action:      "revert",
			Target:      target,
			Detail: fmt.Sprintf("to sha %s, version %s (from %s)",
				shortOrNone(plan.Target.SHA), orNone(plan.Target.Version), plan.Target.Source),
		},
		{
			Disposition: DispositionRun,
			Action:      "write state",
			Target:      target,
			Detail:      fmt.Sprintf("sha %s, version %s", shortOrNone(plan.Target.SHA), orNone(plan.Target.Version)),
		},
	}
}

// gatedEffectsFromRollback renders the effect sequence for a rollback the
// deploy-result gate held back. Mirroring the promote preview, the resolved
// revert is still shown as run (the deploy callbacks did execute; one of them
// is what failed), but the write-state effect is gated with the blocking
// reason, because the real finalize aborts before Apply and leaves trunk state
// unchanged. A gated no-op plan collapses to a single gate effect: there is no
// revert to show and no write to hold back, but the real operation still
// refuses, so a clean skip would overstate it.
func gatedEffectsFromRollback(plan *rollback.Plan, reason string) []Effect {
	if plan == nil {
		return nil
	}

	target := rollbackTarget(plan)

	if plan.NoOp {
		return []Effect{{
			Disposition: DispositionGate,
			Action:      "rollback",
			Target:      target,
			Detail:      reason,
		}}
	}

	return []Effect{
		{
			Disposition: DispositionRun,
			Action:      "revert",
			Target:      target,
			Detail: fmt.Sprintf("to sha %s, version %s (from %s)",
				shortOrNone(plan.Target.SHA), orNone(plan.Target.Version), plan.Target.Source),
		},
		{
			Disposition: DispositionGate,
			Action:      "write state",
			Target:      target,
			Detail:      reason,
		},
	}
}

// rollbackTarget renders the effect target for a plan: the environment alone,
// or env/deployable when the rollback is scoped to a single deploy.
func rollbackTarget(plan *rollback.Plan) string {
	if plan.Deployable != "" {
		return fmt.Sprintf("%s/%s", plan.Environment, plan.Deployable)
	}
	return plan.Environment
}

// emptyHistory is a record-only rollback.HistoryReader that reports no git
// history. It pins target resolution to the in-state deploy-history ring so the
// simulation is deterministic and never reads a git repository.
type emptyHistory struct{}

// PriorStates always returns an empty slice, signaling no recoverable history.
func (emptyHistory) PriorStates(string) ([]*config.EnvState, error) {
	return nil, nil
}

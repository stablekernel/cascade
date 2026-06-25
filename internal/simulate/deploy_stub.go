package simulate

import (
	"fmt"
	"strings"
)

// DeployOutcome is the simulated result of a single build or deploy callback.
// The simulator never runs the user's real build and deploy scripts, so each
// callback resolves to one of these recorded outcomes instead of an execution.
type DeployOutcome string

const (
	// OutcomeSuccess marks a callback the simulation treats as having succeeded.
	// It is the default when no outcome is injected for a callback.
	OutcomeSuccess DeployOutcome = "success"

	// OutcomeFailure marks a callback the simulation treats as having failed.
	// A failed deploy gates the downstream finalize, matching how the real
	// finalizers refuse to record state when a deploy did not succeed.
	OutcomeFailure DeployOutcome = "failure"

	// OutcomeSkipped marks a callback the simulation treats as not run. A
	// skipped deploy is never a failure, but it does not count as a success
	// either, so a step whose only deploys were skipped still gates.
	OutcomeSkipped DeployOutcome = "skipped"
)

// validOutcome reports whether s is one of the outcomes a caller may inject.
func validOutcome(s DeployOutcome) bool {
	switch s {
	case OutcomeSuccess, OutcomeFailure, OutcomeSkipped:
		return true
	default:
		return false
	}
}

// DeployStub is the simulate-side model of the build and deploy callbacks a
// manifest declares. It exists to make one boundary explicit: the simulator
// validates cascade's ORCHESTRATION, meaning the run, skip, and gate decisions
// and the state transitions, not the user's real build and deploy scripts. Those
// scripts never execute in a what-if. Each callback is therefore recorded as a
// stubbed effect carrying a simulated outcome rather than run.
//
// Outcomes default to success, so the orchestration sequences exactly as it
// would with real callbacks that all passed. A caller can inject a failure or
// skipped outcome per callback to preview the orchestration's gating behavior.
// The gate mirrors the DEPLOY_RESULT_<name> inputs the real finalizers read from
// the environment (see internal/promote/finalize.go and
// internal/rollback/command_subcommands.go): a deploy that did not succeed
// refuses to advance trunk state, so the simulated finalize is held back.
type DeployStub struct {
	builds   []string
	deploys  []string
	outcomes map[string]DeployOutcome
}

// newDeployStub builds a DeployStub for the manifest's build and deploy names
// and the injected per-callback outcomes. A nil or absent outcome resolves to
// success. The name slices are copied so the stub does not alias caller state.
func newDeployStub(builds, deploys []string, outcomes map[string]DeployOutcome) *DeployStub {
	cp := func(in []string) []string {
		if len(in) == 0 {
			return nil
		}
		out := make([]string, len(in))
		copy(out, in)
		return out
	}
	merged := make(map[string]DeployOutcome, len(outcomes))
	for name, outcome := range outcomes {
		merged[name] = outcome
	}
	return &DeployStub{builds: cp(builds), deploys: cp(deploys), outcomes: merged}
}

// outcomeFor returns the resolved outcome for a callback, defaulting to success.
func (s *DeployStub) outcomeFor(name string) DeployOutcome {
	if s == nil {
		return OutcomeSuccess
	}
	if o, ok := s.outcomes[name]; ok {
		return o
	}
	return OutcomeSuccess
}

// hasCallbacks reports whether the manifest declared any build or deploy
// callback for the stub to record. When false, the orchestration carries no
// stubbed effects and the generic deploy marker stands on its own.
func (s *DeployStub) hasCallbacks() bool {
	return s != nil && (len(s.builds) > 0 || len(s.deploys) > 0)
}

// recordedEffects returns the ordered stubbed effects for the configured build
// and deploy callbacks: builds first, then deploys, each in manifest order. A
// successful or failed callback is recorded as run with its simulated outcome in
// the detail; a skipped callback is recorded as a skip. Nothing is executed.
func (s *DeployStub) recordedEffects() []Effect {
	if !s.hasCallbacks() {
		return nil
	}
	effects := make([]Effect, 0)
	for _, name := range s.builds {
		effects = append(effects, s.callbackEffect("build", name))
	}
	for _, name := range s.deploys {
		effects = append(effects, s.callbackEffect("deploy", name))
	}
	return effects
}

// callbackEffect renders one build or deploy callback as a stubbed effect.
func (s *DeployStub) callbackEffect(kind, name string) Effect {
	outcome := s.outcomeFor(name)
	disposition := DispositionRun
	if outcome == OutcomeSkipped {
		disposition = DispositionSkip
	}
	return Effect{
		Disposition: disposition,
		Action:      kind,
		Target:      name,
		Detail:      fmt.Sprintf("simulated %s (not executed)", outcome),
	}
}

// gate decides whether the simulated finalize may record state after the deploy
// callbacks ran, using the same rules the real finalizers apply to deploy
// results. It returns the blocking reason, or the empty string when finalize may
// proceed. The rules are:
//
//   - No deploys configured: nothing to gate on, so finalize proceeds.
//   - Any deploy outcome of failure aborts finalize, naming the deploy.
//   - If deploys are configured but none succeeded (all skipped), nothing was
//     deployed, so finalize is held back.
//   - Otherwise at least one deploy succeeded and none failed, so finalize
//     proceeds.
func (s *DeployStub) gate() (blockedReason string) {
	if s == nil || len(s.deploys) == 0 {
		return ""
	}
	anySucceeded := false
	for _, name := range s.deploys {
		switch s.outcomeFor(name) {
		case OutcomeFailure:
			return fmt.Sprintf("deploy %q simulated failure; trunk state left unchanged", name)
		case OutcomeSuccess:
			anySucceeded = true
		case OutcomeSkipped:
			// Skipped is never a failure, but it is not a success either.
		}
	}
	if !anySucceeded {
		return "no simulated deploy succeeded; trunk state left unchanged"
	}
	return ""
}

// ParseDeployResults parses repeatable "name=outcome" pairs (for example
// "services=failure") into a per-callback outcome map. Outcomes are limited to
// success, failure, and skipped. It rejects a malformed pair, a blank name, and
// an unknown outcome so a typo never silently resolves to the success default.
func ParseDeployResults(pairs []string) (map[string]DeployOutcome, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]DeployOutcome, len(pairs))
	for _, raw := range pairs {
		name, value, ok := strings.Cut(raw, "=")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !ok || name == "" || value == "" {
			return nil, fmt.Errorf("invalid deploy-result %q: want name=success|failure|skipped", raw)
		}
		outcome := DeployOutcome(strings.ToLower(value))
		if !validOutcome(outcome) {
			return nil, fmt.Errorf("invalid deploy-result %q: unknown outcome %q (want success, failure, or skipped)", raw, value)
		}
		out[name] = outcome
	}
	return out, nil
}

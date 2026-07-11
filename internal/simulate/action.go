package simulate

// ActionContext carries the inputs an Action needs to replay orchestration
// against the cloned manifest. ClonePath points at the temp copy of the user's
// manifest; the real file is never handed to an action.
type ActionContext struct {
	// ClonePath is the path to the temp clone of the manifest the action may
	// mutate. The real promoter writes its transitions here.
	ClonePath string

	// Actor is the identity that performs the hypothetical action.
	Actor string

	// Component, when non-empty, names the declared component the action is
	// scoped to. The action passes it to the real orchestration options so state
	// is read and written under state.components.<Component>.<env>, matching the
	// component-scoped commands. Empty selects the flat single-component path.
	Component string

	// Deploys is the deploy-stub model for the manifest's build and deploy
	// callbacks. The simulator validates orchestration, not the user's real
	// build and deploy scripts, so an action records each callback as a stubbed
	// effect with a simulated outcome and gates finalize on the result rather
	// than executing anything. It is never nil when supplied by the engine.
	Deploys *DeployStub
}

// ActionOutcome is what an Action returns after replaying orchestration. It
// carries the ordered effects plus the path holding the resolved after-state
// (the clone path, since the real promoter writes there).
type ActionOutcome struct {
	// Effects is the ordered list of steps the orchestration would take.
	Effects []Effect

	// AfterStatePath is the manifest path holding the after-state.
	AfterStatePath string
}

// Action is a hypothetical operation the what-if engine can replay against a
// cloned manifest. Implementations drive the real orchestration logic in
// record-only mode and report the effects it would produce.
type Action interface {
	// Name is a short identifier for the action (for example "promote").
	Name() string

	// Describe returns a one-line human-readable summary of the action.
	Describe() string

	// Apply replays the action against the clone manifest and returns the
	// effects plus the after-state path.
	Apply(ctx ActionContext) (*ActionOutcome, error)
}

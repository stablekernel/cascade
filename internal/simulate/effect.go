package simulate

// Disposition classifies how the orchestration treats a single effect when the
// hypothetical action is replayed in record-only mode.
type Disposition string

const (
	// DispositionRun marks an effect the orchestration would carry out.
	DispositionRun Disposition = "run"

	// DispositionSkip marks an effect the orchestration would skip as a no-op.
	DispositionSkip Disposition = "skip"

	// DispositionGate marks an effect held back behind a gate or guard.
	DispositionGate Disposition = "gate"
)

// Effect is one ordered step the orchestration would take for the simulated
// action. It carries the disposition (run, skip, or gate), the kind of action,
// the target it acts on, and a human-readable detail string.
type Effect struct {
	Disposition Disposition `json:"disposition"`
	Action      string      `json:"action"`
	Target      string      `json:"target"`
	Detail      string      `json:"detail"`
}

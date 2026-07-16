// Package pinreconcile implements the pin-reconciliation engine: adopting an
// external action-pin change back into the manifest and regenerating so every
// owned file agrees with it again.
package pinreconcile

// Input is the source-agnostic reconcile input: the governed action set and,
// per governed action, every ref observed in an authoritative SOURCE file (the
// triggering change and, in cascade's own repo, hand-written workflows plus the
// anchor). Generated files are targets and are deliberately absent here.
type Input struct {
	Governed   map[string]bool
	SourceRefs map[string][]string
}

// Adoptions is the verbatim set to write into the manifest's action_pins map,
// keyed by action path. A sha adoption carries its trailing "# <version>".
type Adoptions struct {
	Pins map[string]string
}

// Relevant reports whether any governed pin actually changed and must be adopted.
func (a Adoptions) Relevant() bool { return len(a.Pins) > 0 }

// PlanAdoptions decides relevance and computes the verbatim adoptions. It reads
// the incoming ref from source with consensus-over-source, refuses on ambiguity,
// and never touches an ungoverned action. It performs no I/O.
func PlanAdoptions(in Input) (Adoptions, error) {
	out := Adoptions{Pins: map[string]string{}}
	for action, refs := range in.SourceRefs {
		if !in.Governed[action] {
			continue
		}
		ref, err := consensusRef(action, refs)
		if err != nil {
			return Adoptions{}, err
		}
		out.Pins[action] = ref
	}
	return out, nil
}

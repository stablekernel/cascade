package config

// MaxPreviousSnapshots bounds the per-environment deploy-history ring
// (state.<env>.previous). Once the ring reaches this length the oldest snapshot
// is dropped as a newer one is prepended, so the ring records at most this many
// prior states, newest first.
const MaxPreviousSnapshots = 10

// PushPreviousSnapshot records the environment's current (outgoing) state into
// the deploy-history ring before the env pointer is advanced to newSHA. Callers
// invoke it just before overwriting SHA/Version/CommittedAt/CommittedBy, so the
// snapshot captures the state that is about to be replaced.
//
// It is a no-op when there is no prior state to record (s.SHA == "") or when the
// environment is not actually transitioning (s.SHA == newSHA). Otherwise it
// prepends a snapshot of {SHA, Version, CommittedAt, CommittedBy} so the ring is
// ordered newest first, then caps the ring at MaxPreviousSnapshots by dropping
// the oldest entries. It is safe to call when s.Previous is nil.
func (s *EnvState) PushPreviousSnapshot(newSHA string) {
	if s.SHA == "" || s.SHA == newSHA {
		return
	}

	snapshot := EnvStateSnapshot{
		SHA:         s.SHA,
		Version:     s.Version,
		CommittedAt: s.CommittedAt,
		CommittedBy: s.CommittedBy,
	}

	s.Previous = append([]EnvStateSnapshot{snapshot}, s.Previous...)
	if len(s.Previous) > MaxPreviousSnapshots {
		s.Previous = s.Previous[:MaxPreviousSnapshots]
	}
}

package config

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPushPreviousSnapshot_PrependsPriorOnTransition(t *testing.T) {
	s := &EnvState{
		SHA:         "old123",
		Version:     "v1.0.0",
		CommittedAt: "2026-01-01T10:00:00Z",
		CommittedBy: "alice",
	}

	s.PushPreviousSnapshot("new456")

	require.Len(t, s.Previous, 1)
	assert.Equal(t, EnvStateSnapshot{
		SHA:         "old123",
		Version:     "v1.0.0",
		CommittedAt: "2026-01-01T10:00:00Z",
		CommittedBy: "alice",
	}, s.Previous[0])

	// A second transition prepends the next snapshot, newest first.
	s.SHA = "new456"
	s.Version = "v1.1.0"
	s.CommittedAt = "2026-01-02T10:00:00Z"
	s.CommittedBy = "bob"
	s.PushPreviousSnapshot("third789")

	require.Len(t, s.Previous, 2)
	assert.Equal(t, "new456", s.Previous[0].SHA)
	assert.Equal(t, "old123", s.Previous[1].SHA)
}

func TestPushPreviousSnapshot_BoundedToMax(t *testing.T) {
	s := &EnvState{}

	// Drive more transitions than the cap and confirm the ring stays bounded,
	// newest first, dropping the oldest entries.
	total := MaxPreviousSnapshots + 5
	for i := 0; i < total; i++ {
		s.SHA = "sha" + strconv.Itoa(i)
		s.Version = "v0.0." + strconv.Itoa(i)
		s.PushPreviousSnapshot("sha" + strconv.Itoa(i+1))
	}

	require.Len(t, s.Previous, MaxPreviousSnapshots)
	// Newest first: the most recent outgoing SHA is "sha{total-1}".
	assert.Equal(t, "sha"+strconv.Itoa(total-1), s.Previous[0].SHA)
	assert.Equal(t, "sha"+strconv.Itoa(total-MaxPreviousSnapshots), s.Previous[MaxPreviousSnapshots-1].SHA)
}

func TestPushPreviousSnapshot_SkipsNoOpSameSHA(t *testing.T) {
	s := &EnvState{SHA: "same111", Version: "v1.0.0"}

	s.PushPreviousSnapshot("same111")

	assert.Empty(t, s.Previous)
}

func TestPushPreviousSnapshot_SkipsWhenNoPriorSHA(t *testing.T) {
	s := &EnvState{} // no prior SHA

	s.PushPreviousSnapshot("new456")

	assert.Empty(t, s.Previous)
}

func TestPreviousRing_YAMLRoundTrip(t *testing.T) {
	original := &EnvState{
		SHA:         "head000",
		Version:     "v2.0.0",
		CommittedAt: "2026-02-01T10:00:00Z",
		CommittedBy: "carol",
		Previous: []EnvStateSnapshot{
			{SHA: "prev111", Version: "v1.1.0", CommittedAt: "2026-01-15T10:00:00Z", CommittedBy: "bob"},
			{SHA: "prev000", Version: "v1.0.0", CommittedAt: "2026-01-01T10:00:00Z", CommittedBy: "alice"},
		},
	}

	data, err := yaml.Marshal(original)
	require.NoError(t, err)

	var got EnvState
	require.NoError(t, yaml.Unmarshal(data, &got))

	assert.Equal(t, original.Previous, got.Previous)

	// omitempty: an env with no ring does not emit the key.
	empty := &EnvState{SHA: "x"}
	emptyData, err := yaml.Marshal(empty)
	require.NoError(t, err)
	assert.NotContains(t, string(emptyData), "previous:")
}

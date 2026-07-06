package pinreconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanAdoptions_TagAndSha(t *testing.T) {
	// Governed set the engine may touch.
	governed := map[string]bool{"actions/checkout": true, "actions/upload-artifact": true}

	// A tag-mode bump on an owned file: checkout moved v5 -> v6.
	adopts, err := PlanAdoptions(Input{
		Governed: governed,
		SourceRefs: map[string][]string{
			"actions/checkout": {"v6"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"actions/checkout": "v6"}, adopts.Pins)

	// An ungoverned uses: is never adopted.
	adopts, err = PlanAdoptions(Input{
		Governed:   governed,
		SourceRefs: map[string][]string{"some/other-action": {"v1"}},
	})
	require.NoError(t, err)
	require.Empty(t, adopts.Pins)
}

func TestPlanAdoptions_ShaKeepsComment(t *testing.T) {
	adopts, err := PlanAdoptions(Input{
		Governed:   map[string]bool{"actions/checkout": true},
		SourceRefs: map[string][]string{"actions/checkout": {"deadbeef... # v6.0.1"}},
	})
	require.NoError(t, err)
	require.Equal(t, "deadbeef... # v6.0.1", adopts.Pins["actions/checkout"])
}

func TestPlanAdoptions_RefusesAmbiguous(t *testing.T) {
	_, err := PlanAdoptions(Input{
		Governed:   map[string]bool{"actions/checkout": true},
		SourceRefs: map[string][]string{"actions/checkout": {"v5", "v6"}},
	})
	require.ErrorIs(t, err, ErrAmbiguousSource)
}

package pinreconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConsensusRef_AgreesAcrossSources(t *testing.T) {
	ref, err := consensusRef("actions/checkout", []string{"v5 # v5.0.0", "v5 # v5.0.0"})
	require.NoError(t, err)
	require.Equal(t, "v5 # v5.0.0", ref)
}

func TestConsensusRef_RefusesDisagreement(t *testing.T) {
	_, err := consensusRef("actions/checkout", []string{"v5 # v5.0.0", "v4 # v4.0.0"})
	require.ErrorIs(t, err, ErrAmbiguousSource)
}

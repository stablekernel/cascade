package pinreconcile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheck_ReportsRelevanceAndRefs(t *testing.T) {
	res, err := Check(Input{
		Governed:   map[string]bool{"actions/checkout": true},
		SourceRefs: map[string][]string{"actions/checkout": {"v6"}},
	})
	require.NoError(t, err)
	require.True(t, res.Relevant)
	require.Equal(t, map[string]string{"actions/checkout": "v6"}, res.ChangedRefs)
}

func TestCheck_NoGovernedChangeIsIrrelevant(t *testing.T) {
	res, err := Check(Input{
		Governed:   map[string]bool{"actions/checkout": true},
		SourceRefs: map[string][]string{"some/other": {"v1"}},
	})
	require.NoError(t, err)
	require.False(t, res.Relevant)
}

func TestWriteCheckArtifact_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pin-reconcile-result.json")
	require.NoError(t, WriteCheckArtifact(path, CheckResult{Relevant: true, ChangedRefs: map[string]string{"actions/checkout": "v6"}}))
	var got CheckResult
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &got))
	require.True(t, got.Relevant)
}

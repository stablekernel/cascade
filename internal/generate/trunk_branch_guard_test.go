package generate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerate_MissingTrunkBranch_IsLoudNotEmptyAllowList pins the generator
// against the failure this guard exists to prevent: an unset trunk_branch used
// to render "branches: []", an allow-list matching no branch at all. The
// workflow was emitted, committed, and reported green while orchestrate could
// never fire on a trunk push. Generation must fail loudly instead.
func TestGenerate_MissingTrunkBranch_IsLoudNotEmptyAllowList(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.TrunkConfig{
		SchemaVersion: 1,
		Environments:  config.EnvNames("staging"),
	}

	_, err := NewGenerator(cfg, dir).Generate()

	require.Error(t, err, "generation must not succeed with an unset trunk_branch")
	assert.Contains(t, err.Error(), "trunk_branch",
		"the error must name the field the operator has to set")
}

// TestGenerate_EmptyPushBranchList_NeverEmitted is the class guard. Any code
// path that renders the orchestrate push trigger with an empty branch list
// produces a workflow that silently never runs, so the emitted text must never
// contain the empty allow-list regardless of which field was left unset.
func TestGenerate_EmptyPushBranchList_NeverEmitted(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.TrunkConfig{
		SchemaVersion: 1,
		TrunkBranch:   "main",
		Environments:  config.EnvNames("staging"),
	}

	result, err := NewGenerator(cfg, dir).Generate()
	require.NoError(t, err)

	assert.NotContains(t, result, "branches: []",
		"an empty push branch allow-list matches no branch and makes the workflow dead")
	assert.Contains(t, result, "branches: [main]")
}

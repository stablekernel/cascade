package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidate_TrunkBranchMissing_IsRejected pins lint to the published schema,
// which has always listed trunk_branch as required. Lint accepted its absence
// and generation then emitted an empty push allow-list, so a manifest could be
// valid, generate cleanly, and never run.
func TestValidate_TrunkBranchMissing_IsRejected(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: 1,
		Environments:  EnvNames("staging"),
	}

	errs := Validate(cfg)

	require.NotEmpty(t, errs, "a manifest without trunk_branch must not validate")
	assert.True(t, hasErrContaining(errs, "trunk_branch"),
		"validation must name trunk_branch, got: %v", errs)
}

// TestValidate_TrunkBranchPresent_IsAccepted keeps the required check from
// firing on the manifests that already work.
func TestValidate_TrunkBranchPresent_IsAccepted(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: 1,
		TrunkBranch:   "main",
		Environments:  EnvNames("staging"),
	}

	assert.False(t, hasErrContaining(Validate(cfg), "trunk_branch"),
		"a manifest that sets trunk_branch must not trip the required check")
}

// TestValidate_TrunkBranchInherited_ByComponents guards the component path:
// components inherit trunk_branch from the shared defaults, so the required
// check must not fire once per component for a value set at the top level.
func TestValidate_TrunkBranchInherited_ByComponents(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: 1,
		TrunkBranch:   "main",
		Environments:  EnvNames("staging"),
		Components: map[string]ComponentConfig{
			"api": {Path: "api/", TagGrammar: &TagGrammarConfig{Prefix: strptr("api-v")}},
		},
	}

	assert.False(t, hasErrContaining(Validate(cfg), "trunk_branch"),
		"an inherited trunk_branch must satisfy the check for every component")
}

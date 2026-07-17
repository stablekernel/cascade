package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidate_PublishWorkflowMissing_IsRejected covers a sibling of the
// trunk_branch gap. The published schema requires publish.workflow, but lint
// accepted its absence and the promote generator skips the publish step
// entirely when it is empty. A manifest could declare publish, validate, and
// silently never publish anything.
func TestValidate_PublishWorkflowMissing_IsRejected(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: 1,
		TrunkBranch:   "main",
		Environments:  EnvNames("prod"),
		Publish:       &PublishConfig{},
	}

	errs := Validate(cfg)

	require.NotEmpty(t, errs, "a publish block without a workflow must not validate")
	assert.True(t, hasErrContaining(errs, "publish.workflow"),
		"validation must name publish.workflow, got: %v", errs)
}

// TestValidate_PublishWorkflowPresent_IsAccepted keeps the check off manifests
// that already work.
func TestValidate_PublishWorkflowPresent_IsAccepted(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: 1,
		TrunkBranch:   "main",
		Environments:  EnvNames("prod"),
		Publish:       &PublishConfig{Workflow: ".github/workflows/publish.yaml"},
	}

	assert.False(t, hasErrContaining(Validate(cfg), "publish.workflow"),
		"a publish block that sets workflow must not trip the required check")
}

// TestValidate_ExternalWithoutDeploys_IsRejected covers the other sibling: the
// schema requires external[].deploys, but lint accepted an external entry with
// none, which generates no jobs and coordinates nothing.
func TestValidate_ExternalWithoutDeploys_IsRejected(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: 1,
		TrunkBranch:   "main",
		Environments:  EnvNames("prod"),
		External:      []ExternalRepoConfig{{Repo: "acme/other"}},
	}

	errs := Validate(cfg)

	require.NotEmpty(t, errs, "an external repo with no deploys must not validate")
	assert.True(t, hasErrContaining(errs, "external[0].deploys"),
		"validation must name external[0].deploys, got: %v", errs)
}

package generate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// groupLineOf extracts the `group:` value from the first top-level concurrency:
// block in a generated workflow.
func groupLineOf(t *testing.T, content string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) != "concurrency:" {
			continue
		}
		for _, sub := range lines[i+1:] {
			trimmed := strings.TrimSpace(sub)
			if strings.HasPrefix(trimmed, "group:") {
				return strings.TrimSpace(strings.TrimPrefix(trimmed, "group:"))
			}
			if trimmed == "" || !strings.HasPrefix(sub, "  ") {
				break
			}
		}
	}
	t.Fatalf("no concurrency group found in:\n%s", content)
	return ""
}

// cancelLineOf extracts the `cancel-in-progress:` value from the first
// top-level concurrency: block in a generated workflow.
func cancelLineOf(t *testing.T, content string) string {
	t.Helper()
	for _, ln := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "cancel-in-progress:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "cancel-in-progress:"))
		}
	}
	t.Fatalf("no cancel-in-progress found in:\n%s", content)
	return ""
}

// userGroupConfig is a single-component manifest that opts into an explicit
// concurrency.group, mirroring the documented example in
// docs/src/content/docs/reference/manifest.md.
func userGroupConfig() *config.TrunkConfig {
	cfg := baseConcurrencyConfig()
	cfg.Concurrency = &config.ConcurrencyConfig{Group: "cascade-${{ github.ref }}"}
	return cfg
}

// baseConcurrencyConfig is a single-component manifest with an external repo
// declared, so the external-update generator (which refuses to emit for a
// non-primary repo) renders alongside the others.
func baseConcurrencyConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev", "prod"),
		External: []config.ExternalRepoConfig{
			{
				Repo: "example/cdk-infra",
				Ref:  "main",
				Deploys: []config.ExternalDeployConfig{
					{Name: "cdk", Workflow: "example/cdk-infra/.github/workflows/deploy.yaml"},
				},
			},
		},
	}
}

// generatedWorkflowGroups renders every generator that honors the manifest-global
// concurrency.group and returns workflow-name -> emitted group expression.
func generatedWorkflowGroups(t *testing.T, cfg *config.TrunkConfig) map[string]string {
	t.Helper()

	orch, err := NewGenerator(cfg, "").Generate()
	require.NoError(t, err)
	rel, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)
	prom, err := NewPromoteGenerator(cfg, "").Generate()
	require.NoError(t, err)
	rb, err := NewRollbackGenerator(cfg, "").Generate()
	require.NoError(t, err)
	ext, err := NewExternalUpdateGenerator(cfg, "").Generate()
	require.NoError(t, err)

	return map[string]string{
		"orchestrate":     groupLineOf(t, orch),
		"release":         groupLineOf(t, rel),
		"promote":         groupLineOf(t, prom),
		"rollback":        groupLineOf(t, rb),
		"external-update": groupLineOf(t, ext),
	}
}

// TestConcurrency_UserSuppliedGroup_IsNamespacedPerWorkflow is the core
// regression. A GitHub concurrency group is REPO-GLOBAL across workflows, and a
// shared group still cancels all-but-the-latest PENDING run even when
// cancel-in-progress is false. So when an operator sets one
// manifest-global concurrency.group, every cascade workflow that honors it must
// still land in its OWN group; otherwise a queued promote and a queued release
// silently cancel each other.
func TestConcurrency_UserSuppliedGroup_IsNamespacedPerWorkflow(t *testing.T) {
	groups := generatedWorkflowGroups(t, userGroupConfig())

	seen := make(map[string]string, len(groups))
	for workflow, group := range groups {
		if other, dup := seen[group]; dup {
			t.Errorf("workflows %q and %q share concurrency group %q; a shared group cancels all-but-latest pending run across workflows", workflow, other, group)
		}
		seen[group] = workflow
	}

	// The operator's expression must survive: namespacing composes onto it
	// rather than discarding it.
	for workflow, group := range groups {
		assert.Contains(t, group, "cascade-${{ github.ref }}",
			"workflow %q must preserve the operator-supplied group expression", workflow)
	}
}

// TestConcurrency_UserSuppliedGroup_IsNotRejected guards the documented example
// in docs/src/content/docs/reference/manifest.md. Namespacing must not turn a
// valid operator override into a validation error.
func TestConcurrency_UserSuppliedGroup_IsNotRejected(t *testing.T) {
	cfg := userGroupConfig()
	require.Empty(t, config.Validate(cfg), "documented concurrency.group example must stay valid")

	for _, group := range generatedWorkflowGroups(t, cfg) {
		require.NotEmpty(t, group)
	}
}

// TestConcurrency_StateMutatingWorkflows_PinCancelInProgressFalse asserts that
// the workflows which mutate durable state (release tags + GitHub Releases,
// promote/rollback manifest env-state pushes, external-update downstream
// manifest commits) never cancel a mid-flight write, even when the manifest
// asks them to.
func TestConcurrency_StateMutatingWorkflows_PinCancelInProgressFalse(t *testing.T) {
	cfg := userGroupConfig()
	cfg.Concurrency.CancelInProgress = boolPtr(true)

	rel, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)
	prom, err := NewPromoteGenerator(cfg, "").Generate()
	require.NoError(t, err)
	rb, err := NewRollbackGenerator(cfg, "").Generate()
	require.NoError(t, err)
	ext, err := NewExternalUpdateGenerator(cfg, "").Generate()
	require.NoError(t, err)

	for name, content := range map[string]string{
		"release":         rel,
		"promote":         prom,
		"rollback":        rb,
		"external-update": ext,
	} {
		assert.Equal(t, "false", cancelLineOf(t, content),
			"%s mutates durable state and must never be cancelled mid-write", name)
	}
}

// TestConcurrency_GroupOnly_DoesNotFlipOrchestrateCancelDefault pins the bug
// that motivates making CancelInProgress a *bool. With a plain bool, a manifest
// that sets ONLY concurrency.group makes Concurrency non-nil with a zero-value
// CancelInProgress, silently flipping orchestrate's documented default of true
// to false. Unset must stay unset.
func TestConcurrency_GroupOnly_DoesNotFlipOrchestrateCancelDefault(t *testing.T) {
	cfg := userGroupConfig() // group set, cancel_in_progress omitted
	require.Nil(t, cfg.Concurrency.CancelInProgress, "omitted cancel_in_progress must be distinguishable from explicit false")

	orch, err := NewGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assert.Equal(t, "true", cancelLineOf(t, orch),
		"orchestrate default cancel-in-progress is true; setting only concurrency.group must not silently flip it")
}

// TestConcurrency_ExplicitCancelFalse_IsHonoredOnOrchestrate asserts the
// *bool migration keeps an explicit false working on orchestrate, which is the
// one workflow whose default is true.
func TestConcurrency_ExplicitCancelFalse_IsHonoredOnOrchestrate(t *testing.T) {
	cfg := userGroupConfig()
	cfg.Concurrency.CancelInProgress = boolPtr(false)

	orch, err := NewGenerator(cfg, "").Generate()
	require.NoError(t, err)

	assert.Equal(t, "false", cancelLineOf(t, orch), "explicit cancel_in_progress: false must be honored")
}

// TestConcurrency_NoManifestBlock_UsesPerWorkflowDefaults asserts the
// no-opt-in path is untouched: a manifest with no concurrency block keeps every
// generator's historical default. This is the byte-identical guarantee that the
// golden baseline also enforces.
func TestConcurrency_NoManifestBlock_UsesPerWorkflowDefaults(t *testing.T) {
	groups := generatedWorkflowGroups(t, baseConcurrencyConfig())
	assert.Equal(t, "orchestrate-${{ github.ref }}", groups["orchestrate"])
	assert.Equal(t, `"${{ github.workflow }}"`, groups["release"])
	assert.Equal(t, `"${{ github.workflow }}"`, groups["promote"])
	assert.Equal(t, `"${{ github.workflow }}"`, groups["rollback"])
	assert.Equal(t, "cascade-external-${{ inputs.deploy_name }}-${{ github.ref }}", groups["external-update"])
}

// TestConcurrency_ExternalUpdate_OverrideKeepsPerComponentAxis asserts that an
// operator override does not collapse distinct upstream components onto one
// lane. external-update's default group carries inputs.deploy_name precisely so
// two components notifying the same downstream repo both run; forwarding an
// override verbatim would discard that axis and re-introduce the regression the
// per-component key was added to fix.
func TestConcurrency_ExternalUpdate_OverrideKeepsPerComponentAxis(t *testing.T) {
	ext, err := NewExternalUpdateGenerator(userGroupConfig(), "").Generate()
	require.NoError(t, err)

	assert.Contains(t, groupLineOf(t, ext), "inputs.deploy_name",
		"external-update override must retain the per-component axis")
}

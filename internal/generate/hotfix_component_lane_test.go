package generate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHotfixGenerator_Component_ApplyLaneUsesComponentEnvBranch proves the
// per-component apply lane creates and operates on env/<component>/<env>: the
// branch is materialized, the resolution PRs base against it, and the
// branch-protection probe URL-encodes the nested prefix. The flat env/${env}
// form must not appear as a base ref in a component workflow.
func TestHotfixGenerator_Component_ApplyLaneUsesComponentEnvBranch(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "", WithHotfixComponentName("web"))
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, `refs/heads/env/web/${env}`,
		"apply lane must materialize the component-scoped env branch")
	assert.Contains(t, content, `--base "env/web/${env}"`,
		"resolution PRs must base against the component-scoped env branch")
	assert.Contains(t, content, `branches/env%2Fweb%2F${env}/protection`,
		"branch-protection probe must URL-encode the nested component prefix")
	assert.NotContains(t, content, `--base "env/${env}"`,
		"a component workflow must not base a PR on the flat env/${env} branch")
}

// TestHotfixGenerator_Component_ContextStripsComponentPrefix proves the merged
// context job recovers TARGET_ENV by stripping the component-aware prefix
// env/<component>/ from the PR base ref, so finalize resolves the same env the
// apply lane targeted.
func TestHotfixGenerator_Component_ContextStripsComponentPrefix(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "", WithHotfixComponentName("web"))
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, `TARGET_ENV="${BASE_REF#env/web/}"`,
		"context job must strip the component-aware prefix to derive TARGET_ENV")
	assert.NotContains(t, content, `TARGET_ENV="${BASE_REF#env/}"`,
		"a component workflow must not strip only the flat env/ prefix")
}

// TestHotfixGenerator_Component_LaneAgreesPlanApplyFinalize is the composition
// gate: it proves the plan, apply, and finalize references in one generated
// per-component workflow all agree on env/<component>/<env>. The plan and
// finalize CLI steps carry --component <name>; the apply lane bases PRs on
// env/<component>/${env}; and the context job strips env/<component>/ to recover
// the target env. If any leg disagreed the merged-hotfix chain would break, so
// asserting all four in one workflow is the end-to-end agreement proof.
func TestHotfixGenerator_Component_LaneAgreesPlanApplyFinalize(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "", WithHotfixComponentName("web"))
	content, err := gen.Generate()
	require.NoError(t, err)

	// plan + finalize CLI steps are component-scoped.
	assert.Equal(t, 2, strings.Count(content, "--component web \\"),
		"both the plan and finalize CLI steps must thread --component web")
	// apply lane bases on the component-scoped env branch.
	assert.Contains(t, content, `--base "env/web/${env}"`)
	// context/finalize recovers the same env by stripping the same prefix.
	assert.Contains(t, content, `TARGET_ENV="${BASE_REF#env/web/}"`)
}

// TestHotfixGenerator_SingleComponent_ApplyLaneFlatByteIdentical pins the
// no-component apply lane and context job to the historical flat forms, so the
// single-component output stays byte-identical to the pre-component behavior.
func TestHotfixGenerator_SingleComponent_ApplyLaneFlatByteIdentical(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, `--base "env/${env}"`)
	assert.Contains(t, content, `refs/heads/env/${env}`)
	assert.Contains(t, content, `branches/env%2F${env}/protection`)
	assert.Contains(t, content, `TARGET_ENV="${BASE_REF#env/}"`)
	// No component-scoped branch name leaks into the single-component workflow.
	assert.NotContains(t, content, "env/web/")
	assert.NotContains(t, content, "--component")
}

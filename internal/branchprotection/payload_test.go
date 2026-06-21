package branchprotection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

// fullConfig returns a manifest with validate, two builds, and a deploy so the
// reusable-caller handling has something to enumerate.
func fullConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		Validate: &config.ValidateConfig{
			Workflow: ".github/workflows/validate.yaml",
		},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml"},
			{Name: "services", Workflow: ".github/workflows/build-services.yaml"},
		},
		Deploys: []config.DeployConfig{
			{Name: "services", Workflow: ".github/workflows/deploy.yaml"},
		},
	}
}

// TestBuild_Contexts_MatchGeneratorSafeChecks asserts the required contexts equal
// the generator's real safe check names, referenced from the same constants the
// generator emits so the test cannot drift from the workflow.
func TestBuild_Contexts_MatchGeneratorSafeChecks(t *testing.T) {
	p := Build(fullConfig(), "main")

	want := []string{generate.FinalizeJobName, generate.SetupJobName} // sorted
	assert.Equal(t, want, p.Protection.RequiredStatusChecks.Contexts)
}

// TestBuild_Safety_NoBareDisplayNameRequired is the safety invariant: a manifest
// with validate+build+deploy must never put a bare reusable-caller DisplayName
// into required contexts (it would never match and would block every PR). Only
// Setup and Finalize may appear, and the DisplayNames are surfaced under
// operator_todo as "<DisplayName> / <inner-job>" placeholders instead.
func TestBuild_Safety_NoBareDisplayNameRequired(t *testing.T) {
	cfg := fullConfig()
	p := Build(cfg, "main")

	contexts := p.Protection.RequiredStatusChecks.Contexts
	assert.ElementsMatch(t, []string{generate.SetupJobName, generate.FinalizeJobName}, contexts)

	// Every reusable-caller DisplayName must be absent from required contexts.
	graph := generate.BuildDependencyGraph(cfg)
	require.NotEmpty(t, graph.Order)
	for _, jobID := range graph.Order {
		display := graph.Nodes[jobID].DisplayName
		assert.NotContains(t, contexts, display,
			"bare DisplayName %q must never be a required context", display)
		// And it must appear as a completion placeholder in operator_todo.
		assert.Contains(t, p.OperatorTodo.CompleteTheseContexts, display+" / "+innerJobPlaceholder)
	}

	// Spot-check the concrete DisplayNames the generator produces.
	for _, bare := range []string{"Validate (validate)", "Build (app)", "Build (services)", "Deploy (services)"} {
		assert.NotContains(t, contexts, bare)
	}
}

// TestBuild_NoCallbacks_EmptyTodoStable confirms complete_these_contexts is an
// emitted (non-nil) empty array when there are no validate/build/deploy nodes.
func TestBuild_NoCallbacks_EmptyTodoStable(t *testing.T) {
	p := Build(&config.TrunkConfig{}, "main")

	assert.NotNil(t, p.OperatorTodo.CompleteTheseContexts)
	assert.Empty(t, p.OperatorTodo.CompleteTheseContexts)

	out, err := Marshal(p)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"complete_these_contexts": []`)
}

// TestMarshal_Deterministic asserts the same manifest yields byte-identical output.
func TestMarshal_Deterministic(t *testing.T) {
	cfg := fullConfig()
	a, err := Marshal(Build(cfg, "main"))
	require.NoError(t, err)
	b, err := Marshal(Build(cfg, "main"))
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.True(t, strings.HasSuffix(string(a), "}\n"), "exactly one trailing newline")
}

// TestMarshal_RoundTrips confirms the emitted bytes are valid JSON with the
// expected wrapper shape.
func TestMarshal_RoundTrips(t *testing.T) {
	out, err := Marshal(Build(fullConfig(), "main"))
	require.NoError(t, err)

	var generic map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &generic))
	assert.Contains(t, generic, "protection")
	assert.Contains(t, generic, "operator_todo")

	// .protection must round-trip back into the typed struct.
	var prot Protection
	require.NoError(t, json.Unmarshal(generic["protection"], &prot))
	assert.ElementsMatch(t, []string{generate.SetupJobName, generate.FinalizeJobName},
		prot.RequiredStatusChecks.Contexts)
}

// TestBuild_DefaultsWellFormed asserts the protection defaults match the decided
// contract and the hotfix advisory (enforce_admins true, one approval, strict).
func TestBuild_DefaultsWellFormed(t *testing.T) {
	p := Build(fullConfig(), "main")
	prot := p.Protection

	assert.True(t, prot.EnforceAdmins)
	assert.True(t, prot.RequiredStatusChecks.Strict)
	assert.Equal(t, 1, prot.RequiredPullRequestReviews.RequiredApprovingReviewCount)
	assert.True(t, prot.RequiredPullRequestReviews.DismissStaleReviews)
	assert.False(t, prot.RequiredPullRequestReviews.RequireCodeOwnerReviews)
	assert.True(t, prot.RequiredLinearHistory)
	assert.False(t, prot.AllowForcePushes)
	assert.False(t, prot.AllowDeletions)
	assert.True(t, prot.RequiredConversationResolution)
	assert.Nil(t, prot.Restrictions)
}

// TestMarshal_RestrictionsNullPresent confirms restrictions is present as null in
// the emitted JSON (GitHub requires the key; null means no push restriction).
func TestMarshal_RestrictionsNullPresent(t *testing.T) {
	out, err := Marshal(Build(fullConfig(), "main"))
	require.NoError(t, err)
	assert.Contains(t, string(out), `"restrictions": null`)
}

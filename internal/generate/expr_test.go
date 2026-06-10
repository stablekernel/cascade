package generate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyInputValue(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  inputKind
	}{
		{"plain literal", "us-east-1", inputLiteral},
		{"empty", "", inputLiteral},
		{"vars passthrough", "${{ vars.DEPLOY_BUCKET }}", inputPassthrough},
		{"secrets passthrough", "${{ secrets.API_KEY }}", inputPassthrough},
		{"env passthrough", "${{ env.FOO }}", inputPassthrough},
		{"dispatch inputs passthrough", "${{ inputs.target_region }}", inputPassthrough},
		{"matrix env is literal/matrix", "${{ matrix.environment }}", inputLiteral},
		{"matrix sha is literal/matrix", "${{ matrix.sha }}", inputLiteral},
		{"state ref", "${{ state.prod.sha }}", inputStateRef},
		{"state version ref", "${{ state.prod.version }}", inputStateRef},
		{"state nested build tag", "${{ state.prod.builds.image.tags.image_tag }}", inputStateRef},
		{"mixed template not passthrough", "prefix-${{ vars.X }}", inputLiteral},
		{"two expressions not passthrough", "${{ vars.A }}${{ vars.B }}", inputLiteral},
		{"whitespace tolerant", "  ${{ vars.X }}  ", inputPassthrough},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyInputValue(tc.value))
		})
	}
}

func TestResolveStateExpression(t *testing.T) {
	state := map[string]*config.EnvState{
		"prod": {
			SHA:     "deadbeef",
			Version: "v2.3.4",
			Builds: map[string]*config.BuildState{
				"image": {
					SHA:        "buildsha",
					ArtifactID: "sha256:abc",
					Tags:       map[string]string{"image_tag": "v2.3.4-rc.1"},
				},
			},
			Deploys: map[string]*config.DeployState{
				"app": {
					SHA:     "depsha",
					Version: "v2.3.3",
					Tags:    map[string]string{"chart": "0.9.0"},
				},
			},
		},
	}

	cases := []struct {
		body string
		want string
		ok   bool
	}{
		{"state.prod.sha", "deadbeef", true},
		{"state.prod.version", "v2.3.4", true},
		{"state.prod.builds.image.sha", "buildsha", true},
		{"state.prod.builds.image.artifact_id", "sha256:abc", true},
		{"state.prod.builds.image.tags.image_tag", "v2.3.4-rc.1", true},
		{"state.prod.deploys.app.sha", "depsha", true},
		{"state.prod.deploys.app.version", "v2.3.3", true},
		{"state.prod.deploys.app.tags.chart", "0.9.0", true},
		{"state.missing.sha", "", false},
		{"state.prod.builds.nope.sha", "", false},
		{"state.prod.deploys.app.tags.nope", "", false},
		{"state.prod.unknown", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			got, ok := resolveStateExpression(state, tc.body)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveInputValue(t *testing.T) {
	state := map[string]*config.EnvState{
		"prod": {SHA: "abc123"},
	}

	// Passthrough survives verbatim.
	v, err := resolveInputValue("${{ vars.BUCKET }}", state)
	require.NoError(t, err)
	assert.Equal(t, "${{ vars.BUCKET }}", v)

	// Literal unchanged.
	v, err = resolveInputValue("us-east-1", state)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", v)

	// State ref resolves.
	v, err = resolveInputValue("${{ state.prod.sha }}", state)
	require.NoError(t, err)
	assert.Equal(t, "abc123", v)

	// Unresolved state ref errors.
	_, err = resolveInputValue("${{ state.dev.sha }}", state)
	assert.Error(t, err)
}

func TestLooksLikeUnwrappedExpression(t *testing.T) {
	assert.True(t, looksLikeUnwrappedExpression("vars.BUCKET"))
	assert.True(t, looksLikeUnwrappedExpression("state.prod.sha"))
	assert.True(t, looksLikeUnwrappedExpression("secrets.TOKEN"))
	assert.False(t, looksLikeUnwrappedExpression("${{ vars.BUCKET }}"))
	assert.False(t, looksLikeUnwrappedExpression("us-east-1"))
	assert.False(t, looksLikeUnwrappedExpression("my-bucket-name"))
}

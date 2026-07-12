package statewrite

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommitWithRetry_TwoComponentsSameEnvMerge is the adversarial concurrency
// case for the scoped serializer: two finalize jobs advance the SAME env
// (staging) for DIFFERENT components (api and web). Component api commits first;
// component web's first PUT loses the optimistic-lock race (409), re-fetches
// api's committed bytes, and re-applies its own scoped write on top. Because each
// writer addresses a disjoint state.components.<name>.staging leaf, the final
// manifest must carry BOTH leaves with their exact fields and neither may drop
// the other. This is the sibling-survival property the node-disjoint scoped
// write buys for free under CommitWithRetry.
func TestCommitWithRetry_TwoComponentsSameEnvMerge(t *testing.T) {
	// The branch already carries component api's staging leaf, as if api's
	// finalize won the race and committed first.
	const startManifest = `ci:
  config:
    trunk_branch: main
    environments:
      - staging
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-v
      web:
        path: services/web
        tag_grammar:
          prefix: web-v
  state:
    components:
      api:
        staging:
          sha: apisha
          version: api-v1.0.0
`
	fake := &fakeContents{
		content: startManifest,
		sha:     "sha-0",
		putErrs: []error{rawConflict()}, // web's first PUT 409s, second succeeds
	}

	// web's finalize: a re-appliable scoped write for (web, staging). It re-parses
	// nothing of api's leaf; it only patches its own node, so re-applying it on top
	// of api's committed bytes preserves api verbatim.
	webMutate := func(current []byte) ([]byte, error) {
		return config.WriteScopedState(current, "ci",
			config.StateWrite{Component: "web", Env: "staging", State: &config.EnvState{SHA: "websha", Version: "web-v2.0.0"}})
	}

	var slept int
	err := CommitWithRetry(Options{
		Client:  fake,
		Repo:    "owner/name",
		Path:    ".github/manifest.yaml",
		Ref:     "main",
		Message: "chore: record web staging state",
		Mutate:  webMutate,
		Sleep:   noSleep(&slept),
	})
	require.NoError(t, err)

	assert.Equal(t, 2, fake.gets, "web must re-fetch after the 409")
	assert.Equal(t, 2, fake.puts, "web must re-PUT after the 409")

	// Both components' staging leaves survive with their exact fields. Parse the
	// merged manifest and assert the typed model carries both components.
	file, err := config.ParseManifestBytes([]byte(fake.content), "ci")
	require.NoError(t, err)
	require.NotNil(t, file.State, "state must be present after the merge")

	assert.Contains(t, fake.content, "apisha", "api's committed leaf must survive web's re-applied write")
	assert.Contains(t, fake.content, "websha", "web's leaf must be written")
	assert.Contains(t, fake.content, "api-v1.0.0", "api leaf fields intact")
	assert.Contains(t, fake.content, "web-v2.0.0", "web leaf fields intact")
}

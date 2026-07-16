package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const twoComponentStateManifest = `ci:
  config:
    environments: [dev, prod]
  state:
    components:
      api:
        dev:
          sha: apidevsha
          version: api-0.1.0-rc.0
        prod:
          sha: apiprodsha
          version: api-0.1.0
      web:
        dev:
          sha: webdevsha
          version: web-0.1.0-rc.0
`

// TestReadComponentState_ReadsOnlyNamedComponent proves the reader returns every
// env row a single component owns and ignores its siblings, so a flat-map
// consumer can overlay one component's seed without pulling in another's.
func TestReadComponentState_ReadsOnlyNamedComponent(t *testing.T) {
	got, err := ReadComponentState([]byte(twoComponentStateManifest), "ci", "api")
	require.NoError(t, err)
	require.Len(t, got, 2, "api owns dev and prod rows")
	require.Equal(t, "apidevsha", got["dev"].SHA)
	require.Equal(t, "api-0.1.0-rc.0", got["dev"].Version)
	require.Equal(t, "apiprodsha", got["prod"].SHA)
	require.Equal(t, "api-0.1.0", got["prod"].Version)
	_, hasWebLeak := got["web"]
	require.False(t, hasWebLeak, "reader must not leak a sibling component's rows")
}

// TestReadComponentState_MissingComponentOrSubtree proves absent inputs yield a
// nil map and no error, so an overlay is a clean no-op when a component has no
// recorded state yet or the manifest declares no components at all.
func TestReadComponentState_MissingComponentOrSubtree(t *testing.T) {
	got, err := ReadComponentState([]byte(twoComponentStateManifest), "ci", "billing")
	require.NoError(t, err)
	require.Nil(t, got, "an undeclared component yields no rows")

	const flat = `ci:
  config:
    environments: [dev]
  state:
    dev:
      sha: devsha
`
	got, err = ReadComponentState([]byte(flat), "ci", "api")
	require.NoError(t, err)
	require.Nil(t, got, "a manifest with no components subtree yields no rows")
}

// TestReadComponentState_RoundTripsWriteScopedState proves the reader observes
// exactly what the component-scoped writer records: writing a component's env row
// then reading it back yields the same SHA and version, so overlay-then-write is
// a faithful round trip.
func TestReadComponentState_RoundTripsWriteScopedState(t *testing.T) {
	const base = `ci:
  config:
    environments: [dev, prod]
`
	out, err := WriteScopedState([]byte(base), "ci",
		StateWrite{Component: "api", Env: "dev", State: &EnvState{SHA: "s1", Version: "api-0.1.0-rc.0"}},
	)
	require.NoError(t, err)

	got, err := ReadComponentState(out, "ci", "api")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "s1", got["dev"].SHA)
	require.Equal(t, "api-0.1.0-rc.0", got["dev"].Version)
}

// TestOverlayComponentState_LiftsRowsIntoFlatMap proves the overlay copies the
// named component's env rows into the working flat state map, leaves envs the
// component does not record untouched, and is a strict no-op for the empty
// (single-component) name.
func TestOverlayComponentState_LiftsRowsIntoFlatMap(t *testing.T) {
	cicd := &CICDFile{State: map[string]*EnvState{
		"staging": {SHA: "flatstagingsha"},
	}}
	require.NoError(t, OverlayComponentState(cicd, []byte(twoComponentStateManifest), "ci", "api"))
	require.Equal(t, "apidevsha", cicd.State["dev"].SHA)
	require.Equal(t, "apiprodsha", cicd.State["prod"].SHA)
	require.Equal(t, "flatstagingsha", cicd.State["staging"].SHA, "envs the component does not record stay untouched")
	_, hasWebLeak := cicd.State["web"]
	require.False(t, hasWebLeak, "overlay must not pull in a sibling component's rows")
}

// TestOverlayComponentState_EmptyComponentAndNilMap covers the no-op and
// nil-map initialization edges: an empty component name changes nothing, and a
// nil working map is allocated before rows are lifted into it.
func TestOverlayComponentState_EmptyComponentAndNilMap(t *testing.T) {
	cicd := &CICDFile{}
	require.NoError(t, OverlayComponentState(cicd, []byte(twoComponentStateManifest), "ci", ""))
	require.Nil(t, cicd.State, "empty component name must be a strict no-op")

	require.NoError(t, OverlayComponentState(cicd, []byte(twoComponentStateManifest), "ci", "web"))
	require.NotNil(t, cicd.State)
	require.Equal(t, "webdevsha", cicd.State["dev"].SHA)
}

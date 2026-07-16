package release

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// failRoundTripper fails the test if any HTTP request is attempted, proving a
// code path short-circuits before touching the network.
type failRoundTripper struct{ t *testing.T }

func (f failRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Fatalf("unexpected HTTP request to %s: update() must short-circuit on a non-GitHub host", r.URL)
	return nil, nil
}

// TestUpdate_SkipsReleaseObjectOnNonGitHubHost pins the guard that lets the
// hotfix finalize convergence rerun (which routes through update via
// ActionUpdate) succeed on the Gitea e2e backend. Gitea's release-object
// endpoints reject the GitHub release shape and Bearer auth, so update() must
// mirror create() and short-circuit before findRelease. Without the guard this
// test fails: update() would issue the findRelease request and hit the
// fail-on-call transport. Real-GitHub release-object convergence is covered by
// the finalize rerun unit test and the live fleet.
func TestUpdate_SkipsReleaseObjectOnNonGitHubHost(t *testing.T) {
	m := &Manager{
		client:  &http.Client{Transport: failRoundTripper{t}},
		baseURL: "http://gitea.local",
		token:   "t",
		repo:    "owner/repo",
	}

	res, err := m.update(Options{Tag: "v1.2.3", SHA: "deadbeef", CreateTag: true, Environment: "test"})

	require.NoError(t, err, "update() on a non-GitHub host must return synthetic success, not a release-API error")
	require.NotNil(t, res)
}

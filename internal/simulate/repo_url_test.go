package simulate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeGitRemote(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "scp ssh with .git", in: "git@github.com:stablekernel/cascade.git", want: "https://github.com/stablekernel/cascade"},
		{name: "scp ssh without .git", in: "git@github.com:stablekernel/cascade", want: "https://github.com/stablekernel/cascade"},
		{name: "https with .git", in: "https://github.com/stablekernel/cascade.git", want: "https://github.com/stablekernel/cascade"},
		{name: "https without .git", in: "https://github.com/stablekernel/cascade", want: "https://github.com/stablekernel/cascade"},
		{name: "ssh url scheme", in: "ssh://git@github.com/stablekernel/cascade.git", want: "https://github.com/stablekernel/cascade"},
		{name: "ssh url scheme with port", in: "ssh://git@ssh.github.com:443/stablekernel/cascade.git", want: "https://ssh.github.com/stablekernel/cascade"},
		{name: "empty", in: "", want: ""},
		{name: "unrecognized", in: "file:///tmp/repo", want: ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, normalizeGitRemote(tc.in))
		})
	}
}

func TestResolveRepoURL_FromEnv(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "stablekernel/cascade")
	t.Setenv("GITHUB_SERVER_URL", "")
	assert.Equal(t, "https://github.com/stablekernel/cascade", resolveRepoURL())

	t.Setenv("GITHUB_SERVER_URL", "https://ghe.example.com/")
	assert.Equal(t, "https://ghe.example.com/stablekernel/cascade", resolveRepoURL())
}

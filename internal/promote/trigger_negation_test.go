package promote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initNegationRepo builds a repo whose commit ranges each isolate one kind of
// change: c0 (README) -> c1 (docs only) -> c2 (src/vendor only) -> c3 (src only).
func initNegationRepo(t *testing.T) (repoDir string, shas [4]string) {
	t.Helper()
	repoDir = t.TempDir()
	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "config", "commit.gpgsign", "false")

	commit := func(rel, msg string) string {
		full := filepath.Join(repoDir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("content: "+rel+"\n"), 0o644))
		runGit(t, repoDir, "add", rel)
		runGit(t, repoDir, "commit", "-m", msg)
		return gitOut(t, repoDir, "rev-parse", "HEAD")
	}

	shas[0] = commit("README.md", "chore: init")
	shas[1] = commit("docs/guide.md", "docs: guide")
	shas[2] = commit("src/vendor/lib.go", "chore: vendor")
	shas[3] = commit("src/app.go", "feat: app")
	return repoDir, shas
}

// TestHasChanges_NegationContract pins the promote preflight change detection
// to the canonical trigger-negation contract of config.MatchTrigger: a "!"
// pattern is an exclusion (GitHub Actions paths/paths-ignore combined
// semantics), matching the paths: filter the generator emits verbatim. The
// pre-fix evaluator matched "!" patterns literally, so an exclusion was inert
// and a negation-only list could never match anything.
func TestHasChanges_NegationContract(t *testing.T) {
	repoDir, shas := initNegationRepo(t)
	p := &Preflighter{baseDir: repoDir}

	cases := []struct {
		name     string
		base     string
		head     string
		triggers []string
		want     bool
	}{
		{
			name:     "exclusion is not an inclusion",
			base:     shas[0],
			head:     shas[1],
			triggers: []string{"src/**", "!docs/**"},
			want:     false,
		},
		{
			// Old behavior: src/vendor/lib.go matched "src/**" and the inert
			// "!" pattern never subtracted it -> true.
			name:     "mixed list honours the exclusion",
			base:     shas[1],
			head:     shas[2],
			triggers: []string{"src/**", "!src/vendor/**"},
			want:     false,
		},
		{
			name:     "mixed list still includes non-excluded positives",
			base:     shas[2],
			head:     shas[3],
			triggers: []string{"src/**", "!src/vendor/**"},
			want:     true,
		},
		{
			// Old behavior: the literal "!docs/**" pattern matched nothing, so
			// a negation-only list always reported "no changes" -> false.
			// Canonical: negation-only acts as paths-ignore, so any
			// non-excluded change triggers.
			name:     "negation-only list triggers on non-excluded change",
			base:     shas[2],
			head:     shas[3],
			triggers: []string{"!docs/**"},
			want:     true,
		},
		{
			name:     "negation-only list skips an excluded-only change",
			base:     shas[0],
			head:     shas[1],
			triggers: []string{"!docs/**"},
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.hasChanges(tc.base, tc.head, tc.triggers)
			assert.Equal(t, tc.want, got)

			// Agreement assertion: preflight must predict exactly what the
			// canonical evaluator (and therefore the emitted GHA paths filter)
			// decides for the same diff.
			var changed []string
			for _, f := range strings.Split(gitOut(t, repoDir, "diff", "--name-only", tc.base, tc.head), "\n") {
				if f = strings.TrimSpace(f); f != "" {
					changed = append(changed, f)
				}
			}
			assert.Equal(t, config.MatchAnyTrigger(tc.triggers, changed), got,
				"hasChanges disagrees with config.MatchAnyTrigger")
		})
	}
}

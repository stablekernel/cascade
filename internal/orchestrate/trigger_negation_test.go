package orchestrate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
)

// initNegationRepo builds a repo whose commit ranges each isolate one kind of
// change: c0 (README) -> c1 (docs only) -> c2 (src/vendor only) -> c3 (src only).
func initNegationRepo(t *testing.T) (repoDir string, shas [4]string) {
	t.Helper()
	repoDir = t.TempDir()
	runGit(t, repoDir, "init", "-b", "main")

	commit := func(rel, msg string) string {
		writeFile(t, repoDir, rel, "content: "+rel+"\n")
		runGit(t, repoDir, "add", rel)
		runGit(t, repoDir, "commit", "-m", msg)
		return runGit(t, repoDir, "rev-parse", "HEAD")
	}

	shas[0] = commit("README.md", "chore: init")
	shas[1] = commit("docs/guide.md", "docs: guide")
	shas[2] = commit("src/vendor/lib.go", "chore: vendor")
	shas[3] = commit("src/app.go", "feat: app")
	return repoDir, shas
}

// TestDetectChanges_NegationContract pins detectChanges to the canonical
// trigger-negation contract of config.MatchTrigger: a "!" pattern is an
// exclusion (GitHub Actions paths/paths-ignore combined semantics), matching
// the paths: filter the generator emits verbatim. The pre-fix evaluator
// stripped "!" and matched the bare glob positively, turning exclusions into
// inclusions.
func TestDetectChanges_NegationContract(t *testing.T) {
	repoDir, shas := initNegationRepo(t)
	o := &Orchestrator{baseDir: repoDir}

	cases := []struct {
		name     string
		base     string
		head     string
		triggers []string
		want     bool
	}{
		{
			// Old behavior: "!docs/**" matched docs/guide.md positively -> true.
			name:     "exclusion is not an inclusion",
			base:     shas[0],
			head:     shas[1],
			triggers: []string{"src/**", "!docs/**"},
			want:     false,
		},
		{
			// Old behavior: src/vendor/lib.go matched "src/**" and the
			// exclusion was ignored -> true.
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
			// Old behavior: "!docs/**" stripped to docs/** matched nothing in
			// a src-only diff -> false. Canonical: negation-only list acts as
			// paths-ignore, so any non-excluded change triggers.
			name:     "negation-only list triggers on non-excluded change",
			base:     shas[2],
			head:     shas[3],
			triggers: []string{"!docs/**"},
			want:     true,
		},
		{
			// Old behavior: docs/guide.md matched the stripped docs/** -> true.
			name:     "negation-only list skips an excluded-only change",
			base:     shas[0],
			head:     shas[1],
			triggers: []string{"!docs/**"},
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := o.detectChanges(tc.base, tc.head, tc.triggers)
			assert.Equal(t, tc.want, got)

			// Agreement assertion: detectChanges must predict exactly what the
			// canonical evaluator (and therefore the emitted GHA paths filter)
			// decides for the same diff.
			var changed []string
			for _, f := range strings.Split(runGit(t, repoDir, "diff", "--name-only", tc.base, tc.head), "\n") {
				if f = strings.TrimSpace(f); f != "" {
					changed = append(changed, f)
				}
			}
			assert.Equal(t, config.MatchAnyTrigger(tc.triggers, changed), got,
				"detectChanges disagrees with config.MatchAnyTrigger")
		})
	}
}

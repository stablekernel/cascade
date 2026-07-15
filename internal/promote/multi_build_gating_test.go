package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initMultiBuildRepo builds a repo whose commit ranges each isolate one
// component's change: c0 (README) -> c1 (api only) -> c2 (web only)
// -> c3 (docs/api only) -> c4 (unrelated only).
func initMultiBuildRepo(t *testing.T) (repoDir string, shas [5]string) {
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
	shas[1] = commit("api/main.go", "feat: api")
	shas[2] = commit("web/app.js", "feat: web")
	shas[3] = commit("docs/api/readme.md", "docs: api reference")
	shas[4] = commit("unrelated/notes.txt", "chore: notes")
	return repoDir, shas
}

// TestDetectDeployChanges_MultiBuildDependsOn pins promotion change detection
// for a deploy that depends on more than one build: the deploy is promoted
// when ANY referenced build's triggers match the diff, and each build's
// trigger list is evaluated independently so one build's "!" exclusion cannot
// veto a sibling build's positive match. The regression this guards: change
// detection consulted only DependsOn[0], so a diff touching only the second
// build's paths silently dropped the deploy from the promotion set.
func TestDetectDeployChanges_MultiBuildDependsOn(t *testing.T) {
	repoDir, shas := initMultiBuildRepo(t)

	cfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "api", Workflow: ".github/workflows/build-api.yaml", Triggers: []string{"api/**"}},
			{Name: "web", Workflow: ".github/workflows/build-web.yaml", Triggers: []string{"web/**"}},
			{Name: "md", Workflow: ".github/workflows/build-md.yaml", Triggers: []string{"**/*.md"}},
			{Name: "docs", Workflow: ".github/workflows/build-docs.yaml", Triggers: []string{"docs/**", "!docs/api/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "services", Workflow: ".github/workflows/deploy.yaml", DependsOn: []string{"api", "web"}},
			{Name: "docs-site", Workflow: ".github/workflows/deploy-docs.yaml", DependsOn: []string{"md", "docs"}},
		},
	}

	cases := []struct {
		name      string
		targetSHA string // what the target env is running
		sourceSHA string // what promotion would bring in
		deploy    string
		want      bool
	}{
		{
			name:      "first dependency changed",
			targetSHA: shas[0], sourceSHA: shas[1],
			deploy: "services", want: true,
		},
		{
			// The regression case: only the SECOND build's paths changed.
			name:      "second dependency changed",
			targetSHA: shas[1], sourceSHA: shas[2],
			deploy: "services", want: true,
		},
		{
			name:      "no dependency changed",
			targetSHA: shas[3], sourceSHA: shas[4],
			deploy: "services", want: false,
		},
		{
			// docs/api/readme.md matches build "md" ("**/*.md"). Build
			// "docs" excludes it ("!docs/api/**"), but that exclusion is
			// scoped to the "docs" build: concatenating the two lists would
			// let the trailing exclusion veto the sibling's match under the
			// order-dependent (last-match-wins) trigger contract.
			name:      "sibling exclusion does not veto a match",
			targetSHA: shas[2], sourceSHA: shas[3],
			deploy: "docs-site", want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Preflighter{
				cicdFile: &config.CICDFile{
					Config: cfg,
					State: map[string]*config.EnvState{
						"prod": {SHA: tc.targetSHA},
					},
				},
				baseDir: repoDir,
			}
			localDeploys, _ := p.detectDeployChanges(tc.sourceSHA, "prod")
			if tc.want {
				assert.Contains(t, localDeploys, tc.deploy)
			} else {
				assert.NotContains(t, localDeploys, tc.deploy)
			}
		})
	}
}

// TestHasChanges_RepoBoundaryPinned pins the preflight diff exec to the repo
// boundary contract of git.BoundaryEnv: when baseDir is not itself a
// repository, repository discovery must fail closed (include the deploy)
// rather than silently walking up into an enclosing repository and reading
// that repository's diff.
func TestHasChanges_RepoBoundaryPinned(t *testing.T) {
	repoDir, shas := initMultiBuildRepo(t)

	// A plain directory inside the repo: not a repository itself, so an
	// unpinned "git diff" run there resolves to the enclosing repo, whose
	// c3->c4 diff (unrelated/notes.txt) matches no trigger and would return
	// a confident (and wrong-repo) "no changes".
	nonRepoDir := filepath.Join(repoDir, "unrelated")

	p := &Preflighter{baseDir: nonRepoDir}
	got := p.hasChanges(shas[3], shas[4], []string{"src/**"})
	assert.True(t, got,
		"hasChanges must not consult an enclosing repository; a failed diff fails closed")
}

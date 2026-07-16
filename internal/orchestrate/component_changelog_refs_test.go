package orchestrate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// writeComponentChangelogManifest writes a two-component manifest whose
// environments line and recorded state/latest_release blocks are supplied by
// the caller, so each test composes exactly the ladder and markers it needs.
// It returns the manifest path.
func writeComponentChangelogManifest(t *testing.T, repoDir, environments, apiOverrides, recorded string) string {
	t.Helper()
	manifest := `ci:
  config:
    trunk_branch: main
    environments: ` + environments + `
    components:
      api:
        path: src
        tag_grammar:
          prefix: api-
` + apiOverrides + `      web:
        path: web
        tag_grammar:
          prefix: web-
    builds:
      - name: appbuild
        workflow: build.yaml
        triggers: ["src/**"]
    deploys:
      - name: app
        run: echo deploy app
` + recorded
	writeFile(t, repoDir, ".github/manifest.yaml", manifest)
	return repoDir + "/.github/manifest.yaml"
}

// TestChangelogRefs_Component_UsesRecordedComponentLatestRelease is the
// component-scoped counterpart of the "last published release" tier: after a
// component's first publish, the marker recorded under
// latest_release.components.<component> must anchor the changelog base, not
// the repo's initial commit. The top-level latest_release fields are
// structurally empty for a component-scoped manifest, so reading only them
// re-manifests the full-history changelog of issue #80 for every component
// orchestrate after the first publish.
func TestChangelogRefs_Component_UsesRecordedComponentLatestRelease(t *testing.T) {
	repoDir, _ := initRepo(t)
	manifestPath := writeComponentChangelogManifest(t, repoDir, "[dev]", "", `  latest_release:
    components:
      api:
        version: api-1.2.0
        sha: componentrelsha
      web:
        version: web-3.0.0
        sha: webrelsha
`)

	// dev is the terminal (only) env, so the next-env tier cannot answer and
	// the release marker must.
	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	gotSHA, gotTag := orch.calculateChangelogRefs()
	require.Equal(t, "componentrelsha", gotSHA,
		"component changelog base must come from latest_release.components.api, not the initial commit")
	require.Equal(t, "api-1.2.0", gotTag)
}

// TestChangelogRefs_Component_OverriddenLadderIndexesComponentNextEnv covers a
// component whose environments override narrows the global ladder: the
// next-env probe must walk the component's own ladder, so dev compares against
// the component's prod row, not the global ladder's staging (which holds no
// state for this component and would collapse the base to the initial commit).
func TestChangelogRefs_Component_OverriddenLadderIndexesComponentNextEnv(t *testing.T) {
	repoDir, _ := initRepo(t)
	manifestPath := writeComponentChangelogManifest(t, repoDir, "[dev, staging, prod]", `        environments: [dev, prod]
`, `  state:
    components:
      api:
        prod:
          sha: apiprodsha
          version: api-1.0.0
`)

	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	gotSHA, gotTag := orch.calculateChangelogRefs()
	require.Equal(t, "apiprodsha", gotSHA,
		"overridden component ladder must index the component's own next env (prod), not the global staging")
	require.Equal(t, "api-1.0.0", gotTag)
}

// TestChangelogRefs_Component_FallsBackToComponentReleaseTag covers a component
// with no recorded release marker but a published tag in its own namespace: the
// tag (the same base calculateComponentVersion resolves) must anchor the
// changelog before the initial-commit fallback fires.
func TestChangelogRefs_Component_FallsBackToComponentReleaseTag(t *testing.T) {
	repoDir, headSHA := initRepo(t)
	manifestPath := writeComponentChangelogManifest(t, repoDir, "[dev]", "", "")
	runGit(t, repoDir, "tag", "api-1.0.0")

	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	gotSHA, gotTag := orch.calculateChangelogRefs()
	require.Equal(t, headSHA, gotSHA,
		"with no recorded marker the component's own release tag must anchor the changelog base")
	require.Equal(t, "api-1.0.0", gotTag)
}

// TestChangelogRefs_Component_NothingReleasedFallsBackToInitialCommit pins the
// final tier for components: with no next-env state, no recorded marker, and no
// tags in the component's namespace, the base is the repo's initial commit and
// the previous tag is empty (the changelog is the component's introduction).
func TestChangelogRefs_Component_NothingReleasedFallsBackToInitialCommit(t *testing.T) {
	repoDir, _ := initRepo(t)
	manifestPath := writeComponentChangelogManifest(t, repoDir, "[dev]", "", "")
	// A sibling's tag must not leak into api's fallback.
	runGit(t, repoDir, "tag", "web-2.0.0")
	initialSHA := runGit(t, repoDir, "rev-list", "--max-parents=0", "HEAD")

	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	gotSHA, gotTag := orch.calculateChangelogRefs()
	require.Equal(t, initialSHA, gotSHA)
	require.Empty(t, gotTag)
}

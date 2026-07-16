package orchestrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeComponentStateManifest writes a two-component manifest whose recorded
// state lives under state.components.api.dev with a fully populated leaf:
// env-level sha/version, a divergence record (ref/base_sha/patches), a
// deploy-history ring, and per-deploy rows for two apps. It returns the
// manifest path.
func writeComponentStateManifest(t *testing.T, repoDir, recordedSHA string) string {
	t.Helper()
	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev]
    components:
      api:
        path: src
        tag_grammar:
          prefix: api-
      web:
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
      - name: other
        run: echo deploy other
  state:
    components:
      api:
        dev:
          sha: ` + recordedSHA + `
          version: api-0.1.0
          ref: hotfix/api-dev
          base_sha: basesha1234
          patches: [patchsha5678]
          previous:
            - sha: prevsha1
              version: api-0.0.9
          deploys:
            app:
              sha: ` + recordedSHA + `
            other:
              sha: othersha
          builds:
            appbuild:
              sha: ` + recordedSHA + `
      web:
        dev:
          sha: websha
          version: web-0.1.0
`
	writeFile(t, repoDir, ".github/manifest.yaml", manifest)
	return filepath.Join(repoDir, ".github", "manifest.yaml")
}

// TestNewOrchestrator_Component_OverlaysRecordedEnvState verifies that a
// component-scoped orchestrator lifts the component's recorded rows from
// state.components.<name>.<env> into the working flat state map, the same
// overlay the promote and hotfix paths perform. Without it every State[env]
// lookup sees nil: the base-SHA ladder collapses to the HEAD~1 first-run
// fallback and Finalize rebuilds the leaf from empty.
func TestNewOrchestrator_Component_OverlaysRecordedEnvState(t *testing.T) {
	repoDir, _ := initRepo(t)
	manifestPath := writeComponentStateManifest(t, repoDir, "recordedsha")

	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	st := orch.cicdFile.State["dev"]
	require.NotNil(t, st, "component-scoped orchestrator must overlay state.components.api.dev into State[dev]")
	require.Equal(t, "recordedsha", st.SHA)
	require.Equal(t, "api-0.1.0", st.Version)
	require.Equal(t, "hotfix/api-dev", st.Ref)
	require.Equal(t, "basesha1234", st.BaseSHA)
	require.Equal(t, []string{"patchsha5678"}, st.Patches)
	require.Len(t, st.Previous, 1)
	require.Equal(t, "prevsha1", st.Previous[0].SHA)
	require.NotNil(t, st.Deploys["other"])
	require.Equal(t, "othersha", st.Deploys["other"].SHA)
	require.NotNil(t, st.Builds["appbuild"])
	require.Equal(t, "recordedsha", st.Builds["appbuild"].SHA)
}

// TestSetup_Component_NoChangeReDispatch_SkipsBuild is the component-scoped
// counterpart of TestSetup_NoChangeReDispatch_SkipsBuild: with the component's
// recorded state at HEAD, a re-dispatch must anchor the base-SHA ladder on the
// recorded rows and skip the unchanged build, not fall back to HEAD~1 and
// re-run it on every dispatch.
func TestSetup_Component_NoChangeReDispatch_SkipsBuild(t *testing.T) {
	repoDir, headSHA := initRepo(t)
	manifestPath := writeComponentStateManifest(t, repoDir, headSHA)

	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	res, err := orch.Setup(headSHA)
	require.NoError(t, err)
	require.False(t, res.RunBuilds["appbuild"],
		"no-change component re-dispatch must skip the build; the recorded build SHA anchors the ladder")
	require.Equal(t, headSHA, res.BaseSHAs["build_appbuild"],
		"build base SHA must come from the component's recorded state, not the HEAD~1 fallback")
}

// TestFinalize_Component_PreservesRecordedLeaf runs the real component-scoped
// Finalize (state write, commit, push) against a manifest whose component leaf
// already records history, and asserts the rewritten leaf carries every prior
// row forward: the untouched sibling deploy, the deploy-history ring, and the
// divergence record. Rebuilding the leaf from an empty working state would
// silently drop all of them.
func TestFinalize_Component_PreservesRecordedLeaf(t *testing.T) {
	repoDir, headSHA := initRepo(t)
	manifestPath := writeComponentStateManifest(t, repoDir, "recordedsha")
	runGit(t, repoDir, "add", ".github/manifest.yaml")
	runGit(t, repoDir, "commit", "-m", "chore: seed manifest")

	// Finalize commits and pushes the state write; give it a bare upstream.
	remoteDir := t.TempDir()
	runGit(t, remoteDir, "init", "--bare", "-b", "main")
	runGit(t, repoDir, "remote", "add", "origin", remoteDir)
	runGit(t, repoDir, "push", "-u", "origin", "main")

	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	err = orch.Finalize(headSHA, "api-0.1.1",
		map[string]string{"app": "success"},
		map[string]string{"appbuild": "success"},
	)
	require.NoError(t, err)

	out, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	m := readManifestTree(t, out)
	leaf := componentEnvRow(t, m, "api", "dev")

	// The run's own updates landed.
	require.Equal(t, headSHA, leaf["sha"])
	require.Equal(t, "api-0.1.1", leaf["version"])

	deploys, ok := leaf["deploys"].(map[string]any)
	require.True(t, ok, "deploys present in rewritten leaf")
	app, ok := deploys["app"].(map[string]any)
	require.True(t, ok, "deploys.app present")
	require.Equal(t, headSHA, app["sha"])

	// The prior rows the run never touched survive.
	other, ok := deploys["other"].(map[string]any)
	require.True(t, ok, "untouched sibling deploy row must survive the leaf rewrite")
	require.Equal(t, "othersha", other["sha"])

	prev, ok := leaf["previous"].([]any)
	require.True(t, ok, "deploy-history ring must survive the leaf rewrite")
	require.Len(t, prev, 1)

	require.Equal(t, "hotfix/api-dev", leaf["ref"], "divergence ref must survive")
	require.Equal(t, "basesha1234", leaf["base_sha"], "divergence base_sha must survive")
	patches, ok := leaf["patches"].([]any)
	require.True(t, ok, "divergence patches must survive")
	require.Equal(t, []any{"patchsha5678"}, patches)

	// The sibling component's subtree is untouched.
	sib := componentEnvRow(t, m, "web", "dev")
	require.Equal(t, "websha", sib["sha"])
	require.Equal(t, "web-0.1.0", sib["version"])
}

package orchestrate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// writeComponentOwnCallbacksManifest writes the shape the components guide
// documents as canonical: no top-level builds or deploys at all, each component
// declaring its own. The two components' callback names are deliberately
// divergent (api-build/api-deploy vs web-build/web-deploy) so a runtime planning
// against the root config cannot accidentally produce a matching key.
func writeComponentOwnCallbacksManifest(t *testing.T, repoDir string) string {
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
        builds:
          - name: api-build
            workflow: api-build.yaml
            triggers: ["src/**"]
        deploys:
          - name: api-deploy
            workflow: api-deploy.yaml
            triggers: ["src/**"]
      web:
        path: web
        tag_grammar:
          prefix: web-
        builds:
          - name: web-build
            workflow: web-build.yaml
            triggers: ["web/**"]
        deploys:
          - name: web-deploy
            workflow: web-deploy.yaml
            triggers: ["web/**"]
`
	writeFile(t, repoDir, ".github/manifest.yaml", manifest)
	return repoDir + "/.github/manifest.yaml"
}

// TestSetup_Component_PlansComponentOwnBuildsAndDeploys pins the core of the
// component-scoped planning contract: the generator emits a component's
// orchestrate workflow from that component's RESOLVED config, so the job gates
// read needs.setup.outputs.run_build_<component's own build name>. A runtime that
// enumerates the ROOT config's builds and deploys emits keys under root names,
// every gate reads an absent output, and every build and deploy silently skips
// while the run still reports success.
//
// The manifest here is the canonical documented shape (no top-level builds or
// deploys, each component declaring its own), which makes the root enumeration
// return EMPTY maps: not one job of either component would ever run.
func TestSetup_Component_PlansComponentOwnBuildsAndDeploys(t *testing.T) {
	repoDir, headSHA := initRepo(t)
	manifestPath := writeComponentOwnCallbacksManifest(t, repoDir)

	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	res, err := orch.Setup(headSHA)
	require.NoError(t, err)

	require.Contains(t, res.RunBuilds, "api-build",
		"component-scoped setup must plan the component's OWN build; the generated gate reads run_build_api-build")
	require.Contains(t, res.RunDeploys, "api-deploy",
		"component-scoped setup must plan the component's OWN deploy; the generated gate reads run_deploy_api-deploy")

	// The sibling component's callbacks belong to the sibling's own workflow.
	require.NotContains(t, res.RunBuilds, "web-build",
		"component-scoped setup must not plan a sibling component's build")
	require.NotContains(t, res.RunDeploys, "web-deploy",
		"component-scoped setup must not plan a sibling component's deploy")

	// The base-SHA ladder is keyed off the same enumeration.
	require.Contains(t, res.BaseSHAs, "build_api-build",
		"base-SHA ladder must cover the component's own build")
	require.Contains(t, res.BaseSHAs, "deploy_api-deploy",
		"base-SHA ladder must cover the component's own deploy")
}

// TestSetup_Component_InheritsRootCallbacksWhenNotOverridden is the differential
// control: a component that declares no builds or deploys of its own inherits the
// root ones, and the resolved-config swap must not disturb that. It guards the
// fix against over-correcting into "component config only".
func TestSetup_Component_InheritsRootCallbacksWhenNotOverridden(t *testing.T) {
	repoDir, headSHA := initRepo(t)
	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev]
    builds:
      - name: shared-build
        workflow: build.yaml
        triggers: ["src/**"]
    deploys:
      - name: shared-deploy
        workflow: deploy.yaml
        triggers: ["src/**"]
    components:
      api:
        path: src
        tag_grammar:
          prefix: api-
`
	writeFile(t, repoDir, ".github/manifest.yaml", manifest)
	manifestPath := repoDir + "/.github/manifest.yaml"

	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("api"))
	require.NoError(t, err)

	res, err := orch.Setup(headSHA)
	require.NoError(t, err)

	require.Contains(t, res.RunBuilds, "shared-build",
		"a component declaring no builds inherits the root builds")
	require.Contains(t, res.RunDeploys, "shared-deploy",
		"a component declaring no deploys inherits the root deploys")
}

// TestSetup_UndeclaredComponent_RefusesAtVersion pins the undeclared/state-only
// component policy, which differs from promote's (that path no-ops to the root
// config). A component may be recorded only under state.components without a
// config.components declaration. Orchestrate mints versions, and an undeclared
// component has no tag namespace of its own: orchestrating it would derive an
// unprefixed version from repo-wide commits and collide with the repo-global
// namespace. So orchestrate refuses, loudly, at version calculation. The
// resolved-config swap must leave that refusal intact rather than silently
// planning a ghost component against the root config.
func TestSetup_UndeclaredComponent_RefusesAtVersion(t *testing.T) {
	repoDir, headSHA := initRepo(t)
	manifest := `ci:
  config:
    trunk_branch: main
    environments: [dev]
    builds:
      - name: shared-build
        workflow: build.yaml
        triggers: ["src/**"]
  state:
    components:
      ghost:
        dev:
          sha: ghostsha
          version: 0.1.0
`
	writeFile(t, repoDir, ".github/manifest.yaml", manifest)
	manifestPath := repoDir + "/.github/manifest.yaml"

	// Construction succeeds: the state overlay is what a state-only component is
	// for, and the swap no-ops rather than failing on a name it cannot resolve.
	orch, err := NewOrchestrator(manifestPath, "ci", "dev", WithComponent("ghost"))
	require.NoError(t, err, "a state-only component must not fail construction")
	require.Nil(t, orch.resolved, "an undeclared component has no resolved view to swap in")

	_, err = orch.Setup(headSHA)
	require.Error(t, err, "orchestrate must refuse to mint a version for an undeclared component")
	require.ErrorContains(t, err, `component "ghost" is not declared`)
}

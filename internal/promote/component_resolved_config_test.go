package promote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// componentResolvedConfigManifest declares a root deploy (root-svc) and a
// component "api" that overrides deploys with its own name (api-svc) and its
// tag grammar with a strict prefix plus a custom pre-release token. The
// generator emits api's promote workflow from this resolved config: its deploy
// job is named after api-svc and gated on deploys_to_run containing "api-svc".
// The promote runtime must plan against the same resolved config, or the
// generated gate and the runtime plan disagree and every deploy skips.
const componentResolvedConfigManifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, staging, prod]
    deploys:
      - name: root-svc
        workflow: deploy.yaml
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-
          prerelease_token: beta
        deploys:
          - name: api-svc
            workflow: deploy.yaml
  state:
    components:
      api:
        dev:
          sha: apidevsha
          version: api-1.0.1-beta.0
`

func writeResolvedConfigManifest(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(path, []byte(manifest), 0o644))
	return path
}

// TestPreflight_ComponentDeploysOverride_RunsComponentDeploys reproduces the
// executed failure: component api overrides deploys with [api-svc], the
// generated per-component workflow gates its deploy job on
// contains(deploys_to_run, 'api-svc'), so preflight must emit the COMPONENT's
// deploy names. A runtime that plans from the root config emits ["root-svc"],
// the gate never matches, and every deploy skips while the promotion still
// records success.
func TestPreflight_ComponentDeploysOverride_RunsComponentDeploys(t *testing.T) {
	path := writeResolvedConfigManifest(t, componentResolvedConfigManifest)
	cicdFile, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	require.NoError(t, err)
	require.NoError(t, overlayComponentState(cicdFile, path, config.DefaultManifestKey, "api"))
	require.NoError(t, applyResolvedComponentConfig(cicdFile, "api"))

	pf := NewPreflighter(PreflighterOptions{
		Config: cicdFile,
		Mode:   ModeDefault,
	})

	result, err := pf.Run()
	require.NoError(t, err)
	require.Contains(t, result.DeploysToRun, "api-svc",
		"deploys_to_run must carry the component's own deploy name; the generated gate checks for it")
	require.NotContains(t, result.DeploysToRun, "root-svc",
		"the root deploy name must not leak into a component-scoped plan")
}

// TestPreflight_ComponentDowngrade_Blocked proves the downgrade gate holds for
// component-prefixed versions. A component's recorded versions carry its
// tag_grammar prefix (api-1.9.0); under the root grammar they do not parse and
// the gate fails open, silently permitting a prod-bound downgrade that the
// plain-semver control already blocks. With the resolved component config the
// grammar parses and the downgrade is blocked exactly like the control.
func TestPreflight_ComponentDowngrade_Blocked(t *testing.T) {
	const manifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, staging, prod]
    deploys:
      - name: root-svc
        workflow: deploy.yaml
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-
        deploys:
          - name: api-svc
            workflow: deploy.yaml
  state:
    components:
      api:
        dev:
          sha: apidevsha
          version: api-1.2.0
        staging:
          sha: apistagingsha
          version: api-1.9.0
`
	path := writeResolvedConfigManifest(t, manifest)
	cicdFile, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	require.NoError(t, err)
	require.NoError(t, overlayComponentState(cicdFile, path, config.DefaultManifestKey, "api"))
	require.NoError(t, applyResolvedComponentConfig(cicdFile, "api"))

	pf := NewPreflighter(PreflighterOptions{
		Config: cicdFile,
		Mode:   ModeDefault,
	})

	_, err = pf.Run()
	require.Error(t, err, "promoting api-1.2.0 onto staging holding api-1.9.0 is a downgrade and must block")
	require.Contains(t, err.Error(), "downgrade blocked")
}

// TestPreflight_PlainSemverDowngrade_Blocked is the differential control for
// the component downgrade test: the same versions without a component prefix
// were always blocked. It pins the control side so the pair documents the
// asymmetry the resolved-config fix removes.
func TestPreflight_PlainSemverDowngrade_Blocked(t *testing.T) {
	const manifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, staging, prod]
    deploys:
      - name: root-svc
        workflow: deploy.yaml
  state:
    dev:
      sha: devsha
      version: 1.2.0
    staging:
      sha: stagingsha
      version: 1.9.0
`
	path := writeResolvedConfigManifest(t, manifest)
	cicdFile, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	require.NoError(t, err)

	pf := NewPreflighter(PreflighterOptions{
		Config: cicdFile,
		Mode:   ModeDefault,
	})

	_, err = pf.Run()
	require.Error(t, err)
	require.Contains(t, err.Error(), "downgrade blocked")
}

// TestPromoter_ComponentPrereleaseToken_StrippedOnPublish proves the publish
// crossing strips a component's own pre-release token. The component narrows
// its ladder to [dev, staging], so dev->staging is its terminal publish
// crossing and the release data must carry api-1.4.0, not the beta-suffixed
// candidate the root grammar (token "rc") leaves unstripped.
func TestPromoter_ComponentPrereleaseToken_StrippedOnPublish(t *testing.T) {
	const manifest = `ci:
  config:
    trunk_branch: main
    environments: [dev, staging, prod]
    deploys:
      - name: root-svc
        workflow: deploy.yaml
    components:
      api:
        path: services/api
        environments: [dev, staging]
        tag_grammar:
          prefix: api-
          prerelease_token: beta
        deploys:
          - name: api-svc
            workflow: deploy.yaml
  state:
    components:
      api:
        dev:
          sha: apidevsha
          version: api-1.4.0-beta.3
`
	path := writeResolvedConfigManifest(t, manifest)

	p, err := NewPromoter(PromoterOptions{
		ConfigPath: path,
		DryRun:     true,
		Actor:      "test-actor",
		Component:  "api",
	})
	require.NoError(t, err)

	result, err := p.Promote(ModeDefault, "")
	require.NoError(t, err)
	require.True(t, result.Success, "publish crossing must succeed; error: %s", result.Error)
	require.NotNil(t, result.ReleaseData)
	require.Equal(t, "publish", result.ReleaseAction)
	require.Equal(t, "api-1.4.0", result.ReleaseData.SemVersion,
		"the component's beta token must be stripped on publish")
}

// TestNewFinalizer_ComponentResolvedDeploys proves a component-scoped finalize
// operates on the component's resolved deploys. The generated workflow sets
// DEPLOY_RESULT_API_SVC (its jobs are named from the resolved config); a
// finalizer that keeps the root config looks for DEPLOY_RESULT_ROOT_SVC
// instead, finds nothing, and advances env state with no deploy gate at all.
func TestNewFinalizer_ComponentResolvedDeploys(t *testing.T) {
	path := writeResolvedConfigManifest(t, componentResolvedConfigManifest)

	fin, err := NewFinalizer(path, "staging", WithComponent("api"))
	require.NoError(t, err)

	var names []string
	for _, d := range fin.cicdFile.Config.Deploys {
		names = append(names, d.Name)
	}
	require.Equal(t, []string{"api-svc"}, names,
		"a component finalize must read the component's resolved deploys, matching the DEPLOY_RESULT_* keys the generated workflow sets")

	t.Setenv("DEPLOY_RESULT_API_SVC", "success")
	results := readDeployResultsFromEnv(names)
	require.Equal(t, map[string]string{"api-svc": "success"}, results)
}

// TestGateOnExpectedDeploys covers the promote-side fail-safe: when preflight
// planned deploys but none of them reported any result (every gate skipped or
// the result wiring is absent), nothing deployed and the state write must be
// refused. A reported failure or cancellation does not abort this gate (the
// env-pointer hold is applied downstream in updateState, while per-deploy
// successes are still recorded); an empty plan is a legitimate no-deploy
// promotion (trigger filters matched nothing) and always proceeds.
func TestGateOnExpectedDeploys(t *testing.T) {
	t.Run("all expected deploys skipped aborts", func(t *testing.T) {
		err := gateOnExpectedDeploys([]string{"api-svc"}, map[string]string{"api-svc": "skipped"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "api-svc")
	})

	t.Run("unreported results abort", func(t *testing.T) {
		err := gateOnExpectedDeploys([]string{"api-svc"}, map[string]string{})
		require.Error(t, err)
	})

	t.Run("a success proceeds", func(t *testing.T) {
		require.NoError(t, gateOnExpectedDeploys(
			[]string{"api-svc", "worker"},
			map[string]string{"api-svc": "success", "worker": "skipped"},
		))
	})

	t.Run("a failure does not abort this gate (env-pointer hold is downstream)", func(t *testing.T) {
		require.NoError(t, gateOnExpectedDeploys(
			[]string{"api-svc"},
			map[string]string{"api-svc": "failure"},
		))
	})

	t.Run("empty plan proceeds", func(t *testing.T) {
		require.NoError(t, gateOnExpectedDeploys(nil, map[string]string{}))
	})
}

// TestParseExpectedDeploys pins the DEPLOYS_TO_RUN contract: the generated
// finalize step forwards preflight's deploys_to_run output verbatim, a JSON
// array. Empty and null values mean "no expected deploys" (older workflows do
// not set the variable at all); malformed values are a wiring bug and error.
func TestParseExpectedDeploys(t *testing.T) {
	got, err := parseExpectedDeploys(`["api-svc","worker"]`)
	require.NoError(t, err)
	require.Equal(t, []string{"api-svc", "worker"}, got)

	got, err = parseExpectedDeploys("")
	require.NoError(t, err)
	require.Empty(t, got)

	got, err = parseExpectedDeploys("null")
	require.NoError(t, err)
	require.Empty(t, got)

	_, err = parseExpectedDeploys("not-json")
	require.Error(t, err)
}

package generate

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"gopkg.in/yaml.v3"
)

// TestResolveActionlint_CIHardFailsWhenMissing proves the guard cannot silently
// disable itself. Under CI a missing actionlint binary must resolve to fatal
// (reds the guard), not skip; locally it skips; and a present binary is used.
// This is the contract that makes the emitted-workflow guard a real merge gate
// rather than a test that green-skips when actionlint is absent from the runner.
func TestResolveActionlint_CIHardFailsWhenMissing(t *testing.T) {
	t.Parallel()
	missing := func(string) (string, error) { return "", errors.New("not found") }
	present := func(string) (string, error) { return "/usr/bin/actionlint", nil }
	const noBrew = "/nonexistent/actionlint"

	if _, res := resolveActionlint(missing, noBrew, true); res != actionlintFatal {
		t.Errorf("missing actionlint under CI resolved to %v, want actionlintFatal: the guard must red, not skip", res)
	}
	if _, res := resolveActionlint(missing, noBrew, false); res != actionlintSkip {
		t.Errorf("missing actionlint locally resolved to %v, want actionlintSkip", res)
	}
	if p, res := resolveActionlint(present, noBrew, true); res != actionlintFound || p != "/usr/bin/actionlint" {
		t.Errorf("present actionlint resolved to (%q, %v), want (/usr/bin/actionlint, actionlintFound)", p, res)
	}
}

// TestActionlint_FeatureMatrix is the emitted-workflow enforcement guard. It
// closes the gap that the e2e scenario corpus leaves open: act (the e2e runner)
// does not enforce every rule real GitHub enforces at parse, so a documented
// manifest feature can emit a workflow GitHub would reject even while every
// scenario stays green.
//
// For each manifest shape in the matrix below the guard runs the full generate
// Plan (the same set the generate command writes) and runs actionlint over every
// emitted workflow. A case is one of two kinds:
//
//   - a supported feature: Plan must succeed and every emitted workflow must be
//     actionlint-clean, because real GitHub must accept it at parse;
//   - an unsupported-by-design feature: Plan must reject it at validation with a
//     clear message, because cascade must reject loudly rather than emit a
//     workflow GitHub rejects.
//
// The invariant the guard enforces is absolute: cascade never emits a workflow
// real GitHub rejects at parse. Either the feature is expressed correctly, or it
// is rejected loudly at validation.
//
// To extend coverage for a new emitted-affecting manifest field, add a row to
// featureMatrixCases with a manifest exercising it. That is the whole contract:
// any feature that changes emitted YAML belongs in this matrix.
func TestActionlint_FeatureMatrix(t *testing.T) {
	bin := locateActionlint(t)
	for _, tc := range featureMatrixCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := stageMatrixProject(t, tc)
			cfgPath := filepath.Join(dir, "ci.yaml")

			planned, err := Plan(PlanOptions{ConfigPath: cfgPath})

			if tc.rejectSubstr != "" {
				if err == nil {
					// The feature is unsupported by design, but Plan produced
					// output. Lint it so the failure quotes the invalid YAML the
					// guard exists to catch.
					out, _ := writeAndLintWorkflows(t, bin, dir, planned)
					t.Fatalf("expected Plan to reject %q with %q, but it succeeded; "+
						"actionlint over the emitted output (real GitHub would reject this at parse):\n%s",
						tc.name, tc.rejectSubstr, out)
				}
				if !strings.Contains(err.Error(), tc.rejectSubstr) {
					t.Fatalf("Plan rejected %q with %v, want a message containing %q", tc.name, err, tc.rejectSubstr)
				}
				return
			}

			if err != nil {
				t.Fatalf("Plan failed for supported feature %q: %v", tc.name, err)
			}
			out, runErr := writeAndLintWorkflows(t, bin, dir, planned)
			if runErr != nil {
				t.Errorf("actionlint rejected the generated workflows for %q "+
					"(real GitHub would reject these at parse):\n%s", tc.name, out)
			}
		})
	}
}

// featureMatrixCase is one manifest shape and the emitted-output expectation the
// guard enforces for it.
type featureMatrixCase struct {
	name     string
	manifest string
	// stubs maps a reusable-workflow filename (build.yaml, deploy.yaml) to its
	// full on-disk content. The generators parse these to discover declared
	// inputs/outputs and actionlint resolves the emitted uses: references against
	// them, so a stub that omits an input a callback passes reproduces exactly
	// what real GitHub rejects.
	stubs map[string]string
	// rejectSubstr, when non-empty, marks the feature unsupported by design: Plan
	// must fail validation with a message containing this substring.
	rejectSubstr string
}

// callbackStub renders a reusable-workflow stub declaring the given inputs. A
// callback the generator threads an input into must declare that input or real
// GitHub rejects the call; the guard's stubs model the callback contract
// (environment/sha/dry_run) so an emitted with: that passes an undeclared input
// surfaces as an actionlint error.
func callbackStub(inputs ...string) string {
	var b strings.Builder
	b.WriteString("on:\n  workflow_call:\n    inputs:\n")
	for _, in := range inputs {
		typ := "string"
		if in == "dry_run" {
			typ = "boolean"
		}
		fmt.Fprintf(&b, "      %s:\n        type: %s\n        required: false\n", in, typ)
	}
	// A minimal jobs: section makes the stub itself a valid reusable workflow, so
	// linting the whole workflows directory does not flag the callee stubs. The
	// generated caller workflows are what the guard is actually validating.
	b.WriteString("jobs:\n  noop:\n    runs-on: ubuntu-latest\n    steps:\n      - run: \"true\"\n")
	return b.String()
}

// contractStubs returns the standard callback-contract stub set: build.yaml and
// deploy.yaml each declaring environment, sha, and dry_run (the inputs the
// framework threads). It deliberately does not declare target_env, so a hotfix
// build callback that passes an undeclared target_env is caught.
func contractStubs() map[string]string {
	stub := callbackStub("environment", "sha", "dry_run")
	return map[string]string{"build.yaml": stub, "deploy.yaml": stub}
}

// stageMatrixProject builds a self-contained project tree (a .git anchor so
// actionlint resolves local reusable-workflow references, the callback stubs, and
// the manifest) and returns the project directory.
func stageMatrixProject(t *testing.T, tc featureMatrixCase) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll .git anchor: %v", err)
	}
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("MkdirAll workflows: %v", err)
	}
	for name, content := range tc.stubs {
		if err := os.WriteFile(filepath.Join(wfDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile stub %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yaml"), []byte(tc.manifest), 0o644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
	return dir
}

// writeAndLintWorkflows writes every planned workflow file into the staged tree
// and runs actionlint over the whole .github/workflows directory in a single
// invocation, so cross-file reusable-workflow references resolve. It returns the
// combined actionlint output and its error.
func writeAndLintWorkflows(t *testing.T, bin, dir string, planned []PlannedFile) (string, error) {
	t.Helper()
	wfDir := filepath.Join(dir, ".github", "workflows")
	for _, pf := range planned {
		if !strings.HasSuffix(pf.Path, ".yaml") && !strings.HasSuffix(pf.Path, ".yml") {
			continue
		}
		target := pf.Path
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, pf.Path)
		}
		// Only workflow files under .github/workflows are lintable workflows; the
		// composite action lives under .github/actions and is not a workflow.
		if !strings.Contains(filepath.ToSlash(target), "/.github/workflows/") {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, []byte(pf.Content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", target, err)
		}
	}

	entries, err := filepath.Glob(filepath.Join(wfDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob workflows: %v", err)
	}
	sort.Strings(entries)
	// -shellcheck= / -pyflakes= keep the result independent of whether those
	// external linters are installed (see runActionlint); the guard governs
	// workflow structure and uses: reference validity, not run: script style.
	args := append([]string{"-shellcheck=", "-pyflakes=", "-no-color"}, entries...)
	out, runErr := exec.Command(bin, args...).CombinedOutput()
	return string(out), runErr
}

// sweepStubNames are the reusable-workflow callee stubs the census sweep stages.
// Every callback path a registry mutator can point at
// (build/deploy/validate/publish/release/changelog) is present.
var sweepStubNames = []string{"build.yaml", "deploy.yaml", "validate.yaml", "publish.yaml", "release.yaml", "changelog.yaml"}

// sweepCalleeStub is a contract-complete reusable-workflow callee for the census
// sweep. It declares every input, output, and secret a generator threads under
// the documented callback contract (the standard environment/sha/dry_run; the
// changelog contract's changelog_base_sha/head_sha/repo; the operator/matrix
// input keys the field batteries plant; the dependency outputs a dependent
// consumes; the battery secret names), so a compliant callee accepts what the
// generator emits.
//
// It deliberately does NOT declare target_env: that input is not part of any
// callback contract (it was the GM3 defect), so a regression that reintroduces
// an ungated undeclared input still reds this sweep.
func sweepCalleeStub() string {
	inputs := []string{
		"environment", "sha", "dry_run",
		"changelog_base_sha", "head_sha", "repo",
		"cluster", "go-version", "goarch",
	}
	var b strings.Builder
	b.WriteString("on:\n  workflow_call:\n    inputs:\n")
	for _, in := range inputs {
		typ := "string"
		if in == "dry_run" {
			typ = "boolean"
		}
		fmt.Fprintf(&b, "      %s:\n        type: %s\n        required: false\n", in, typ)
	}
	b.WriteString("    outputs:\n")
	for _, out := range []string{"tag", "changelog", "artifact_id", "image", "bundle"} {
		fmt.Fprintf(&b, "      %s:\n        value: \"x\"\n", out)
	}
	b.WriteString("    secrets:\n")
	for _, s := range []string{"MY_SECRET_1", "GOOD_IN", "GOOD_OUT"} {
		fmt.Fprintf(&b, "      %s:\n        required: false\n", s)
	}
	b.WriteString("jobs:\n  noop:\n    runs-on: ubuntu-latest\n    steps:\n      - run: \"true\"\n")
	return b.String()
}

// planAndLintConfig marshals cfg into a manifest, stages the callee stubs, runs
// the full generate Plan, and runs actionlint over every emitted workflow. A Plan
// failure is fatal (the manifest cannot generate at all); the returned (output,
// error) pair is the actionlint result, where a non-nil error means real GitHub
// would reject the emitted workflows at parse.
func planAndLintConfig(t *testing.T, bin string, cfg *config.TrunkConfig) (string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll .git anchor: %v", err)
	}
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("MkdirAll workflows: %v", err)
	}
	stub := sweepCalleeStub()
	for _, name := range sweepStubNames {
		if err := os.WriteFile(filepath.Join(wfDir, name), []byte(stub), 0o644); err != nil {
			t.Fatalf("WriteFile stub %s: %v", name, err)
		}
	}

	body, err := yaml.Marshal(map[string]any{config.DefaultManifestKey: config.CICDFile{Config: cfg}})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yaml"), body, 0o644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}

	planned, err := Plan(PlanOptions{ConfigPath: filepath.Join(dir, "ci.yaml")})
	if err != nil {
		t.Fatalf("generating the full workflow set failed: %v", err)
	}
	return writeAndLintWorkflows(t, bin, dir, planned)
}

// TestActionlint_EmittedFieldRegistrySweep is the census-forced arm of the
// guard. It drives actionlint directly off the T1 emitted-field census: for every
// entry in emittedFieldRegistry (the registry that reds CI when a new
// emitted-affecting manifest field appears unclassified), it plants that field's
// good value on the base manifest, generates the full workflow set, and lints it.
//
// This is what forces the NEXT emitted-affecting field into actionlint coverage
// automatically: TestEmittedFieldRegistry_EveryFieldClassified already fails CI
// until a new spliced field is added to emittedFieldRegistry, and once it is
// there this sweep lints its emitted output with no new code. A future field that
// emits an input its callee never declared (the GM3 class) reds here the moment
// it is registered, rather than only in production.
func TestActionlint_EmittedFieldRegistrySweep(t *testing.T) {
	bin := locateActionlint(t)

	paths := make([]string, 0, len(emittedFieldRegistry))
	for p := range emittedFieldRegistry {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		p := p
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			entry := emittedFieldRegistry[p]
			good := entry.good
			if good == "" {
				good = shapeBatteries[entry.shape].good
			}
			cfg := guardBaseConfig()
			entry.set(cfg, good)
			if errs := config.Validate(cfg); len(errs) > 0 {
				t.Fatalf("registry good value for %s does not validate: %v", p, errs)
			}
			out, lintErr := planAndLintConfig(t, bin, cfg)
			if lintErr != nil {
				t.Errorf("actionlint rejected the generated workflows when field %s is set "+
					"(real GitHub would reject these at parse):\n%s", p, out)
			}
		})
	}
}

// featureMatrixCases enumerates the emitted-affecting manifest surface. Each case
// exercises a documented feature or combination whose emitted YAML must survive
// real GitHub's parse (or be rejected loudly at validation).
func featureMatrixCases() []featureMatrixCase {
	// wrap nests a config body under the ci/config manifest envelope, indenting
	// every non-empty line by four spaces so it sits under ci.config.
	wrap := func(body string) string {
		var b strings.Builder
		b.WriteString("ci:\n  config:\n")
		for _, line := range strings.Split(body, "\n") {
			if line == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString("    ")
			b.WriteString(line)
			b.WriteString("\n")
		}
		return b.String()
	}

	const base = `trunk_branch: main
environments: [dev, prod]
`
	buildDeploy := base + `builds:
  - name: app
    workflow: build.yaml
    triggers: ["src/**"]
deploys:
  - name: cdk
    workflow: deploy.yaml
    triggers: ["cdk/**"]
`
	return []featureMatrixCase{
		{
			name:     "on_failure_abort",
			manifest: wrap(buildDeploy + `    on_failure: abort` + "\n"),
			stubs:    contractStubs(),
		},
		{
			// GB1: on_failure: continue emits continue-on-error on a
			// reusable-workflow-call job, which real GitHub rejects at parse. It is
			// not expressible for such a job, so cascade must reject it at
			// validation.
			name: "on_failure_continue_rejected",
			manifest: wrap(base + `builds:
  - name: app
    workflow: build.yaml
    triggers: ["src/**"]
    on_failure: continue
deploys:
  - name: cdk
    workflow: deploy.yaml
    triggers: ["cdk/**"]
`),
			stubs:        contractStubs(),
			rejectSubstr: "on_failure: continue is not supported",
		},
		{
			// GM3: the hotfix build callback must not pass inputs the reusable
			// workflow does not declare (target_env is not part of the contract
			// stub). The hotfix workflow is emitted for every manifest with a build.
			name:     "hotfix_reusable_build_inputs",
			manifest: wrap(buildDeploy),
			stubs:    contractStubs(),
		},
		{
			name: "build_matrix_inputs",
			manifest: wrap(base + `builds:
  - name: app
    workflow: build.yaml
    triggers: ["src/**"]
    matrix:
      dimensions:
        goarch: ["amd64", "arm64"]
      max_parallel: 2
      fail_fast: false
deploys:
  - name: cdk
    workflow: deploy.yaml
    triggers: ["cdk/**"]
`),
			stubs: map[string]string{
				"build.yaml":  callbackStub("environment", "sha", "dry_run", "goarch"),
				"deploy.yaml": callbackStub("environment", "sha", "dry_run"),
			},
		},
		{
			name: "deployments_native",
			manifest: wrap(base + `deployments:
  enabled: true
builds:
  - name: app
    workflow: build.yaml
    triggers: ["src/**"]
deploys:
  - name: cdk
    workflow: deploy.yaml
    triggers: ["cdk/**"]
`),
			stubs: contractStubs(),
		},
		{
			name: "rollback_repository_dispatch",
			manifest: wrap(buildDeploy + `rollback:
  repository_dispatch:
    types: [rollback-requested]
`),
			stubs: contractStubs(),
		},
		{
			name: "retries",
			manifest: wrap(base + `builds:
  - name: app
    workflow: build.yaml
    triggers: ["src/**"]
    retries: 2
deploys:
  - name: cdk
    workflow: deploy.yaml
    triggers: ["cdk/**"]
`),
			stubs: contractStubs(),
		},
		{
			// Coverage-gap closer: the "retries" case above never exercises a
			// dependent deploy of a retried job, so it could not have caught the
			// defect where a dependent's needs: omitted the retry shims its own
			// if: gate referenced (a shim outside needs: is unresolvable, both at
			// actionlint parse time and at GHA runtime). This case pairs retries
			// with depends_on so that combination is under permanent guard.
			name: "retries_dependent_deploy",
			manifest: wrap(base + `deploys:
  - name: app
    workflow: deploy.yaml
    triggers: ["src/**"]
    retries: 1
  - name: notify
    workflow: deploy.yaml
    triggers: ["notify/**"]
    depends_on: ["deploy:app"]
`),
			stubs: contractStubs(),
		},
		{
			name: "cancel_in_progress_true",
			manifest: wrap(buildDeploy + `concurrency:
  group: orchestrate-${{ github.ref }}
  cancel_in_progress: true
`),
			stubs: contractStubs(),
		},
		{
			name: "cancel_in_progress_false",
			manifest: wrap(buildDeploy + `concurrency:
  group: orchestrate-${{ github.ref }}
  cancel_in_progress: false
`),
			stubs: contractStubs(),
		},
		{
			name: "component_scoped",
			manifest: wrap(buildDeploy + `components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-
  web:
    path: services/web
    tag_grammar:
      prefix: web-
`),
			stubs: contractStubs(),
		},
	}
}

// emittedAffectingMarkers are the reason substrings that classify a
// notEmittedAllowlist entry as genuinely not spliced into emitted output. An
// allowlist reason carrying any of these is exempt from the actionlint sweep;
// an entry whose reason carries none is emitted-affecting and must have a
// mutator in emittedAffectingAllowlistMutators so its generated output is
// linted. This slice is the single source both the classification test and the
// human reviewer read.
var emittedAffectingMarkers = []string{
	"rejected",
	"reserved",
	"not consumed",
	"never emitted",
	"documented validated-only",
}

// withProdEnvConfig replaces the environments ladder with dev plus a prod entry
// carrying ec, and enables native deployments so the environment-provisioning
// fields (gha_environment, branch_policy, patterns, reviewers) are exercised
// against a manifest that provisions native GitHub Environments.
func withProdEnvConfig(cfg *config.TrunkConfig, ec config.EnvironmentConfig) {
	cfg.Deployments = &config.DeploymentsConfig{Enabled: boolPtr(true)}
	cfg.Environments = []config.EnvironmentEntry{
		{Name: "dev"},
		{Name: "prod", EnvironmentConfig: ec},
	}
}

// baseExternalRepo returns a valid single external satellite deploy that
// generates an actionlint-clean external-update workflow, ready for a mutator to
// extend one deploy field. The workflow is an external org/repo path@ref that
// actionlint skips as unresolvable, matching the emitted uses: reference.
func baseExternalRepo() config.ExternalRepoConfig {
	return config.ExternalRepoConfig{
		Repo: "org/satellite",
		Ref:  "main",
		Deploys: []config.ExternalDeployConfig{{
			Name:     "ext-app",
			Workflow: "org/satellite/.github/workflows/deploy.yaml@v1",
		}},
	}
}

// ensureValidateBlock returns cfg's validate callback, planting a minimal valid
// one (pointing at the validate.yaml callee stub) when absent, so validate.*
// mutators can set a single field on a generatable validate block.
func ensureValidateBlock(cfg *config.TrunkConfig) *config.ValidateConfig {
	if cfg.Validate == nil {
		cfg.Validate = &config.ValidateConfig{Workflow: "validate.yaml", Triggers: []string{"src/**"}}
	}
	return cfg.Validate
}

// emittedAffectingAllowlistMutators plants a valid value for every
// notEmittedAllowlist field that DOES change generated workflow structure (the
// entries whose reason carries no emittedAffectingMarkers marker). Each mutator
// mutates a fresh guardBaseConfig in place with a value that passes
// config.Validate and generates an actionlint-clean full workflow set, forcing
// the field through the emitted-workflow guard.
// TestActionlint_AllowlistFieldsClassified keeps this map in lockstep with
// notEmittedAllowlist: a new emitted-affecting allowlist entry with no mutator
// here fails that test.
var emittedAffectingAllowlistMutators = map[string]func(*config.TrunkConfig){
	"action_pins[key]": func(c *config.TrunkConfig) {
		c.ActionPins = map[string]string{"actions/checkout": "0123abcd # v4"}
	},

	// depends_on / optional_depends_on carry a declared-callback reference the
	// generator turns into a needs: edge on the derived job ID. A build may only
	// depend on a build, so plant a second build and point at it; a deploy may
	// depend on the base build directly.
	"builds[].depends_on[]": func(c *config.TrunkConfig) {
		c.Builds = append(c.Builds, config.BuildConfig{Name: "dep", Workflow: "build.yaml", Triggers: []string{"dep/**"}})
		c.Builds[0].DependsOn = []string{"build:dep"}
	},
	"builds[].optional_depends_on[]": func(c *config.TrunkConfig) {
		c.Builds = append(c.Builds, config.BuildConfig{Name: "dep", Workflow: "build.yaml", Triggers: []string{"dep/**"}})
		c.Builds[0].OptionalDependsOn = []string{"build:dep"}
	},
	// env_inputs keys reference a declared environment (dev); the routed input
	// key (cluster) is one the callee stub declares.
	"builds[].env_inputs[key]": func(c *config.TrunkConfig) {
		c.Builds[0].EnvInputs = map[string]map[string]interface{}{"dev": {"cluster": "x"}}
	},
	"builds[].env_inputs.*": func(c *config.TrunkConfig) {
		c.Builds[0].EnvInputs = map[string]map[string]interface{}{"dev": {"cluster": "x"}}
	},
	// matrix.dimensions keys become strategy.matrix keys and ${{ matrix.<k> }}
	// derefs; goarch is declared by the callee stub.
	"builds[].matrix.dimensions.*": func(c *config.TrunkConfig) {
		c.Builds[0].Matrix = &config.MatrixConfig{Dimensions: map[string][]string{"goarch": {"amd64", "arm64"}}}
	},
	// on_failure only supports abort on a reusable-workflow call; continue is
	// rejected at validation, so plant the supported value.
	"builds[].on_failure": func(c *config.TrunkConfig) {
		c.Builds[0].OnFailure = config.OnFailureAbort
	},
	"builds[].run_policy": func(c *config.TrunkConfig) {
		c.Builds[0].RunPolicy = config.RunPolicyAlways
	},
	"builds[].permissions[key]": func(c *config.TrunkConfig) {
		c.Builds[0].Permissions = map[string]string{"contents": "read"}
	},
	"builds[].permissions.*": func(c *config.TrunkConfig) {
		c.Builds[0].Permissions = map[string]string{"contents": "read"}
	},

	"deploys[].depends_on[]": func(c *config.TrunkConfig) {
		c.Deploys[0].DependsOn = []string{"build:app"}
	},
	"deploys[].optional_depends_on[]": func(c *config.TrunkConfig) {
		c.Deploys[0].OptionalDependsOn = []string{"build:app"}
	},
	"deploys[].env_inputs[key]": func(c *config.TrunkConfig) {
		c.Deploys[0].EnvInputs = map[string]map[string]interface{}{"dev": {"cluster": "x"}}
	},
	"deploys[].env_inputs.*": func(c *config.TrunkConfig) {
		c.Deploys[0].EnvInputs = map[string]map[string]interface{}{"dev": {"cluster": "x"}}
	},
	"deploys[].on_failure": func(c *config.TrunkConfig) {
		c.Deploys[0].OnFailure = config.OnFailureAbort
	},
	"deploys[].run_policy": func(c *config.TrunkConfig) {
		c.Deploys[0].RunPolicy = config.RunPolicyAlways
	},
	"deploys[].permissions[key]": func(c *config.TrunkConfig) {
		c.Deploys[0].Permissions = map[string]string{"contents": "read"}
	},
	"deploys[].permissions.*": func(c *config.TrunkConfig) {
		c.Deploys[0].Permissions = map[string]string{"contents": "read"}
	},

	"dispatch_inputs.*.description": func(c *config.TrunkConfig) {
		c.DispatchInputs = map[string]config.DispatchInput{"note": {Type: config.DispatchInputTypeString, Description: "operator help text"}}
	},
	"dispatch_inputs.*.type": func(c *config.TrunkConfig) {
		c.DispatchInputs = map[string]config.DispatchInput{"mode": {Type: config.DispatchInputTypeString}}
	},

	// role is a promotion-stage selector; dev/prod match the positional default
	// (prerelease/release) so the explicit roles introduce no conflict.
	"environments[].role": func(c *config.TrunkConfig) {
		c.Environments = []config.EnvironmentEntry{
			{Name: "dev", Role: config.EnvRolePrerelease},
			{Name: "prod", Role: config.EnvRoleRelease},
		}
	},
	"environments[].branch_policy": func(c *config.TrunkConfig) {
		withProdEnvConfig(c, config.EnvironmentConfig{BranchPolicy: config.EnvBranchPolicyAll})
	},
	"environments[].gha_environment": func(c *config.TrunkConfig) {
		withProdEnvConfig(c, config.EnvironmentConfig{GHAEnvironment: "production"})
	},
	// branch_patterns / tag_patterns are only valid under branch_policy: custom.
	"environments[].branch_patterns[]": func(c *config.TrunkConfig) {
		withProdEnvConfig(c, config.EnvironmentConfig{BranchPolicy: config.EnvBranchPolicyCustom, BranchPatterns: []string{"main"}})
	},
	"environments[].tag_patterns[]": func(c *config.TrunkConfig) {
		withProdEnvConfig(c, config.EnvironmentConfig{BranchPolicy: config.EnvBranchPolicyCustom, TagPatterns: []string{"v*"}})
	},
	"environments[].required_reviewers[]": func(c *config.TrunkConfig) {
		withProdEnvConfig(c, config.EnvironmentConfig{RequiredReviewers: []string{"octocat"}})
	},

	"external[].repo": func(c *config.TrunkConfig) {
		c.External = []config.ExternalRepoConfig{baseExternalRepo()}
	},
	"external[].deploys[].optional_depends_on[]": func(c *config.TrunkConfig) {
		ext := baseExternalRepo()
		ext.Deploys[0].OptionalDependsOn = []string{"build:app"}
		c.External = []config.ExternalRepoConfig{ext}
	},
	"external[].deploys[].permissions[key]": func(c *config.TrunkConfig) {
		ext := baseExternalRepo()
		ext.Deploys[0].Permissions = map[string]string{"contents": "read"}
		c.External = []config.ExternalRepoConfig{ext}
	},
	"external[].deploys[].permissions.*": func(c *config.TrunkConfig) {
		ext := baseExternalRepo()
		ext.Deploys[0].Permissions = map[string]string{"contents": "read"}
		c.External = []config.ExternalRepoConfig{ext}
	},

	"git.mode": func(c *config.TrunkConfig) {
		c.Git = &config.GitConfig{Mode: config.GitModeCustom, UserName: "Release Bot", UserEmail: "bot@example.com"}
	},

	"pin_mode": func(c *config.TrunkConfig) {
		c.PinMode = config.PinModeTag
	},
	"release_trigger": func(c *config.TrunkConfig) {
		c.ReleaseTrigger = config.ReleaseTriggerPush
	},
	// reconcile is enabled so the emitted pin-reconcile companion generates and
	// is linted; source/commit each set their own recognized adapter value.
	"reconcile.source": func(c *config.TrunkConfig) {
		c.Reconcile = &config.ReconcileConfig{Enabled: true, Source: config.ReconcileSourceDependabot}
	},
	"reconcile.commit": func(c *config.TrunkConfig) {
		c.Reconcile = &config.ReconcileConfig{Enabled: true, Commit: config.ReconcileCommitAppend}
	},

	"validate.env_inputs[key]": func(c *config.TrunkConfig) {
		ensureValidateBlock(c).EnvInputs = map[string]map[string]interface{}{"dev": {"cluster": "x"}}
	},
	"validate.env_inputs.*": func(c *config.TrunkConfig) {
		ensureValidateBlock(c).EnvInputs = map[string]map[string]interface{}{"dev": {"cluster": "x"}}
	},
	"validate.on_failure": func(c *config.TrunkConfig) {
		ensureValidateBlock(c).OnFailure = config.OnFailureAbort
	},
	"validate.run_policy": func(c *config.TrunkConfig) {
		ensureValidateBlock(c).RunPolicy = config.RunPolicyAlways
	},
	"validate.permissions[key]": func(c *config.TrunkConfig) {
		ensureValidateBlock(c).Permissions = map[string]string{"contents": "read"}
	},
	"validate.permissions.*": func(c *config.TrunkConfig) {
		ensureValidateBlock(c).Permissions = map[string]string{"contents": "read"}
	},
}

// TestActionlint_EmittedAffectingAllowlistSweep drives actionlint over the
// emitted-affecting allowlist fields: the notEmittedAllowlist entries whose
// reason carries no not-emitted marker. Each of these changes generated
// workflow structure but was previously outside actionlint coverage. For every
// mutator it plants the field on the base manifest, asserts config.Validate is
// clean, generates the full workflow set, and fails on any actionlint error
// (which means real GitHub would reject the emitted workflows at parse). It
// mirrors TestActionlint_EmittedFieldRegistrySweep.
func TestActionlint_EmittedAffectingAllowlistSweep(t *testing.T) {
	bin := locateActionlint(t)

	paths := make([]string, 0, len(emittedAffectingAllowlistMutators))
	for p := range emittedAffectingAllowlistMutators {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		p := p
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			cfg := guardBaseConfig()
			emittedAffectingAllowlistMutators[p](cfg)
			if errs := config.Validate(cfg); len(errs) > 0 {
				t.Fatalf("mutator for %s produced a config that does not validate: %v", p, errs)
			}
			out, lintErr := planAndLintConfig(t, bin, cfg)
			if lintErr != nil {
				t.Errorf("actionlint rejected the generated workflows when allowlist field %s is set "+
					"(real GitHub would reject these at parse):\n%s", p, out)
			}
		})
	}
}

// TestActionlint_AllowlistFieldsClassified is the forcing test that keeps the
// emitted-affecting allowlist in actionlint coverage. Every notEmittedAllowlist
// field is one of two kinds: genuinely not emitted (its reason carries an
// emittedAffectingMarkers marker), in which case it needs no mutator; or
// emitted-affecting (its reason carries no marker), in which case it MUST have
// an emittedAffectingAllowlistMutators entry so its generated output is swept.
// A new emitted-affecting allowlist entry with no mutator fails here, forcing
// the author to add a mutator or record a not-emitted reason. It also fails on a
// stale mutator (no matching allowlist entry) and on a double-classification (a
// field both marker-exempt and given a mutator).
func TestActionlint_AllowlistFieldsClassified(t *testing.T) {
	isNotEmitted := func(reason string) bool {
		for _, m := range emittedAffectingMarkers {
			if strings.Contains(reason, m) {
				return true
			}
		}
		return false
	}

	var missing, doubled []string
	for path, reason := range notEmittedAllowlist {
		_, hasMutator := emittedAffectingAllowlistMutators[path]
		if isNotEmitted(reason) {
			if hasMutator {
				doubled = append(doubled, path)
			}
			continue
		}
		if !hasMutator {
			missing = append(missing, path)
		}
	}

	var stale []string
	for path := range emittedAffectingAllowlistMutators {
		if _, ok := notEmittedAllowlist[path]; !ok {
			stale = append(stale, path)
		}
	}

	sort.Strings(missing)
	sort.Strings(doubled)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Errorf("emitted-affecting allowlist fields with no actionlint mutator (add each to "+
			"emittedAffectingAllowlistMutators so its generated output is swept, or give it a not-emitted "+
			"reason carrying one of %v):\n  %s", emittedAffectingMarkers, strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("emittedAffectingAllowlistMutators entries absent from notEmittedAllowlist (remove them):\n  %s",
			strings.Join(stale, "\n  "))
	}
	if len(doubled) > 0 {
		t.Errorf("allowlist fields both marked not-emitted and given an emitted-affecting mutator "+
			"(a field is one or the other):\n  %s", strings.Join(doubled, "\n  "))
	}
}

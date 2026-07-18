package generate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

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

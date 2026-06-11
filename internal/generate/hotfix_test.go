package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/hotfix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// threeEnvHotfixConfig returns a 3-environment manifest config suitable for
// exercising the hotfix generator. The first env ("dev") is the build target and
// is excluded from the hotfix target choices; "test" and "prod" are targets.
func threeEnvHotfixConfig() *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "test", "prod"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: ".github/workflows/build.yaml"},
		},
		Deploys: []config.DeployConfig{
			{Name: "service", Workflow: ".github/workflows/deploy.yaml"},
		},
	}
}

func TestHotfixGenerator_Enabled(t *testing.T) {
	// Two or more environments enables the hotfix workflow.
	assert.True(t, NewHotfixGenerator(&config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
	}, "").Enabled(), "2 envs should enable the hotfix workflow")

	assert.True(t, NewHotfixGenerator(threeEnvHotfixConfig(), "").Enabled(), "3 envs should enable")

	// Below two environments emits nothing.
	assert.False(t, NewHotfixGenerator(&config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
	}, "").Enabled(), "1 env should not enable")

	assert.False(t, NewHotfixGenerator(&config.TrunkConfig{
		TrunkBranch: "main",
	}, "").Enabled(), "0 envs should not enable")

	// Nil config reports disabled rather than panicking.
	assert.False(t, NewHotfixGenerator(nil, "").Enabled(), "nil config should not enable")
}

// TestHotfixGenerator_Threshold_EmitsNothingBelowTwoEnvs confirms the Q1
// generation threshold: with a single env the generator gate is closed.
func TestHotfixGenerator_Threshold_EmitsNothingBelowTwoEnvs(t *testing.T) {
	oneEnv := &config.TrunkConfig{TrunkBranch: "main", Environments: []string{"dev"}}
	assert.False(t, NewHotfixGenerator(oneEnv, "").Enabled())

	zeroEnv := &config.TrunkConfig{TrunkBranch: "main"}
	assert.False(t, NewHotfixGenerator(zeroEnv, "").Enabled())
}

func TestHotfixGenerator_Triggers(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "name: Cascade Hotfix")
	assert.Contains(t, content, "workflow_dispatch:")
	assert.Contains(t, content, "pull_request:")
	assert.Contains(t, content, "types: [closed]")
	assert.Contains(t, content, "branches:")
	assert.Contains(t, content, "'env/*'")

	// Dispatch inputs.
	assert.Contains(t, content, "commit:")
	assert.Contains(t, content, "target_env:")
	assert.Contains(t, content, "pr_number:")
	assert.Contains(t, content, "dry_run:")

	// target_env choice options list non-first envs, not the build target.
	assert.Contains(t, content, "- test")
	assert.Contains(t, content, "- prod")
	assert.NotContains(t, content, "- dev")
}

func TestHotfixGenerator_Concurrency(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)
	assert.Contains(t, content, "group: hotfix-")
	assert.Contains(t, content, "cancel-in-progress: false")
}

func TestHotfixGenerator_Permissions(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)
	assert.Contains(t, content, "contents: write")
	assert.Contains(t, content, "pull-requests: write")
	assert.Contains(t, content, "actions: read")
}

func TestHotfixGenerator_Jobs(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "  plan:")
	assert.Contains(t, content, "  apply:")
	assert.Contains(t, content, "  check:")
	assert.Contains(t, content, "  finalize:")
	// Build and deploy jobs are emitted per configured callback.
	assert.Contains(t, content, "build-app:")
	assert.Contains(t, content, "deploy-service:")

	// plan runs cascade hotfix plan; finalize runs cascade hotfix finalize.
	assert.Contains(t, content, "cascade hotfix plan")
	assert.Contains(t, content, "cascade hotfix finalize")

	// check job runs the parse-config validity gate.
	assert.Contains(t, content, "cascade parse-config")
}

func TestHotfixGenerator_ConflictPath(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "cascade-hotfix-conflict")
	assert.Contains(t, content, "--force-with-lease")
	assert.Contains(t, content, "Cascade-Hotfix-Target:")
	assert.Contains(t, content, "Cascade-Hotfix-Source:")
	assert.Contains(t, content, "Cascade-Hotfix-Base:")
}

func TestHotfixGenerator_CleanPath(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "--label cascade-hotfix")
	assert.Contains(t, content, "gh pr merge --auto")
}

func TestHotfixGenerator_Q2BranchProtectionWarn(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// gh api branch-protection call (slash URL-encoded as %2F) plus a loud warning.
	assert.Contains(t, content, "branches/env")
	assert.Contains(t, content, "protection")
	assert.Contains(t, content, "::warning::")
}

func TestHotfixGenerator_Q6ProtectionSuggestions(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)
	assert.Contains(t, content, "protection_suggestions")
}

func TestHotfixGenerator_ProdGatingEnvironment(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)
	// Deploy job must carry an environment: key for org protection gating.
	assert.Contains(t, content, "environment:")
}

func TestHotfixGenerator_DryRunSafety(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// apply job is skipped on dry-run; plan forwards --dry-run.
	assert.Contains(t, content, "dry_run != 'true'")
	assert.Contains(t, content, "--dry-run")
}

func TestHotfixGenerator_MergedLabelGate(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "github.event.pull_request.merged == true")
	assert.Contains(t, content, "'cascade-hotfix')")
}

func TestHotfixGenerator_ValidYAML(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(content), &parsed), "emitted workflow must be valid YAML")
	assert.Contains(t, parsed, "jobs")
	assert.Contains(t, parsed, "on")
	assert.Contains(t, parsed, "permissions")
}

// TestHotfixGenerator_PinModeSHA confirms third-party action refs route through
// the shared pin helper rather than emitting a raw @v4.
func TestHotfixGenerator_PinModeSHA(t *testing.T) {
	cfg := threeEnvHotfixConfig()
	cfg.PinMode = config.PinModeSHA
	gen := NewHotfixGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "uses: actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10")
	assert.NotContains(t, content, "uses: actions/checkout@v4")
}

// TestHotfixGeneratorE2E exercises the manifest -> parse -> generate path: a
// 3-env manifest enables the hotfix workflow; a single-env manifest disables it.
func TestHotfixGeneratorE2E(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")

	manifest := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - test
      - prod
`
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0644))

	cfg, err := config.ParseWithKey(manifestPath, "ci")
	require.NoError(t, err)

	gen := NewHotfixGenerator(cfg, tmpDir)
	require.True(t, gen.Enabled(), "3-env manifest should enable the hotfix workflow")
	content, err := gen.Generate()
	require.NoError(t, err)
	assert.Contains(t, content, "name: Cascade Hotfix")
	assert.Contains(t, content, "cascade hotfix plan")
	assert.Contains(t, content, "- test")
	assert.Contains(t, content, "- prod")

	// Single-env manifest reports disabled: nothing is emitted.
	single := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
`
	require.NoError(t, os.WriteFile(manifestPath, []byte(single), 0644))
	singleCfg, err := config.ParseWithKey(manifestPath, "ci")
	require.NoError(t, err)
	assert.False(t, NewHotfixGenerator(singleCfg, tmpDir).Enabled(), "single-env manifest emits nothing")
}

// hotfixFlagInvocation captures a single `cascade hotfix <subcommand> --flag`
// pairing parsed from the generated workflow, for cross-checking against the
// real cobra command tree.
type hotfixFlagInvocation struct {
	subcommand string
	flag       string
	line       string
}

// parseHotfixInvocations scans the generated workflow for every
// `cascade hotfix <subcommand> ...` invocation and the long-form flags it
// passes, returning one entry per (subcommand, flag) pair. It handles the
// shell line-continuation style the generator emits (a leading verb line such
// as `cascade hotfix finalize \` followed by `  --flag "..." \` lines).
func parseHotfixInvocations(content string) []hotfixFlagInvocation {
	var (
		invocations []hotfixFlagInvocation
		current     string
	)
	invokeRe := regexp.MustCompile(`cascade hotfix (\S+)`)
	flagRe := regexp.MustCompile(`(?:^|\s)--([a-zA-Z][a-zA-Z0-9-]*)`)

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)

		if m := invokeRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			// A verb line can also carry flags on the same physical line.
			for _, fm := range flagRe.FindAllStringSubmatch(line, -1) {
				invocations = append(invocations, hotfixFlagInvocation{
					subcommand: current,
					flag:       fm[1],
					line:       line,
				})
			}
			continue
		}

		if current == "" {
			continue
		}

		flags := flagRe.FindAllStringSubmatch(line, -1)
		if len(flags) == 0 {
			// A line with no flags that does not continue the command ends it.
			if !strings.HasSuffix(line, `\`) {
				current = ""
			}
			continue
		}
		for _, fm := range flags {
			invocations = append(invocations, hotfixFlagInvocation{
				subcommand: current,
				flag:       fm[1],
				line:       line,
			})
		}
		if !strings.HasSuffix(line, `\`) {
			current = ""
		}
	}
	return invocations
}

// subcommandByName returns the named direct subcommand of root, or nil.
func subcommandByName(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// TestHotfixGenerator_EmittedFlagsExistOnCommands is a regression guard against
// the whole class of "generated workflow passes a flag the CLI does not define"
// bug: for every `cascade hotfix <subcommand> --flag` the generated YAML emits,
// the flag must be a real registered flag on that subcommand's cobra command.
// It builds the command tree from the same constructor the CLI wires up, so the
// check tracks the real flag set rather than a hand-maintained list.
func TestHotfixGenerator_EmittedFlagsExistOnCommands(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	root := hotfix.NewCommand()

	invocations := parseHotfixInvocations(content)
	require.NotEmpty(t, invocations, "expected to parse at least one cascade hotfix invocation")

	// Sanity: the bug-prone subcommands are actually exercised by the workflow.
	seen := map[string]bool{}
	for _, inv := range invocations {
		seen[inv.subcommand] = true
	}
	assert.True(t, seen["plan"], "workflow should invoke cascade hotfix plan")
	assert.True(t, seen["finalize"], "workflow should invoke cascade hotfix finalize")

	for _, inv := range invocations {
		sub := subcommandByName(root, inv.subcommand)
		require.NotNilf(t, sub, "generated workflow invokes unknown subcommand %q (line: %s)", inv.subcommand, inv.line)
		assert.NotNilf(t, sub.Flags().Lookup(inv.flag),
			"generated `cascade hotfix %s` passes --%s, which is not a registered flag on that command (line: %s)",
			inv.subcommand, inv.flag, inv.line)
	}
}

// TestHotfixGenerator_FinalizeRequiredFlags asserts the finalize invocation
// supplies every flag the command marks required, threaded from upstream job
// outputs and the workflow context.
func TestHotfixGenerator_FinalizeRequiredFlags(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	for _, flag := range []string{"--target-env", "--merge-sha", "--fix-sha", "--base-sha"} {
		assert.Contains(t, content, flag,
			"finalize invocation must pass the required %s flag", flag)
	}
	// The retired alias must not reappear.
	assert.NotContains(t, content, "cascade hotfix finalize \\\n            --sha ",
		"finalize must not pass the nonexistent --sha flag")

	// The plan job must expose fix_sha as a job output (the planner emits it),
	// matching the planner's GHA output key.
	assert.Contains(t, content, "fix_sha: ${{ steps.plan.outputs.fix_sha }}",
		"plan job must expose fix_sha as a job output")

	// On the merged-PR finalize path the plan job does not run, so fix-sha and
	// base-sha are recovered by the context job (from the resolution PR-body
	// trailers) and finalize consumes them via needs.context.outputs.
	assert.Contains(t, content, "fix_sha: ${{ steps.ctx.outputs.fix_sha }}",
		"context job must expose fix_sha for finalize to consume")
	assert.Contains(t, content, "needs.context.outputs.fix_sha",
		"finalize must thread fix-sha from the context job output")
	assert.Contains(t, content, "needs.context.outputs.base_sha",
		"finalize must thread base-sha from the context job output")
}

// TestHotfixGenerator_PlanJobOutputsMatchPlannerKeys guards the plan job's
// declared outputs against the GHA output keys the planner actually writes:
// referencing a steps.plan.outputs.<key> that the CLI never sets silently
// yields an empty value at runtime.
func TestHotfixGenerator_PlanJobOutputsMatchPlannerKeys(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	// Keys the planner emits via writePlanGHAOutput.
	refRe := regexp.MustCompile(`steps\.plan\.outputs\.([a-zA-Z0-9_]+)`)
	plannerKeys := map[string]bool{
		"target_env": true, "fix_sha": true, "branch": true, "base_sha": true,
		"no_op": true, "branch_created": true, "hotfix_version_candidate": true,
		"conflict_expected": true, "dry_run": true,
		"protection_suggestions": true, "protection_suggestions_text": true,
	}
	for _, m := range refRe.FindAllStringSubmatch(content, -1) {
		assert.Truef(t, plannerKeys[m[1]],
			"plan job references steps.plan.outputs.%s, which the planner never sets", m[1])
	}
}

// TestHotfixGenerator_Actionlint runs actionlint over the generated workflow
// when the binary is available on PATH. It is skipped otherwise so the unit
// suite stays hermetic.
func TestHotfixGenerator_Actionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}

	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(wfDir, 0755))
	wfPath := filepath.Join(wfDir, "cascade-hotfix.yaml")
	require.NoError(t, os.WriteFile(wfPath, []byte(content), 0644))

	// actionlint resolves local reusable-workflow refs (`uses: ./...`) against the
	// enclosing git repository root, discovered via `git rev-parse --show-toplevel`
	// from the linted file's directory. t.TempDir() can sit inside this repository,
	// which would make actionlint resolve `./.github/workflows/<x>.yaml` against the
	// real repo root rather than the temp dir. Initialize the temp dir as its own
	// git repository so it becomes the project root and resolution stays scoped to
	// the stubs written below.
	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = dir
	require.NoError(t, gitInit.Run(), "git init for actionlint project root")

	// The generated workflow may reference local reusable workflows via
	// `uses: ./.github/workflows/<x>.yaml`. actionlint resolves those `./`-prefixed
	// refs against the filesystem and validates that the referenced workflows are
	// well-formed workflow_call workflows. Write a minimal valid stub for every
	// such reference the generator emits so resolution stays honest (rather than
	// suppressing the workflow-call check) and the test tracks fixture changes.
	writeReusableWorkflowStubs(t, dir, content)

	cmd := exec.Command(bin, wfPath)
	cmd.Dir = dir
	out, runErr := cmd.CombinedOutput()
	assert.NoError(t, runErr, "actionlint reported issues:\n%s", string(out))
}

// reusableWorkflowStub is a minimal valid workflow_call reusable workflow that
// satisfies actionlint's resolution of local `uses: ./...` references. It
// declares the inputs the hotfix generator passes to a reusable build workflow
// (sha, target_env) so actionlint can validate the call site's `with:` block
// against the called workflow's declared inputs under full strictness.
const reusableWorkflowStub = `name: Stub
on:
  workflow_call:
    inputs:
      sha:
        required: false
        type: string
      target_env:
        required: false
        type: string
jobs:
  stub:
    runs-on: ubuntu-latest
    steps:
      - run: 'true'
`

// writeReusableWorkflowStubs scans the generated workflow content for local
// reusable-workflow references of the form `uses: ./.github/workflows/<x>.yaml`
// and writes a minimal valid stub workflow at each referenced path under root,
// so actionlint can resolve and validate every call site.
func writeReusableWorkflowStubs(t *testing.T, root, content string) {
	t.Helper()

	const marker = "uses: ./"
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		idx := strings.Index(trimmed, marker)
		if idx < 0 {
			continue
		}
		ref := strings.Fields(trimmed[idx+len("uses: "):])[0]
		// ref is like "./.github/workflows/build.yaml"; strip the leading "./".
		rel := strings.TrimPrefix(ref, "./")
		stubPath := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(stubPath), 0755))
		require.NoError(t, os.WriteFile(stubPath, []byte(reusableWorkflowStub), 0644))
	}
}

// TestHotfixGenerator_PlanAndFinalizePassConfig guards the regression where the
// plan and finalize invocations ran without --config and resolved the manifest
// from an implicit default that does not exist in the runner. Both CLI calls
// must thread the explicit manifest path so they parse the same config the
// workflow was generated from. The assertions are scoped per job so a --config
// that appears only in one job cannot mask a missing flag in the other.
func TestHotfixGenerator_PlanAndFinalizePassConfig(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	planJob := extractJobSection(t, content, "plan:")
	require.NotEmpty(t, planJob, "plan job section should be present")
	assert.Contains(t, planJob, "cascade hotfix plan",
		"plan job should invoke cascade hotfix plan")
	assert.Contains(t, planJob, "--config ",
		"plan job must pass --config so the verb parses the generated manifest")

	finalizeJob := extractJobSection(t, content, "finalize:")
	require.NotEmpty(t, finalizeJob, "finalize job section should be present")
	assert.Contains(t, finalizeJob, "cascade hotfix finalize",
		"finalize job should invoke cascade hotfix finalize")
	assert.Contains(t, finalizeJob, "--config ",
		"finalize job must pass --config so the verb parses the generated manifest")
}

// TestHotfixGenerator_ApplyMaterializesAbsentEnvBranch guards the first-hotfix
// regression where the apply job branched from origin/env/<env>, a remote ref
// that does not exist until an env branch has been pushed. The plan verb creates
// env/<env> only locally, so the first hotfix into an environment must
// materialize and push it from the validated base SHA before cherry-picking.
func TestHotfixGenerator_ApplyMaterializesAbsentEnvBranch(t *testing.T) {
	gen := NewHotfixGenerator(threeEnvHotfixConfig(), "")
	content, err := gen.Generate()
	require.NoError(t, err)

	applyJob := extractJobSection(t, content, "apply:")
	require.NotEmpty(t, applyJob, "apply job section should be present")

	// The hotfix branch must be cut from the plan's validated base SHA, not from
	// a remote-tracking ref that may not exist on a first hotfix.
	assert.Contains(t, applyJob, `git switch -c "$BRANCH" "$BASE_SHA"`,
		"apply job must branch the hotfix from the validated BASE_SHA")
	assert.NotContains(t, applyJob, `git switch -c "$BRANCH" "origin/env/${TARGET_ENV}"`,
		"apply job must not branch from origin/env/<env>, which is absent on a first hotfix")

	// When the remote env branch is absent the apply job must create and push it
	// at BASE_SHA so the resolution PR has a base to target.
	assert.Contains(t, applyJob, `refs/remotes/origin/env/${TARGET_ENV}`,
		"apply job must probe for the remote env branch before relying on it")
	assert.Contains(t, applyJob, `git push origin "${BASE_SHA}:refs/heads/env/${TARGET_ENV}"`,
		"apply job must push env/<env> at BASE_SHA when it is absent")
}

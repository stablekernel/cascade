package harness

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MultiStepScenario represents a full lifecycle E2E test with multiple steps
type MultiStepScenario struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Config      Config      `yaml:"config"`
	Setup       *SetupState `yaml:"setup,omitempty"` // Optional initial state
	Steps       []Step      `yaml:"steps"`
	// SetupWorkflows seeds reusable callback workflow files into the setup commit
	// BEFORE workflow generation runs, keyed by repository path (for example
	// ".github/workflows/deploy-app.yaml"). The harness generates a generic
	// non-failing stub for every build/deploy callback, which is sufficient for
	// most scenarios. A scenario that needs a callback to behave differently (for
	// example a deploy that exits non-zero only under the Rollback workflow) sets
	// the exact reusable-workflow body here so it is present on disk when the
	// generator reads the referenced workflow. A staged file overrides any
	// auto-generated stub at the same path. These files are harness-side only and
	// are never written into the generated manifest. Staging them via a step's
	// commit.files would land after generation, so they would be invisible to it.
	SetupWorkflows map[string]string `yaml:"setup_workflows,omitempty"`
}

// SetupState defines optional initial state for the scenario
type SetupState struct {
	State    map[string]*EnvStateSetup `yaml:"state,omitempty"`
	Tags     []string                  `yaml:"tags,omitempty"`
	Releases []ReleaseSetup            `yaml:"releases,omitempty"`
}

// EnvStateSetup defines initial environment state
type EnvStateSetup struct {
	SHA     string `yaml:"sha,omitempty"`
	Version string `yaml:"version,omitempty"`
	// Ref stages the environment on an integration branch (e.g. a hotfix branch)
	// rather than trunk. Written into the manifest's per-env state so a diverged
	// environment exists without running a full hotfix flow.
	Ref string `yaml:"ref,omitempty"`
	// BaseSHA is the trunk anchor the staged integration branch diverged from.
	// May be a commit reference (resolved via the execution context) or a literal.
	BaseSHA string `yaml:"base_sha,omitempty"`
	// Patches stages the patch commit SHAs applied on top of BaseSHA. Each entry
	// may be a commit reference or a literal SHA.
	Patches []string `yaml:"patches,omitempty"`
	// PreviousVersion stages the version held before divergence. Harness-side
	// only (see StateExpect.PreviousVersion); not written to the manifest.
	PreviousVersion string `yaml:"previous_version,omitempty"`
}

// Step represents a single action in the scenario
type Step struct {
	Name    string       `yaml:"name"`
	Action  string       `yaml:"action"` // commit, orchestrate, promote, hotfix_plan, hotfix_apply, merge_pr, resolve_conflict, hotfix_merged
	Commit  *CommitStep  `yaml:"commit,omitempty"`
	Promote *PromoteStep `yaml:"promote,omitempty"`
	// Rollback configures a "rollback" action: a workflow_dispatch run of the
	// cascade-rollback workflow that re-points an environment at a prior version
	// or SHA, re-runs its deploys at that target, and marks the environment
	// diverged until a forward promotion rejoins it.
	Rollback *RollbackStep `yaml:"rollback,omitempty"`
	Expect   *StepExpect   `yaml:"expect,omitempty"`
	// HotfixPlan configures a "hotfix_plan" action: a workflow_dispatch run of the
	// hotfix workflow's plan job for a trunk commit and target environment.
	HotfixPlan *HotfixPlanStep `yaml:"hotfix_plan,omitempty"`
	// HotfixApply configures a "hotfix_apply" action: a harness-driven cherry-pick
	// of a trunk commit onto the target env branch, opening a hotfix PR.
	HotfixApply *HotfixApplyStep `yaml:"hotfix_apply,omitempty"`
	// MergePR configures a "merge_pr" action: squash-merge an open PR identified
	// by index or label.
	MergePR *MergePRStep `yaml:"merge_pr,omitempty"`
	// ResolveConflict configures a "resolve_conflict" action: push resolved file
	// content to the last hotfix PR head branch and re-run the check job.
	ResolveConflict *ResolveConflictStep `yaml:"resolve_conflict,omitempty"`
	// HotfixMerged configures a "hotfix_merged" action: replay the merged
	// pull_request event so the context/build/deploy/finalize jobs run.
	HotfixMerged *HotfixMergedStep `yaml:"hotfix_merged,omitempty"`
	// StageDivergence configures a "stage_divergence" action: overwrite an
	// environment's divergence fields in the live manifest mid-scenario without
	// running any workflow.
	StageDivergence *StageDivergenceStep `yaml:"stage_divergence,omitempty"`
	// Verify configures a "verify" action: a read-only `cascade verify` run that
	// asserts the committed workflows match the manifest, exercising verify's
	// exit-code contract.
	Verify *VerifyStep `yaml:"verify,omitempty"`
	// ExpectFailure marks a step whose workflow is expected to conclude in
	// failure (for example an orchestrate run whose build exits non-zero). When
	// set, a failure conclusion is the success path and a success conclusion is
	// the error. Mirrors PromoteStep.ExpectFailure so orchestrate and promote
	// share one operator-facing knob.
	ExpectFailure bool `yaml:"expect_failure,omitempty"`
}

// HotfixPlanStep defines a hotfix_plan action: a workflow_dispatch of the hotfix
// workflow's plan job. CommitRef is the trunk commit to plan a hotfix for and is
// resolved via the execution context (falling back to a literal SHA).
type HotfixPlanStep struct {
	CommitRef     string `yaml:"commit_ref"`
	TargetEnv     string `yaml:"target_env"`
	DryRun        bool   `yaml:"dry_run,omitempty"`
	ExpectFailure bool   `yaml:"expect_failure,omitempty"`
}

// HotfixApplyStep defines a hotfix_apply action: a harness-driven cherry-pick of
// CommitRef onto env/<TargetEnv>, pushing a hotfix branch and opening a labeled
// PR. CommitRef is resolved via the execution context (falling back to literal).
type HotfixApplyStep struct {
	TargetEnv string `yaml:"target_env"`
	CommitRef string `yaml:"commit_ref"`
}

// MergePRStep defines a merge_pr action. Index identifies the PR directly; if
// zero, the first open PR carrying Label is merged.
type MergePRStep struct {
	Label string `yaml:"label,omitempty"`
	Index int64  `yaml:"index,omitempty"`
}

// ResolveConflictStep defines a resolve_conflict action. Files maps repository
// paths to their resolved content, committed onto the last hotfix PR head branch.
type ResolveConflictStep struct {
	Files map[string]string `yaml:"files"`
}

// HotfixMergedStep defines a hotfix_merged action: replay of the merged
// pull_request event for the recorded hotfix PR of TargetEnv.
type HotfixMergedStep struct {
	TargetEnv string `yaml:"target_env"`
}

// StageDivergenceStep defines a stage_divergence action: it rewrites the
// divergence fields (ref/base_sha/patches) for Env directly in the live
// manifest, then records the same divergence in the execution context. No
// workflow runs. Ref/BaseSHA/Patches entries may be commit references (resolved
// via the execution context) or literal SHAs. Used to re-wire a diverged env's
// patch set to an off-trunk SHA so a later promote exercises the
// patch-containment guard.
type StageDivergenceStep struct {
	Env             string   `yaml:"env"`
	Ref             string   `yaml:"ref,omitempty"`
	BaseSHA         string   `yaml:"base_sha,omitempty"`
	Patches         []string `yaml:"patches,omitempty"`
	PreviousVersion string   `yaml:"previous_version,omitempty"`
}

// CommitStep defines a commit action
type CommitStep struct {
	Message string            `yaml:"message"`
	Files   map[string]string `yaml:"files"`
}

// PromoteStep defines a promote action
type PromoteStep struct {
	Mode string `yaml:"mode"` // default, cascade
	// Target is the destination env for a cascade promote; the harness builds the
	// "<source>-to-<target>" mode string from Source and Target.
	Target string `yaml:"target,omitempty"`
	// Source overrides the cascade source env. When unset the harness defaults to
	// Environments[0] (the trunk-rooted leg), matching the generator's dev-rooted
	// cascade options. Set it to drive a non-default leg, e.g. source: test with
	// target: prod runs the test-to-prod hop so a promote sourced from a diverged
	// env exercises the diverged-source guard.
	Source        string `yaml:"source,omitempty"`
	AllowBreaking bool   `yaml:"allow_breaking,omitempty"`
	ExpectFailure bool   `yaml:"expect_failure,omitempty"`
	// Force sets the promote workflow's "force" dispatch input to "true",
	// bypassing the no-op promotion guard. Only meaningful for multi-env
	// (default mode) promotions; single-env Release promotions ignore it.
	Force bool `yaml:"force,omitempty"`
	// RollbackOnFailure sets the promote workflow's "rollback_on_failure"
	// dispatch input to "true", enabling the atomic rollback path: when a deploy
	// fails, every deploy that already succeeded is rolled back to the SHA
	// previously deployed in the target env (preflight's rollback_sha).
	RollbackOnFailure bool `yaml:"rollback_on_failure,omitempty"`
}

// RollbackStep defines a rollback action: a workflow_dispatch of the
// cascade-rollback workflow. Environment is the env to roll back. Target is the
// prior version or SHA to roll back to; when empty the workflow defaults to the
// previous version (N-1). Deployable, when set, limits the rollback to a single
// deployable. DryRun sets the dry_run input, which suppresses the deploy and
// finalize jobs. ExpectFailure marks a run that is expected to conclude in
// failure (for example a rollback whose preflight cannot resolve a target),
// mirroring PromoteStep.ExpectFailure.
type RollbackStep struct {
	Environment   string `yaml:"environment"`
	Target        string `yaml:"target,omitempty"`
	Deployable    string `yaml:"deployable,omitempty"`
	DryRun        bool   `yaml:"dry_run,omitempty"`
	ExpectFailure bool   `yaml:"expect_failure,omitempty"`
}

// VerifyStep defines a verify action: a read-only `cascade verify` run in the
// repo that compares the committed workflow and action files against what the
// manifest would generate. Regenerate, when set, runs `cascade generate-workflow
// -f` first so verify checks pristine generated output rather than the harness's
// localized copies. Mutate optionally overwrites one generated file with the
// given content before verifying, so a scenario can drive the drift path.
// ExpectExit is the exit code `cascade verify` must return (0 = no drift,
// non-zero = drift).
type VerifyStep struct {
	Regenerate   bool   `yaml:"regenerate,omitempty"`
	MutatePath   string `yaml:"mutate_path,omitempty"`
	MutateAppend string `yaml:"mutate_append,omitempty"`
	ExpectExit   int    `yaml:"expect_exit"`
}

// StepExpect defines expected outcomes for a step
type StepExpect struct {
	State         map[string]*StateExpect `yaml:"state,omitempty"`
	Jobs          map[string]string       `yaml:"jobs,omitempty"` // job name -> success/skipped/failure
	Releases      []ReleaseExpectStep     `yaml:"releases,omitempty"`
	Tags          *TagsExpect             `yaml:"tags,omitempty"`
	Preflight     *PreflightExpect        `yaml:"preflight,omitempty"`
	WorkflowFiles []WorkflowFileExpect    `yaml:"workflow_files,omitempty"` // Generated workflow file content checks
	// Branches asserts presence/absence of branches in Gitea (live check).
	Branches *BranchesExpect `yaml:"branches,omitempty"`
	// PRs asserts open pull requests in Gitea (live check).
	PRs *PRsExpect `yaml:"prs,omitempty"`
}

// BranchesExpect asserts branch existence in Gitea. Exist entries must be
// present; Deleted entries must be absent.
type BranchesExpect struct {
	Exist   []string `yaml:"exist,omitempty"`
	Deleted []string `yaml:"deleted,omitempty"`
}

// PRsExpect asserts open pull requests in Gitea. OpenWithLabel filters open PRs
// by label; when OpenCount is set the count must match exactly, otherwise at
// least one matching PR must be open.
type PRsExpect struct {
	OpenWithLabel string `yaml:"open_with_label,omitempty"`
	OpenCount     *int   `yaml:"open_count,omitempty"`
}

// WorkflowFileExpect asserts a generated workflow file contains/excludes
// specific substrings, or asserts the file is absent entirely. Verifies
// manifest fields make it into the emitted YAML, orthogonal to behavior
// checks (state/jobs/etc.) which observe the run outcome. NotExists covers
// the conditional-generation case where a feature suppresses a whole file
// (for example the hotfix workflow when fewer than two environments exist).
// Used for features whose effect is purely the generated workflow shape
// (#92 concurrency, #97 timeout-minutes, #101/#102 push retry loops).
type WorkflowFileExpect struct {
	Path        string   `yaml:"path"`                   // Path inside the test repo (e.g., ".github/workflows/orchestrate.yaml")
	Contains    []string `yaml:"contains,omitempty"`     // Substrings that must appear
	NotContains []string `yaml:"not_contains,omitempty"` // Substrings that must NOT appear
	NotExists   bool     `yaml:"not_exists,omitempty"`   // When true, the file must NOT exist
}

// StateExpect defines expected state for an environment
type StateExpect struct {
	SHA       string                   `yaml:"sha,omitempty"` // Can be "commit1", "commit2", etc.
	Version   string                   `yaml:"version,omitempty"`
	Wiped     bool                     `yaml:"wiped,omitempty"`     // State should not exist
	Unchanged bool                     `yaml:"unchanged,omitempty"` // State should match previous
	Deploys   map[string]*DeployExpect `yaml:"deploys,omitempty"`
	// Ref is the integration branch the environment is expected to track instead
	// of trunk (e.g. "env/prod" or a hotfix branch). Matched exactly against the
	// state's recorded divergence ref.
	Ref string `yaml:"ref,omitempty"`
	// BaseSHA is the trunk anchor the integration branch is expected to have
	// diverged from. Resolved via commit references (falling back to a literal)
	// the same way SHA is.
	BaseSHA string `yaml:"base_sha,omitempty"`
	// Patches lists every patch commit the environment must have applied on top
	// of BaseSHA. Treated as "must contain all listed": each entry is resolved
	// via a commit reference (falling back to a literal) and must be present in
	// the recorded patches slice.
	Patches []string `yaml:"patches,omitempty"`
	// PatchesContain lists substrings or exact members that must each match at
	// least one recorded patch (membership/substring match, no reference
	// resolution). Useful when only a fragment of a patch SHA is known.
	PatchesContain []string `yaml:"patches_contain,omitempty"`
	// PreviousVersion is the version the environment held before the most recent
	// divergence update. Matched exactly. This is a harness-side expectation
	// surface and is not read back from the product manifest (the product tracks
	// prior versions in a separate "previous" ring), so it is only populated by
	// setup staging or by an explicit divergence record.
	PreviousVersion string `yaml:"previous_version,omitempty"`
	// Cleared names divergence fields that must now be empty on the recorded
	// state. Supported members: "ref", "base_sha", "patches". This expresses the
	// rejoin contract (divergence fields are cleared once an env rejoins trunk),
	// which an empty Ref/BaseSHA/Patches value alone cannot assert because empty
	// expectation values are skipped. Each named field must read back empty.
	Cleared []string `yaml:"cleared,omitempty"`
}

// DeployExpect defines expected deploy state
type DeployExpect struct {
	SHA string `yaml:"sha,omitempty"`
}

// ReleaseExpectStep defines expected release state
type ReleaseExpectStep struct {
	Tag        string `yaml:"tag"`
	Prerelease bool   `yaml:"prerelease,omitempty"`
	Draft      bool   `yaml:"draft,omitempty"`
	Latest     bool   `yaml:"latest,omitempty"`
	Deleted    bool   `yaml:"deleted,omitempty"` // Tag should be deleted
}

// TagsExpect defines expected tag state
type TagsExpect struct {
	Exist   []string `yaml:"exist,omitempty"`
	Deleted []string `yaml:"deleted,omitempty"`
}

// PreflightExpect defines expected preflight outputs
type PreflightExpect struct {
	HasBreaking bool   `yaml:"has_breaking,omitempty"`
	CanProceed  bool   `yaml:"can_proceed,omitempty"`
	SourceEnv   string `yaml:"source_env,omitempty"`
	TargetEnv   string `yaml:"target_env,omitempty"`
}

// ParseMultiStepScenario parses YAML bytes into a MultiStepScenario
func ParseMultiStepScenario(data []byte) (*MultiStepScenario, error) {
	var s MultiStepScenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// DiscoverMultiStepScenarios finds and parses all multi-step scenario YAML files
func DiscoverMultiStepScenarios(dir string) ([]*MultiStepScenario, error) {
	var scenarios []*MultiStepScenario

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Skip multi-repo scenarios (they use "repos:" instead of "config:")
		if strings.Contains(string(data), "\nrepos:") {
			return nil
		}

		scenario, err := ParseMultiStepScenario(data)
		if err != nil {
			return err
		}

		// Store relative path for test naming
		relPath, _ := filepath.Rel(dir, path)
		if scenario.Description != "" {
			scenario.Description = relPath + ": " + scenario.Description
		} else {
			scenario.Description = relPath
		}

		scenarios = append(scenarios, scenario)
		return nil
	})

	return scenarios, err
}

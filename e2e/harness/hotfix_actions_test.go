package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseHotfixScenario verifies the new step actions, divergence expectation
// fields, diverged-env setup staging, promote force, and branches/prs
// expectations all unmarshal into the expected struct fields.
func TestParseHotfixScenario(t *testing.T) {
	yamlDoc := `
name: "Hotfix lifecycle"
config:
  environments: [dev, prod]
setup:
  state:
    prod:
      sha: base000
      version: v1.0.0
      ref: hotfix/prod/abc12345
      base_sha: commit1
      patches: [commit2]
      previous_version: v0.9.0
steps:
  - name: "Plan hotfix"
    action: hotfix_plan
    hotfix_plan:
      commit_ref: commit1
      target_env: prod
      dry_run: true
  - name: "Apply hotfix"
    action: hotfix_apply
    hotfix_apply:
      commit_ref: commit1
      target_env: prod
  - name: "Resolve conflict"
    action: resolve_conflict
    resolve_conflict:
      files:
        src/app.ts: "// resolved"
  - name: "Merge PR"
    action: merge_pr
    merge_pr:
      label: cascade-hotfix
  - name: "Merged event"
    action: hotfix_merged
    hotfix_merged:
      target_env: prod
    expect:
      state:
        prod:
          ref: hotfix/prod/abc12345
          base_sha: commit1
          patches: [commit2]
          patches_contain: [commit]
          previous_version: v0.9.0
      branches:
        exist: [env/prod]
        deleted: [hotfix/prod/abc12345]
      prs:
        open_with_label: cascade-hotfix
        open_count: 0
  - name: "Force promote"
    action: promote
    promote:
      mode: default
      force: true
`
	s, err := ParseMultiStepScenario([]byte(yamlDoc))
	require.NoError(t, err)
	require.Len(t, s.Steps, 6)

	// Setup diverged env.
	prodSetup := s.Setup.State["prod"]
	require.NotNil(t, prodSetup)
	assert.Equal(t, "hotfix/prod/abc12345", prodSetup.Ref)
	assert.Equal(t, "commit1", prodSetup.BaseSHA)
	assert.Equal(t, []string{"commit2"}, prodSetup.Patches)
	assert.Equal(t, "v0.9.0", prodSetup.PreviousVersion)

	// hotfix_plan.
	require.NotNil(t, s.Steps[0].HotfixPlan)
	assert.Equal(t, "commit1", s.Steps[0].HotfixPlan.CommitRef)
	assert.Equal(t, "prod", s.Steps[0].HotfixPlan.TargetEnv)
	assert.True(t, s.Steps[0].HotfixPlan.DryRun)

	// hotfix_apply.
	require.NotNil(t, s.Steps[1].HotfixApply)
	assert.Equal(t, "commit1", s.Steps[1].HotfixApply.CommitRef)
	assert.Equal(t, "prod", s.Steps[1].HotfixApply.TargetEnv)

	// resolve_conflict.
	require.NotNil(t, s.Steps[2].ResolveConflict)
	assert.Equal(t, "// resolved", s.Steps[2].ResolveConflict.Files["src/app.ts"])

	// merge_pr.
	require.NotNil(t, s.Steps[3].MergePR)
	assert.Equal(t, "cascade-hotfix", s.Steps[3].MergePR.Label)

	// hotfix_merged + expectations.
	require.NotNil(t, s.Steps[4].HotfixMerged)
	assert.Equal(t, "prod", s.Steps[4].HotfixMerged.TargetEnv)
	exp := s.Steps[4].Expect
	require.NotNil(t, exp)
	se := exp.State["prod"]
	require.NotNil(t, se)
	assert.Equal(t, "hotfix/prod/abc12345", se.Ref)
	assert.Equal(t, "commit1", se.BaseSHA)
	assert.Equal(t, []string{"commit2"}, se.Patches)
	assert.Equal(t, []string{"commit"}, se.PatchesContain)
	assert.Equal(t, "v0.9.0", se.PreviousVersion)
	require.NotNil(t, exp.Branches)
	assert.Equal(t, []string{"env/prod"}, exp.Branches.Exist)
	assert.Equal(t, []string{"hotfix/prod/abc12345"}, exp.Branches.Deleted)
	require.NotNil(t, exp.PRs)
	assert.Equal(t, "cascade-hotfix", exp.PRs.OpenWithLabel)
	require.NotNil(t, exp.PRs.OpenCount)
	assert.Equal(t, 0, *exp.PRs.OpenCount)

	// promote force.
	require.NotNil(t, s.Steps[5].Promote)
	assert.True(t, s.Steps[5].Promote.Force)
}

// TestValidateScenarioHotfixActions covers valid and invalid configurations for
// each new step action.
func TestValidateScenarioHotfixActions(t *testing.T) {
	tests := []struct {
		name    string
		step    Step
		wantErr string // substring; empty means no error expected
	}{
		{
			name: "hotfix_plan valid",
			step: Step{Name: "p", Action: "hotfix_plan", HotfixPlan: &HotfixPlanStep{CommitRef: "c1", TargetEnv: "prod"}},
		},
		{
			name:    "hotfix_plan missing config",
			step:    Step{Name: "p", Action: "hotfix_plan"},
			wantErr: "requires hotfix_plan config",
		},
		{
			name:    "hotfix_plan missing target_env",
			step:    Step{Name: "p", Action: "hotfix_plan", HotfixPlan: &HotfixPlanStep{CommitRef: "c1"}},
			wantErr: "requires target_env",
		},
		{
			name:    "hotfix_plan missing commit_ref",
			step:    Step{Name: "p", Action: "hotfix_plan", HotfixPlan: &HotfixPlanStep{TargetEnv: "prod"}},
			wantErr: "requires commit_ref",
		},
		{
			name: "hotfix_apply valid",
			step: Step{Name: "a", Action: "hotfix_apply", HotfixApply: &HotfixApplyStep{CommitRef: "c1", TargetEnv: "prod"}},
		},
		{
			name:    "hotfix_apply missing config",
			step:    Step{Name: "a", Action: "hotfix_apply"},
			wantErr: "requires hotfix_apply config",
		},
		{
			name:    "hotfix_apply missing target_env",
			step:    Step{Name: "a", Action: "hotfix_apply", HotfixApply: &HotfixApplyStep{CommitRef: "c1"}},
			wantErr: "requires target_env",
		},
		{
			name:    "hotfix_apply missing commit_ref",
			step:    Step{Name: "a", Action: "hotfix_apply", HotfixApply: &HotfixApplyStep{TargetEnv: "prod"}},
			wantErr: "requires commit_ref",
		},
		{
			name: "merge_pr valid by label",
			step: Step{Name: "m", Action: "merge_pr", MergePR: &MergePRStep{Label: "cascade-hotfix"}},
		},
		{
			name: "merge_pr valid by index",
			step: Step{Name: "m", Action: "merge_pr", MergePR: &MergePRStep{Index: 3}},
		},
		{
			name:    "merge_pr missing config",
			step:    Step{Name: "m", Action: "merge_pr"},
			wantErr: "requires merge_pr config",
		},
		{
			name:    "merge_pr missing label and index",
			step:    Step{Name: "m", Action: "merge_pr", MergePR: &MergePRStep{}},
			wantErr: "requires label or index",
		},
		{
			name: "resolve_conflict valid",
			step: Step{Name: "r", Action: "resolve_conflict", ResolveConflict: &ResolveConflictStep{Files: map[string]string{"a": "b"}}},
		},
		{
			name:    "resolve_conflict missing config",
			step:    Step{Name: "r", Action: "resolve_conflict"},
			wantErr: "requires resolve_conflict config",
		},
		{
			name:    "resolve_conflict no files",
			step:    Step{Name: "r", Action: "resolve_conflict", ResolveConflict: &ResolveConflictStep{Files: map[string]string{}}},
			wantErr: "requires at least one file",
		},
		{
			name: "hotfix_merged valid",
			step: Step{Name: "h", Action: "hotfix_merged", HotfixMerged: &HotfixMergedStep{TargetEnv: "prod"}},
		},
		{
			name:    "hotfix_merged missing config",
			step:    Step{Name: "h", Action: "hotfix_merged"},
			wantErr: "requires hotfix_merged config",
		},
		{
			name:    "hotfix_merged missing target_env",
			step:    Step{Name: "h", Action: "hotfix_merged", HotfixMerged: &HotfixMergedStep{}},
			wantErr: "requires target_env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunner(t, nil)
			err := r.ValidateScenario(&MultiStepScenario{Name: "s", Steps: []Step{tt.step}})
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestAssertStateDivergence covers match and mismatch for each new StateExpect
// divergence field against a hand-built context populated via
// RecordStateDivergence.
func TestAssertStateDivergence(t *testing.T) {
	newCtx := func() *ExecutionContext {
		c := NewExecutionContext()
		c.RecordCommit("commit1", "base0000abcd")
		c.RecordCommit("commit2", "patch1111efgh")
		c.RecordState("prod", "tip9999", "v1.1.0")
		c.RecordStateDivergence("prod", "hotfix/prod/base0000", "base0000abcd",
			[]string{"patch1111efgh"}, "v1.0.0")
		return c
	}

	tests := []struct {
		name    string
		expect  *StateExpect
		wantErr string // substring; empty means no errors
	}{
		{name: "ref match", expect: &StateExpect{Ref: "hotfix/prod/base0000"}},
		{name: "ref mismatch", expect: &StateExpect{Ref: "hotfix/prod/other"}, wantErr: ".ref expected"},
		{name: "base_sha match by reference", expect: &StateExpect{BaseSHA: "commit1"}},
		{name: "base_sha match by literal", expect: &StateExpect{BaseSHA: "base0000abcd"}},
		{name: "base_sha mismatch", expect: &StateExpect{BaseSHA: "deadbeef"}, wantErr: ".base_sha expected"},
		{name: "patches match by reference", expect: &StateExpect{Patches: []string{"commit2"}}},
		{name: "patches match by literal", expect: &StateExpect{Patches: []string{"patch1111efgh"}}},
		{name: "patches mismatch", expect: &StateExpect{Patches: []string{"missingsha"}}, wantErr: ".patches missing"},
		{name: "patches_contain match", expect: &StateExpect{PatchesContain: []string{"patch1111"}}},
		{name: "patches_contain mismatch", expect: &StateExpect{PatchesContain: []string{"nope"}}, wantErr: "no entry containing"},
		{name: "previous_version match", expect: &StateExpect{PreviousVersion: "v1.0.0"}},
		{name: "previous_version mismatch", expect: &StateExpect{PreviousVersion: "v0.0.1"}, wantErr: ".previous_version expected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := AssertState(newCtx(), "prod", tt.expect)
			if tt.wantErr == "" {
				assert.Empty(t, errs)
				return
			}
			require.NotEmpty(t, errs)
			var joined string
			for _, e := range errs {
				joined += e.Error() + "\n"
			}
			assert.Contains(t, joined, tt.wantErr)
		})
	}
}

// TestRunnerHotfixActionsNoHarness proves the dispatch wiring for each new
// action returns nil (the no-harness guard path) without containers.
func TestRunnerHotfixActionsNoHarness(t *testing.T) {
	ctx := context.Background()
	steps := []Step{
		{Name: "plan", Action: "hotfix_plan", HotfixPlan: &HotfixPlanStep{CommitRef: "c1", TargetEnv: "prod"}},
		{Name: "apply", Action: "hotfix_apply", HotfixApply: &HotfixApplyStep{CommitRef: "c1", TargetEnv: "prod"}},
		{Name: "merge", Action: "merge_pr", MergePR: &MergePRStep{Label: "cascade-hotfix"}},
		{Name: "resolve", Action: "resolve_conflict", ResolveConflict: &ResolveConflictStep{Files: map[string]string{"a": "b"}}},
		{Name: "merged", Action: "hotfix_merged", HotfixMerged: &HotfixMergedStep{TargetEnv: "prod"}},
	}
	for _, step := range steps {
		t.Run(step.Action, func(t *testing.T) {
			r := NewRunner(t, nil)
			step := step
			err := r.executeStep(ctx, &step, Config{Environments: []string{"dev", "prod"}})
			assert.NoError(t, err)
		})
	}
}

// TestRunnerStateDivergenceSetupNoHarness verifies applySetup records divergence
// into the execution context without a harness.
func TestRunnerStateDivergenceSetupNoHarness(t *testing.T) {
	r := NewRunner(t, nil)
	r.ctx.RecordCommit("commit1", "base0000")
	err := r.applySetup(context.Background(), &SetupState{
		State: map[string]*EnvStateSetup{
			"prod": {
				SHA:             "tip000",
				Version:         "v1.1.0",
				Ref:             "hotfix/prod/x",
				BaseSHA:         "commit1",
				Patches:         []string{"patchsha"},
				PreviousVersion: "v1.0.0",
			},
		},
	})
	require.NoError(t, err)

	st := r.ctx.GetState("prod")
	assert.Equal(t, "hotfix/prod/x", st.Ref)
	assert.Equal(t, "base0000", st.BaseSHA) // resolved from commit1
	assert.Equal(t, []string{"patchsha"}, st.Patches)
	assert.Equal(t, "v1.0.0", st.PreviousVersion)
}

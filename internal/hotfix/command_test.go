package hotfix

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns whatever
// fn printed. It lets the human-readable printers be asserted on their output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()
	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

// TestNewCommand verifies the hotfix command exposes plan and finalize.
func TestNewCommand(t *testing.T) {
	cmd := NewCommand()
	require.NotNil(t, cmd)
	assert.Equal(t, "hotfix", cmd.Name())

	plan, _, err := cmd.Find([]string{"plan"})
	require.NoError(t, err)
	assert.Equal(t, "plan", plan.Name())

	finalize, _, err := cmd.Find([]string{"finalize"})
	require.NoError(t, err)
	assert.Equal(t, "finalize", finalize.Name())
}

// TestNewPlanCommand_Flags asserts the plan subcommand wires its flags and
// required/exclusive constraints.
func TestNewPlanCommand_Flags(t *testing.T) {
	cmd := newPlanCommand()
	assert.Equal(t, "plan", cmd.Name())
	for _, name := range []string{"config", "key", "commit", "commits", "target-env", "actor", "remote", "repo", "component", "dry-run", "json", "gha-output"} {
		assert.NotNil(t, cmd.Flags().Lookup(name), "plan flag %q should exist", name)
	}
}

// TestNewFinalizeCommand_Flags asserts the finalize subcommand wires its flags.
func TestNewFinalizeCommand_Flags(t *testing.T) {
	cmd := newFinalizeCommand()
	assert.Equal(t, "finalize", cmd.Name())
	for _, name := range []string{"config", "key", "target-env", "merge-sha", "fix-sha", "base-sha", "actor", "dry-run", "deploy-result", "build-result"} {
		assert.NotNil(t, cmd.Flags().Lookup(name), "finalize flag %q should exist", name)
	}
}

// TestSplitResultFlag covers the name=result parser including its reject cases.
func TestSplitResultFlag(t *testing.T) {
	cases := []struct {
		in     string
		name   string
		result string
		ok     bool
	}{
		{"app=success", "app", "success", true},
		{"svc.api=failure", "svc.api", "failure", true},
		{"a=b=c", "a", "b=c", true},
		{"", "", "", false},
		{"=success", "", "", false},
		{"app=", "", "", false},
		{"noequals", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			name, result, ok := splitResultFlag(tc.in)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.name, name)
			assert.Equal(t, tc.result, result)
		})
	}
}

// TestNewRestPRChecker constructs the REST-backed checker and records its repo.
func TestNewRestPRChecker(t *testing.T) {
	c := newRestPRChecker("org/repo")
	require.NotNil(t, c)
	assert.Equal(t, "org/repo", c.repo)
}

// TestPrintPlan_FullPlan asserts the human plan output includes the env, branch,
// version, and the dry-run marker.
func TestPrintPlan_FullPlan(t *testing.T) {
	result := &PlanResult{
		TargetEnv:              "uat",
		FixSHA:                 "abcdef1234567890",
		Branch:                 "env/uat",
		BaseSHA:                "fedcba0987654321",
		BranchCreated:          true,
		DryRun:                 true,
		HotfixVersionCandidate: "v1.2.0-rc.1.hotfix.1",
		ProtectionSuggestions:  []string{"gh api ...protect..."},
	}
	out := captureStdout(t, func() { printPlan(result) })
	assert.Contains(t, out, "uat")
	assert.Contains(t, out, "env/uat")
	assert.Contains(t, out, "v1.2.0-rc.1.hotfix.1")
	assert.Contains(t, out, "dry-run")
	assert.Contains(t, out, "would create")
	assert.Contains(t, out, "gh api ...protect...")
}

// TestPrintPlan_NoOp short-circuits with a no-op message.
func TestPrintPlan_NoOp(t *testing.T) {
	result := &PlanResult{TargetEnv: "prod", FixSHA: "abc1234", NoOp: true}
	out := captureStdout(t, func() { printPlan(result) })
	assert.Contains(t, out, "no-op")
	assert.Contains(t, out, "prod")
	assert.NotContains(t, out, "Version:")
}

// TestPrintPlan_ExistingBranch reports the already-present branch path.
func TestPrintPlan_ExistingBranch(t *testing.T) {
	result := &PlanResult{
		TargetEnv:              "uat",
		FixSHA:                 "abcdef1",
		Branch:                 "env/uat",
		BaseSHA:                "fed0987",
		BranchCreated:          false,
		HotfixVersionCandidate: "v1.0.1",
	}
	out := captureStdout(t, func() { printPlan(result) })
	assert.Contains(t, out, "already present")
	assert.NotContains(t, out, "dry-run")
}

// TestPrintPlanChain renders the bottom-up env sequence and per-env commits.
func TestPrintPlanChain(t *testing.T) {
	result := &PlanChainResult{Envs: []EnvPlan{
		{Env: "test", Branch: "env/test", BaseSHA: "1111111aaaa", Commits: []string{"2222222bbbb", "3333333cccc"}},
		{Env: "uat", NoOp: true},
	}}
	out := captureStdout(t, func() { printPlanChain(result) })
	assert.Contains(t, out, "test")
	assert.Contains(t, out, "env/test")
	assert.Contains(t, out, "2222222")
	assert.Contains(t, out, "no-op")
	assert.Contains(t, out, "2 environment(s)")
}

// TestOutputJSON marshals a value and prints it as indented JSON.
func TestOutputJSON(t *testing.T) {
	out := captureStdout(t, func() {
		require.NoError(t, outputJSON(&PlanResult{TargetEnv: "uat", FixSHA: "abc"}))
	})
	assert.Contains(t, out, `"target_env": "uat"`)
	assert.Contains(t, out, `"fix_sha": "abc"`)
}

// TestWritePlanGHAOutput writes the single-env plan keys to $GITHUB_OUTPUT.
func TestWritePlanGHAOutput(t *testing.T) {
	outFile := t.TempDir() + "/gha_output"
	t.Setenv("GITHUB_OUTPUT", outFile)

	result := &PlanResult{
		TargetEnv:              "uat",
		FixSHA:                 "abc123",
		Branch:                 "env/uat",
		BaseSHA:                "def456",
		NoOp:                   false,
		BranchCreated:          true,
		HotfixVersionCandidate: "v1.0.1",
		ConflictExpected:       false,
		DryRun:                 true,
		ProtectionSuggestions:  []string{"gh api protect"},
	}
	require.NoError(t, writePlanGHAOutput(result))

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "target_env=uat")
	assert.Contains(t, content, "branch=env/uat")
	assert.Contains(t, content, "hotfix_version_candidate=v1.0.1")
	assert.Contains(t, content, "dry_run=true")
	assert.Contains(t, content, "protection_suggestions")
}

// TestWritePlanChainGHAOutput writes the additive chain keys to $GITHUB_OUTPUT.
func TestWritePlanChainGHAOutput(t *testing.T) {
	outFile := t.TempDir() + "/gha_output"
	t.Setenv("GITHUB_OUTPUT", outFile)

	result := &PlanChainResult{Envs: []EnvPlan{
		{Env: "test", Branch: "env/test", BaseSHA: "111", Commits: []string{"aaa", "bbb"}},
		{Env: "uat", Branch: "env/uat", BaseSHA: "222", NoOp: true},
	}}
	require.NoError(t, writePlanChainGHAOutput(result))

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "env_sequence=test,uat")
	assert.Contains(t, content, "env_count=2")
	assert.Contains(t, content, "commits_test=aaa,bbb")
	assert.Contains(t, content, "no_op_uat=true")
	assert.Contains(t, content, "base_test=111")
}

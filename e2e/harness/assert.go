package harness

import (
	"fmt"
	"strings"
)

// testingT is an interface that matches the subset of testing.T we use
type testingT interface {
	Errorf(format string, args ...interface{})
	Helper()
}

// WorkflowRunResult represents the result of a workflow run
type WorkflowRunResult struct {
	Jobs      map[string]string // job name -> conclusion (success/skipped/failure)
	Preflight *PreflightResult
}

// PreflightResult mirrors the promote preflight output for assertion
type PreflightResult struct {
	HasBreaking bool
	CanProceed  bool
	SourceEnv   string
	TargetEnv   string
}

// AssertStateMatches validates environment state against expectations
func AssertStateMatches(t testingT, ctx *ExecutionContext, expected map[string]*StateExpect) {
	t.Helper()

	for env, expect := range expected {
		assertState(t, ctx, env, expect)
	}
}

// assertState validates a single environment state against expectations
func assertState(t testingT, ctx *ExecutionContext, env string, expect *StateExpect) {
	t.Helper()
	actual := ctx.GetState(env)

	// Check if state should be wiped
	if expect.Wiped {
		if actual.SHA != "" || actual.Version != "" {
			t.Errorf("state[%s] expected to be wiped but has SHA=%s, Version=%s",
				env, actual.SHA, actual.Version)
		}
		return
	}

	// Check SHA (resolve commit references)
	if expect.SHA != "" {
		expectedSHA := ctx.ResolveSHA(expect.SHA)
		if expectedSHA == "" {
			expectedSHA = expect.SHA // Use as literal if not a reference
		}
		if actual.SHA != expectedSHA {
			t.Errorf("state[%s].sha expected %s, got %s",
				env, expectedSHA, actual.SHA)
		}
	}

	// Check version
	if expect.Version != "" && actual.Version != expect.Version {
		t.Errorf("state[%s].version expected %s, got %s",
			env, expect.Version, actual.Version)
	}

	// Check deploys
	for deployName, deployExpect := range expect.Deploys {
		actualDeploy := actual.Deploys[deployName]
		if actualDeploy == nil {
			t.Errorf("state[%s].deploys[%s] expected but not found",
				env, deployName)
			continue
		}
		if deployExpect.SHA != "" {
			expectedSHA := ctx.ResolveSHA(deployExpect.SHA)
			if expectedSHA == "" {
				expectedSHA = deployExpect.SHA
			}
			if actualDeploy.SHA != expectedSHA {
				t.Errorf("state[%s].deploys[%s].sha expected %s, got %s",
					env, deployName, expectedSHA, actualDeploy.SHA)
			}
		}
	}
}

// AssertJobsRan validates job conclusions against expectations
func AssertJobsRan(t testingT, runResult *WorkflowRunResult, expected map[string]string) {
	t.Helper()

	if runResult == nil {
		t.Errorf("workflow run result is nil")
		return
	}

	for job, expectedConclusion := range expected {
		actualConclusion, ok := runResult.Jobs[job]
		if !ok {
			t.Errorf("job[%s] expected %s but job not found",
				job, expectedConclusion)
			continue
		}
		if actualConclusion != expectedConclusion {
			t.Errorf("job[%s] expected %s, got %s",
				job, expectedConclusion, actualConclusion)
		}
	}
}

// AssertTagsExist validates tag existence against expectations
func AssertTagsExist(t testingT, ctx *ExecutionContext, expected *TagsExpect) {
	t.Helper()

	if expected == nil {
		return
	}

	for _, tag := range expected.Exist {
		if !ctx.HasTag(tag) {
			t.Errorf("tag %s expected to exist but not found", tag)
		}
	}

	for _, tag := range expected.Deleted {
		if ctx.HasTag(tag) {
			t.Errorf("tag %s expected to be deleted but exists", tag)
		}
	}
}

// AssertReleases validates release state against expectations
func AssertReleases(t testingT, ctx *ExecutionContext, expected []ReleaseExpectStep) {
	t.Helper()

	for _, exp := range expected {
		release := ctx.GetRelease(exp.Tag)

		if exp.Deleted {
			if release != nil {
				t.Errorf("release %s expected to be deleted but exists", exp.Tag)
			}
			continue
		}

		if release == nil {
			t.Errorf("release %s expected but not found", exp.Tag)
			continue
		}

		// Check prerelease flag
		if exp.Prerelease != release.Prerelease {
			t.Errorf("release %s expected prerelease=%v, got %v",
				exp.Tag, exp.Prerelease, release.Prerelease)
		}

		// Check draft flag
		if exp.Draft != release.Draft {
			t.Errorf("release %s expected draft=%v, got %v",
				exp.Tag, exp.Draft, release.Draft)
		}

		// Check latest flag if specified
		if exp.Latest && !release.Latest {
			t.Errorf("release %s expected latest=true, got false", exp.Tag)
		}
	}
}

// AssertPreflight validates preflight outputs against expectations
func AssertPreflight(t testingT, runResult *WorkflowRunResult, expected *PreflightExpect) {
	t.Helper()

	if expected == nil {
		return
	}

	if runResult == nil || runResult.Preflight == nil {
		t.Errorf("preflight result is nil")
		return
	}

	actual := runResult.Preflight

	if expected.HasBreaking != actual.HasBreaking {
		t.Errorf("preflight expected has_breaking=%v, got %v",
			expected.HasBreaking, actual.HasBreaking)
	}

	if expected.CanProceed != actual.CanProceed {
		t.Errorf("preflight expected can_proceed=%v, got %v",
			expected.CanProceed, actual.CanProceed)
	}

	if expected.SourceEnv != "" && actual.SourceEnv != expected.SourceEnv {
		t.Errorf("preflight expected source_env=%s, got %s",
			expected.SourceEnv, actual.SourceEnv)
	}

	if expected.TargetEnv != "" && actual.TargetEnv != expected.TargetEnv {
		t.Errorf("preflight expected target_env=%s, got %s",
			expected.TargetEnv, actual.TargetEnv)
	}
}

// assertsSomething reports whether expect carries at least one real assertion.
// Component and Env are selectors: they choose which state leaf to read, and say
// nothing about what it must contain. Every other populated field constrains the
// state, including Wiped and Unchanged, whose truth is itself the assertion.
func (expect *StateExpect) assertsSomething() bool {
	return expect.SHA != "" ||
		expect.Version != "" ||
		expect.Wiped ||
		expect.Unchanged ||
		expect.Ref != "" ||
		expect.BaseSHA != "" ||
		expect.PreviousVersion != "" ||
		len(expect.Deploys) > 0 ||
		len(expect.Patches) > 0 ||
		len(expect.PatchesContain) > 0 ||
		len(expect.PreviousContains) > 0 ||
		len(expect.Cleared) > 0
}

// AssertState validates environment state against expectations (used internally)
// Returns errors instead of failing test directly for more flexible error handling
func AssertState(ctx *ExecutionContext, env string, expect *StateExpect) []error {
	var errs []error

	// An expectation with no populated assertion field compares nothing against
	// nothing and always passes, so the step it belongs to is a no-op wearing the
	// shape of a check. Reject it rather than let it report green.
	if !expect.assertsSomething() {
		return []error{fmt.Errorf("state[%s] expectation asserts nothing: set at least one of sha, version, wiped, unchanged, deploys, ref, base_sha, patches, patches_contain, previous_version, previous_contains, cleared (component and env only select which state to read)", env)}
	}

	actual := ctx.GetState(env)

	// Check if state should be wiped
	if expect.Wiped {
		if actual.SHA != "" || actual.Version != "" {
			errs = append(errs, fmt.Errorf("state[%s] expected to be wiped but has SHA=%s, Version=%s",
				env, actual.SHA, actual.Version))
		}
		return errs
	}

	// Every remaining expectation is positive: it describes something the state
	// must hold. Reading an env the scenario never recorded yields a zero
	// EnvState, so a typo'd env name would be compared field by field against
	// empty values and quietly report on state nobody wrote. Absence is only a
	// valid subject for Wiped, which returned above.
	if !ctx.HasState(env) {
		return []error{fmt.Errorf("state[%s] has no recorded state, so this expectation cannot hold: check the env name, or use wiped to assert absence", env)}
	}

	// Check SHA (resolve commit references)
	if expect.SHA != "" {
		expectedSHA := ctx.ResolveSHA(expect.SHA)
		if expectedSHA == "" {
			expectedSHA = expect.SHA // Use as literal if not a reference
		}
		if actual.SHA != expectedSHA {
			errs = append(errs, fmt.Errorf("state[%s].sha expected %s, got %s",
				env, expectedSHA, actual.SHA))
		}
	}

	// Check version
	if expect.Version != "" && actual.Version != expect.Version {
		errs = append(errs, fmt.Errorf("state[%s].version expected %s, got %s",
			env, expect.Version, actual.Version))
	}

	// Check integration-branch ref (exact match).
	if expect.Ref != "" && actual.Ref != expect.Ref {
		errs = append(errs, fmt.Errorf("state[%s].ref expected %s, got %s",
			env, expect.Ref, actual.Ref))
	}

	// Check base SHA (resolve commit references, falling back to literal).
	if expect.BaseSHA != "" {
		expectedBase := ctx.ResolveSHA(expect.BaseSHA)
		if expectedBase == "" {
			expectedBase = expect.BaseSHA
		}
		if actual.BaseSHA != expectedBase {
			errs = append(errs, fmt.Errorf("state[%s].base_sha expected %s, got %s",
				env, expectedBase, actual.BaseSHA))
		}
	}

	// Check patches: every listed patch (resolved via reference, falling back to
	// literal) must be present in the recorded patches slice.
	for _, p := range expect.Patches {
		want := ctx.ResolveSHA(p)
		if want == "" {
			want = p
		}
		if !containsString(actual.Patches, want) {
			errs = append(errs, fmt.Errorf("state[%s].patches missing %s, got %v",
				env, want, actual.Patches))
		}
	}

	// Check patches_contain: each entry must match at least one recorded patch
	// by substring (no reference resolution).
	for _, frag := range expect.PatchesContain {
		if !anyContains(actual.Patches, frag) {
			errs = append(errs, fmt.Errorf("state[%s].patches has no entry containing %q, got %v",
				env, frag, actual.Patches))
		}
	}

	// Check pre-divergence version (exact match).
	if expect.PreviousVersion != "" && actual.PreviousVersion != expect.PreviousVersion {
		errs = append(errs, fmt.Errorf("state[%s].previous_version expected %s, got %s",
			env, expect.PreviousVersion, actual.PreviousVersion))
	}

	// Check previous_contains: each listed version must appear in the recorded
	// deploy-history ring, proving a later promotion carried the ring forward.
	for _, want := range expect.PreviousContains {
		if !containsString(actual.PreviousVersions, want) {
			errs = append(errs, fmt.Errorf("state[%s].previous has no entry with version %q, got %v",
				env, want, actual.PreviousVersions))
		}
	}

	// Check cleared divergence fields: each named field must read back empty.
	// Expresses the rejoin contract, which an empty expectation value alone
	// cannot assert (empty Ref/BaseSHA/Patches expectations are skipped above).
	for _, field := range expect.Cleared {
		switch field {
		case "ref":
			if actual.Ref != "" {
				errs = append(errs, fmt.Errorf("state[%s].ref expected cleared but got %s",
					env, actual.Ref))
			}
		case "base_sha":
			if actual.BaseSHA != "" {
				errs = append(errs, fmt.Errorf("state[%s].base_sha expected cleared but got %s",
					env, actual.BaseSHA))
			}
		case "patches":
			if len(actual.Patches) > 0 {
				errs = append(errs, fmt.Errorf("state[%s].patches expected cleared but got %v",
					env, actual.Patches))
			}
		default:
			errs = append(errs, fmt.Errorf("state[%s].cleared lists unsupported field %q (want ref|base_sha|patches)",
				env, field))
		}
	}

	// Check deploys
	for deployName, deployExpect := range expect.Deploys {
		actualDeploy := actual.Deploys[deployName]
		if actualDeploy == nil {
			errs = append(errs, fmt.Errorf("state[%s].deploys[%s] expected but not found",
				env, deployName))
			continue
		}
		if deployExpect.SHA != "" {
			expectedSHA := ctx.ResolveSHA(deployExpect.SHA)
			if expectedSHA == "" {
				expectedSHA = deployExpect.SHA
			}
			if actualDeploy.SHA != expectedSHA {
				errs = append(errs, fmt.Errorf("state[%s].deploys[%s].sha expected %s, got %s",
					env, deployName, expectedSHA, actualDeploy.SHA))
			}
		}
	}

	return errs
}

// containsString reports whether s appears exactly in list.
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// anyContains reports whether any element of list contains substr.
func anyContains(list []string, substr string) bool {
	for _, v := range list {
		if strings.Contains(v, substr) {
			return true
		}
	}
	return false
}

// AssertJobs validates job conclusions against expectations (used internally)
// Returns errors instead of failing test directly for more flexible error handling
func AssertJobs(actual map[string]string, expect map[string]string) []error {
	var errs []error

	for job, expectedConclusion := range expect {
		actualConclusion, ok := actual[job]
		if !ok {
			errs = append(errs, fmt.Errorf("job[%s] expected %s but job not found",
				job, expectedConclusion))
			continue
		}
		if actualConclusion != expectedConclusion {
			errs = append(errs, fmt.Errorf("job[%s] expected %s, got %s",
				job, expectedConclusion, actualConclusion))
		}
	}

	return errs
}

// AssertTags validates tag existence against expectations (used internally)
// Returns errors instead of failing test directly for more flexible error handling
func AssertTags(ctx *ExecutionContext, expect *TagsExpect) []error {
	var errs []error

	for _, tag := range expect.Exist {
		if !ctx.HasTag(tag) {
			errs = append(errs, fmt.Errorf("tag %s expected to exist but not found", tag))
		}
	}

	for _, tag := range expect.Deleted {
		if ctx.HasTag(tag) {
			errs = append(errs, fmt.Errorf("tag %s expected to be deleted but exists", tag))
		}
	}

	return errs
}

// AssertReleasesInternal validates release state against expectations (used internally)
// Returns errors instead of failing test directly for more flexible error handling
func AssertReleasesInternal(ctx *ExecutionContext, expect []ReleaseExpectStep) []error {
	var errs []error

	for _, exp := range expect {
		release := ctx.GetRelease(exp.Tag)

		if exp.Deleted {
			if release != nil {
				errs = append(errs, fmt.Errorf("release %s expected to be deleted but exists", exp.Tag))
			}
			continue
		}

		if release == nil {
			errs = append(errs, fmt.Errorf("release %s expected but not found", exp.Tag))
			continue
		}

		if exp.Prerelease != release.Prerelease {
			errs = append(errs, fmt.Errorf("release %s expected prerelease=%v, got %v",
				exp.Tag, exp.Prerelease, release.Prerelease))
		}

		if exp.Draft != release.Draft {
			errs = append(errs, fmt.Errorf("release %s expected draft=%v, got %v",
				exp.Tag, exp.Draft, release.Draft))
		}

		if exp.Latest && !release.Latest {
			errs = append(errs, fmt.Errorf("release %s expected latest=true, got false", exp.Tag))
		}
	}

	return errs
}

// AssertWorkflowExecution validates workflow execution against expectations
// Returns errors instead of failing test directly for more flexible error handling
func AssertWorkflowExecution(result *ExtendedWorkflowResult, expectJobs map[string]string) []error {
	var errs []error

	if result == nil {
		errs = append(errs, fmt.Errorf("workflow result is nil"))
		return errs
	}

	for jobName, expectedConclusion := range expectJobs {
		job := findJob(result.Jobs, jobName)
		if job == nil {
			// When act doesn't run a job (condition false), it's not in the output
			// Treat "not found" as equivalent to "skipped" when that's expected
			if expectedConclusion == "skipped" {
				continue // Not found = skipped, which is what we expect
			}
			errs = append(errs, fmt.Errorf("job %s expected %s but not found in result", jobName, expectedConclusion))
			continue
		}
		if job.Conclusion != expectedConclusion {
			errs = append(errs, fmt.Errorf("job %s expected conclusion=%s, got %s",
				jobName, expectedConclusion, job.Conclusion))
		}
	}

	return errs
}

// findJob looks up a job by name, handling act's job ID normalization
// Act sometimes strips prefixes like "build-" or "deploy-" from job IDs
func findJob(jobs map[string]*JobResultExtended, name string) *JobResultExtended {
	// Try exact match first
	if job, ok := jobs[name]; ok {
		return job
	}

	// Try stripping common prefixes (act normalizes these)
	prefixes := []string{"build-", "deploy-"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			stripped := strings.TrimPrefix(name, prefix)
			if job, ok := jobs[stripped]; ok {
				return job
			}
		}
	}

	return nil
}

// assertRollbackSource checks that the workflow logs contain the expected
// resolved-source marker emitted by the preflight "Report Resolved Source"
// step. The marker has the form "rollback resolved from <source>", where
// <source> is one of "state", "previous-ring", or "git-history".
//
// It is called from executeRollback when RollbackStep.ExpectSource is set, so
// scenarios can assert WHICH precedence path the resolver chose, not just the
// resulting SHA.
func assertRollbackSource(t testingT, logs string, wantSource string) error {
	t.Helper()
	marker := fmt.Sprintf("rollback resolved from %s", wantSource)
	if !strings.Contains(logs, marker) {
		t.Errorf("rollback logs did not contain resolved-source marker %q", marker)
		return fmt.Errorf("resolved-source marker %q not found in rollback logs", marker)
	}
	return nil
}

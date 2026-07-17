package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// baseScenarioYAML is a minimal well-formed multi-repo scenario. Each guard test
// splices its own expectation block onto this so the only variable under test is
// the block, not the scaffolding.
const baseScenarioYAML = `
name: guard-fixture
repos:
  primary:
    config:
      trunk_branch: main
      environments: [dev, prod]
      external:
        - repo: example/infra
          ref: main
          deploys:
            - name: cdk
              workflow: example/infra/.github/workflows/deploy.yaml
primary: primary
steps:
  - name: touch
    repo: primary
    action: commit
    commit:
      message: "chore: touch"
      files:
        a.txt: "a"
expect:
  repos:
    primary:
`

// TestParseMultiRepoScenario_RejectsPhantomStateKey pins the gap that strict
// decoding alone left open. RepoExpect.State was map[string]interface{}, and
// KnownFields cannot see inside a map: every key under it was opaque, so a
// typo'd expectation decoded cleanly and then asserted nothing. Typing the
// state subtree is what puts these keys back under the decoder's guard.
func TestParseMultiRepoScenario_RejectsPhantomStateKey(t *testing.T) {
	tests := []struct {
		name    string
		expect  string
		wantErr string
	}{
		{
			name: "phantom key at env level",
			expect: `
      state:
        dev:
          shaa: "typo"
`,
			wantErr: "shaa",
		},
		{
			name: "phantom key at external deploy level",
			expect: `
      state:
        dev:
          external:
            cdk:
              builds: "phantom"
`,
			wantErr: "builds",
		},
		{
			name: "phantom key at repo-expect level",
			expect: `
      stat:
        dev:
          sha: "x"
`,
			wantErr: "stat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMultiRepoScenario([]byte(baseScenarioYAML + tt.expect))
			require.Error(t, err, "a phantom key must be rejected, not silently dropped")
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestParseMultiRepoScenario_AcceptsEveryLegitimateKey is the counterweight to
// the guard tests: it enumerates the full legitimate key surface the checked-in
// corpus uses, so a rule tightened far enough to red a real scenario fails here
// first, in milliseconds, rather than in a Docker leg on some future PR.
func TestParseMultiRepoScenario_AcceptsEveryLegitimateKey(t *testing.T) {
	s, err := ParseMultiRepoScenario([]byte(baseScenarioYAML + `
      tags:
        - pattern: "v1.0.0-rc.0"
          on_sha: "abc"
      state:
        dev:
          sha: "dev-sha"
          version: "v1.0.0"
          external:
            cdk:
              sha: "cdk-sha"
              version: "v1.1.0"
              artifacts:
                image_tag: "tag-value"
`))
	require.NoError(t, err)

	got := s.Expect.Repos["primary"]
	require.Len(t, got.Tags, 1)
	assert.Equal(t, "v1.0.0-rc.0", got.Tags[0].Pattern)
	assert.Equal(t, "dev-sha", got.State["dev"].SHA)
	assert.Equal(t, "v1.0.0", got.State["dev"].Version)
	assert.Equal(t, "cdk-sha", got.State["dev"].External["cdk"].SHA)
	assert.Equal(t, "tag-value", got.State["dev"].External["cdk"].Artifacts["image_tag"])
}

// TestValidateMultiRepoScenario_RejectsUnfalsifiableSubjects is the multi-repo
// analog of validateStateSubjects. A typo'd env or external deploy name reads
// back as absent no matter how the product behaves, so the expectation can never
// fail. The name is decidable against the scenario's own config without running
// anything.
func TestValidateMultiRepoScenario_RejectsUnfalsifiableSubjects(t *testing.T) {
	tests := []struct {
		name    string
		expect  string
		wantErr string
	}{
		{
			name: "env the repo never declares",
			expect: `
      state:
        staging:
          sha: "x"
`,
			wantErr: "not an environment",
		},
		{
			name: "external deploy the repo never declares",
			expect: `
      state:
        dev:
          external:
            typo:
              sha: "x"
`,
			wantErr: "not an external deploy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseMultiRepoScenario([]byte(baseScenarioYAML + tt.expect))
			require.NoError(t, err, "the fixture must decode; the subject check is what should reject it")
			err = ValidateMultiRepoScenario(s)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestValidateMultiRepoScenario_RejectsZeroExpectations mirrors the guard
// AssertState carries on the single-repo side. A repo expectation with neither
// tags nor state asserts nothing and reports green unconditionally.
func TestValidateMultiRepoScenario_RejectsZeroExpectations(t *testing.T) {
	s, err := ParseMultiRepoScenario([]byte(baseScenarioYAML + "\n"))
	require.NoError(t, err)

	err = ValidateMultiRepoScenario(s)
	require.Error(t, err, "a repo expectation asserting nothing must be rejected")
	assert.Contains(t, err.Error(), "asserts nothing")
}

// TestValidateMultiRepoScenario_RejectsEmptyEnvExpectation closes this guard's
// own thesis one level down. The zero-expectation rule was checked at the repo
// level only, so `state: {dev: {}}` decoded and validated clean while asserting
// nothing: a state block naming a real env, proving nothing about it, passing
// unconditionally. Unlike `tags: []`, an empty env block states no claim at all
// (there is no "this env has no state" reading), so there is nothing to
// implement here and rejecting it is the honest rule.
func TestValidateMultiRepoScenario_RejectsEmptyEnvExpectation(t *testing.T) {
	s, err := ParseMultiRepoScenario([]byte(baseScenarioYAML + `
      state:
        dev: {}
`))
	require.NoError(t, err, "the fixture must decode; the emptiness check is what should reject it")

	err = ValidateMultiRepoScenario(s)
	require.Error(t, err, "an env expectation with no sha, version, or external asserts nothing")
	assert.Contains(t, err.Error(), "asserts nothing")
}

// TestValidateMultiRepoScenario_EmptyTagsIsAFalsifiableAssertion guards the
// distinction the zero-expectation rule depends on. An absent tags key asserts
// nothing, but `tags: []` is the corpus's way of saying "this repo has no tags
// yet", which is a real claim that a stray tag must break. yaml decodes the two
// differently (nil vs empty slice), so the rule can tell them apart.
func TestValidateMultiRepoScenario_EmptyTagsIsAFalsifiableAssertion(t *testing.T) {
	s, err := ParseMultiRepoScenario([]byte(baseScenarioYAML + "      tags: []\n"))
	require.NoError(t, err)

	require.NotNil(t, s.Expect.Repos["primary"].Tags, "`tags: []` must decode as an empty, non-nil slice")
	assert.NoError(t, ValidateMultiRepoScenario(s), "`tags: []` is an assertion, not an empty expectation")
}

// TestValidateMultiRepoScenario_RejectsNoSteps pins the same rule discovery
// applies on the single-repo side: a scenario that runs nothing asserts nothing.
func TestValidateMultiRepoScenario_RejectsNoSteps(t *testing.T) {
	s, err := ParseMultiRepoScenario([]byte(`
name: no-steps
repos:
  primary:
    config:
      trunk_branch: main
      environments: [dev]
primary: primary
`))
	require.NoError(t, err)

	err = ValidateMultiRepoScenario(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares no steps")
}

// TestRunSteps_ExpectedErrorThatSucceedsFails pins a real inversion. Expect.Error
// was read only on the failure branch, so a step that was supposed to fail and
// instead succeeded fell through to the success path and passed. That is a test
// that cannot fail in the direction it exists to test.
func TestRunSteps_ExpectedErrorThatSucceedsFails(t *testing.T) {
	scenario := &MultiRepoScenario{
		Name:    "expected-error-inversion",
		Primary: "primary",
		Steps: []ScenarioStep{{
			Name:   "this step succeeds but the scenario expects it to fail",
			Repo:   "primary",
			Action: "assert",
			Expect: &MultiRepoStepExpect{Error: "something that never happens"},
		}},
	}

	runner := &MultiRepoRunner{scenario: scenario}
	err := runner.RunSteps(context.Background())

	require.Error(t, err, "a step expected to fail that instead succeeded must fail the scenario")
	assert.Contains(t, err.Error(), "expected to fail")
}

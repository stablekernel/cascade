package generate

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boolPtr is a test helper that returns a pointer to a bool value.
func boolPtr(b bool) *bool { return &b }

// minimalCfg builds a TrunkConfig with one matrix-based deploy so that
// writeDeployJobs always emits a strategy: block.
func minimalCfg(deploys []config.DeployConfig) *config.TrunkConfig {
	return &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys:      deploys,
	}
}

// generateDeploy runs the generator and returns the full workflow string.
func generateDeploy(t *testing.T, cfg *config.TrunkConfig) string {
	t.Helper()
	g := NewPromoteGenerator(cfg, t.TempDir())
	out, err := g.Generate()
	require.NoError(t, err)
	return out
}

// deployStrategyBlock extracts the strategy: block lines (indented under the
// first job that has one) from the generated workflow string.
func deployStrategyBlock(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(out, "\n")
	var block strings.Builder
	inStrategy := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "strategy:" {
			inStrategy = true
			block.WriteString(line + "\n")
			continue
		}
		if inStrategy {
			// strategy: sub-keys are indented by at least 6 spaces (job-level = 4, sub = 6)
			if strings.HasPrefix(line, "      ") || strings.HasPrefix(line, "\t\t\t") {
				block.WriteString(line + "\n")
			} else {
				break
			}
		}
	}
	require.True(t, inStrategy, "no strategy: block found in generated output")
	return block.String()
}

// matrixDeploy returns a DeployConfig with inputs (triggers matrix strategy).
func matrixDeploy(name string, rollout *config.RolloutConfig) config.DeployConfig {
	return config.DeployConfig{
		Name:     name,
		Workflow: ".github/workflows/deploy-" + name + ".yaml",
		Inputs: map[string]interface{}{
			"environment": "${{ matrix.environment }}",
			"sha":         "${{ matrix.sha }}",
		},
		Rollout: rollout,
	}
}

// --- writeDeployStrategyOptions unit tests ---

func TestWriteDeployStrategyOptions_NilRollout(t *testing.T) {
	var sb strings.Builder
	g := &PromoteGenerator{}
	g.writeDeployStrategyOptions(&sb, nil)
	got := sb.String()
	assert.Contains(t, got, "fail-fast: false", "nil rollout must default to fail-fast: false")
	assert.NotContains(t, got, "max-parallel:", "nil rollout must not emit max-parallel")
}

func TestWriteDeployStrategyOptions_MaxParallelSet(t *testing.T) {
	var sb strings.Builder
	g := &PromoteGenerator{}
	rollout := &config.RolloutConfig{MaxParallel: 1}
	g.writeDeployStrategyOptions(&sb, rollout)
	got := sb.String()
	assert.Contains(t, got, "max-parallel: 1")
	assert.Contains(t, got, "fail-fast: false", "unset FailFast pointer must default to false")
}

func TestWriteDeployStrategyOptions_MaxParallelUnset(t *testing.T) {
	var sb strings.Builder
	g := &PromoteGenerator{}
	rollout := &config.RolloutConfig{} // MaxParallel == 0
	g.writeDeployStrategyOptions(&sb, rollout)
	got := sb.String()
	assert.NotContains(t, got, "max-parallel:", "MaxParallel==0 must not emit max-parallel line")
}

func TestWriteDeployStrategyOptions_FailFastTrue(t *testing.T) {
	var sb strings.Builder
	g := &PromoteGenerator{}
	rollout := &config.RolloutConfig{FailFast: boolPtr(true)}
	g.writeDeployStrategyOptions(&sb, rollout)
	got := sb.String()
	assert.Contains(t, got, "fail-fast: true")
	assert.NotContains(t, got, "fail-fast: false")
}

func TestWriteDeployStrategyOptions_FailFastFalse(t *testing.T) {
	var sb strings.Builder
	g := &PromoteGenerator{}
	rollout := &config.RolloutConfig{FailFast: boolPtr(false)}
	g.writeDeployStrategyOptions(&sb, rollout)
	got := sb.String()
	assert.Contains(t, got, "fail-fast: false")
	assert.NotContains(t, got, "fail-fast: true")
}

func TestWriteDeployStrategyOptions_BothSet(t *testing.T) {
	var sb strings.Builder
	g := &PromoteGenerator{}
	rollout := &config.RolloutConfig{MaxParallel: 3, FailFast: boolPtr(true)}
	g.writeDeployStrategyOptions(&sb, rollout)
	got := sb.String()
	assert.Contains(t, got, "max-parallel: 3")
	assert.Contains(t, got, "fail-fast: true")
}

// --- Integration tests via Generate() ---

func TestDeployStrategy_DefaultBehaviour(t *testing.T) {
	// No rollout config: must preserve historical default (fail-fast: false, no max-parallel)
	cfg := minimalCfg([]config.DeployConfig{matrixDeploy("app", nil)})
	out := generateDeploy(t, cfg)
	block := deployStrategyBlock(t, out)
	assert.Contains(t, block, "fail-fast: false")
	assert.NotContains(t, block, "max-parallel:")
}

func TestDeployStrategy_MaxParallelEmitted(t *testing.T) {
	cfg := minimalCfg([]config.DeployConfig{
		matrixDeploy("app", &config.RolloutConfig{MaxParallel: 2}),
	})
	out := generateDeploy(t, cfg)
	block := deployStrategyBlock(t, out)
	assert.Contains(t, block, "max-parallel: 2")
	assert.Contains(t, block, "fail-fast: false")
}

func TestDeployStrategy_RollingOneAtATime(t *testing.T) {
	// max_parallel: 1 models rolling region-by-region deploys
	cfg := minimalCfg([]config.DeployConfig{
		matrixDeploy("region", &config.RolloutConfig{MaxParallel: 1}),
	})
	out := generateDeploy(t, cfg)
	block := deployStrategyBlock(t, out)
	assert.Contains(t, block, "max-parallel: 1")
}

func TestDeployStrategy_FailFastTrue(t *testing.T) {
	cfg := minimalCfg([]config.DeployConfig{
		matrixDeploy("app", &config.RolloutConfig{FailFast: boolPtr(true)}),
	})
	out := generateDeploy(t, cfg)
	block := deployStrategyBlock(t, out)
	assert.Contains(t, block, "fail-fast: true")
	assert.NotContains(t, block, "fail-fast: false")
}

func TestDeployStrategy_FailFastFalseExplicit(t *testing.T) {
	cfg := minimalCfg([]config.DeployConfig{
		matrixDeploy("app", &config.RolloutConfig{FailFast: boolPtr(false)}),
	})
	out := generateDeploy(t, cfg)
	block := deployStrategyBlock(t, out)
	assert.Contains(t, block, "fail-fast: false")
	assert.NotContains(t, block, "fail-fast: true")
}

func TestDeployStrategy_MaxParallelAndFailFastCombined(t *testing.T) {
	cfg := minimalCfg([]config.DeployConfig{
		matrixDeploy("svc", &config.RolloutConfig{MaxParallel: 1, FailFast: boolPtr(true)}),
	})
	out := generateDeploy(t, cfg)
	block := deployStrategyBlock(t, out)
	assert.Contains(t, block, "max-parallel: 1")
	assert.Contains(t, block, "fail-fast: true")
}

func TestDeployStrategy_MaxParallelZeroOmitted(t *testing.T) {
	// MaxParallel == 0 (the zero value) must not emit max-parallel
	cfg := minimalCfg([]config.DeployConfig{
		matrixDeploy("app", &config.RolloutConfig{MaxParallel: 0, FailFast: boolPtr(false)}),
	})
	out := generateDeploy(t, cfg)
	block := deployStrategyBlock(t, out)
	assert.NotContains(t, block, "max-parallel:")
}

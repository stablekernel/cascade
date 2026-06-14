package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReusableWorkflowCallbackHasNoJobAttributes asserts that a reusable
// workflow: callback (jobs.<id>.uses) never carries runs-on / permissions /
// concurrency on the job. GHA forbids them there and schema validation rejects
// runs_on/concurrency on reusable callbacks. Only permissions is structurally
// accepted on the config, but the generator must not emit any of the three on a
// uses: job.
func TestReusableWorkflowCallbackHasNoJobAttributes(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:        "app",
				Workflow:    ".github/workflows/build.yaml",
				Triggers:    []string{"src/**"},
				Permissions: map[string]string{"contents": "read"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-app:")
	assert.Contains(t, job, "uses: ./.github/workflows/build.yaml")
	// None of the cascade-owned job attributes appear on a reusable uses: job.
	assert.NotContains(t, job, "runs-on:")
	assert.NotContains(t, job, "permissions:")
	assert.NotContains(t, job, "concurrency:")
}

// TestValidationRejectsJobAttributesOnReusableCallback confirms schema validation
// still rejects runs_on / concurrency on a reusable-workflow callback, so the
// generator never sees them there.
func TestValidationRejectsJobAttributesOnReusableCallback(t *testing.T) {
	runsOnCfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: "b.yaml", Triggers: []string{"src/**"}, RunsOn: &config.RunsOn{Label: "ubuntu-latest"}},
		},
	}
	assert.True(t, hasErr(config.Validate(runsOnCfg), "runs_on is not valid on a reusable-workflow callback"),
		"expected runs_on rejection on reusable callback")

	concCfg := &config.TrunkConfig{
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: "d.yaml", Triggers: []string{"src/**"}, Concurrency: &config.ConcurrencyConfig{Group: "g"}},
		},
	}
	assert.True(t, hasErr(config.Validate(concCfg), "concurrency is not valid on a reusable-workflow callback"),
		"expected concurrency rejection on reusable callback")
}

// hasErr reports whether any error string contains substr.
func hasErr(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

// jobSection returns the text of the job whose header line (e.g. "build-app:")
// starts the section, up to the next top-level (two-space-indented) job header.
func jobSection(yaml, header string) string {
	lines := strings.Split(yaml, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header && strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(strings.TrimSpace(l), ":") && len(strings.TrimSpace(l)) > 1 {
			// next job header at the same two-space indent
			trimmed := strings.TrimLeft(l, " ")
			if len(l)-len(trimmed) == 2 {
				end = i
				break
			}
		}
	}
	return strings.Join(lines[start:end], "\n")
}

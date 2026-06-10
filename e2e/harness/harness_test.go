package harness

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHarness_RunScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	scenario := &Scenario{
		Name:        "Simple test",
		Description: "Tests basic workflow execution",
		Setup: Setup{
			Config: Config{
				TrunkBranch:  "main",
				Environments: []string{"dev", "prod"},
			},
			Commits: []Commit{
				{
					Message: "feat: initial",
					Files: map[string]string{
						"README.md": "# Test",
					},
				},
			},
		},
		Trigger: Trigger{
			Workflow: "test.yaml",
			Event:    "push",
		},
		Expect: Expect{
			Workflow: WorkflowExpect{
				Conclusion: "success",
			},
		},
	}

	h := New(t)
	defer h.Cleanup()

	err := h.SetupInfra(ctx)
	require.NoError(t, err)

	err = h.StageRepo(ctx, scenario.Setup)
	require.NoError(t, err)
}

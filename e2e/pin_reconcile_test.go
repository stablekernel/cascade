package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/e2e/harness"
	"github.com/stablekernel/cascade/internal/config"
)

// bumpedCheckoutRef is the synthetic sha/version pair the scenario substitutes
// for the real compiled-in checkout pin, simulating an external governed-pin
// bump (the shape of a merged Dependabot update) landing in the generated
// orchestrate.yaml. It is obviously fake so a false-positive match against the
// real pin table is impossible.
const bumpedCheckoutRef = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee # v99.99.99"

// TestReconcileAdoptsBumpAndSurvivesRegen proves cascade reconcile's
// user-facing contract end to end: an external governed-pin bump landing in an
// already-generated workflow is adopted into the manifest's action_pins and
// the resulting regenerate carries that same pin, so the bump SURVIVES
// regeneration rather than drifting back out on the next generate. The
// scenario stages a real two-environment pipeline, mutates the generated
// orchestrate.yaml's checkout pin in place (simulating the bump), runs
// `cascade reconcile`, and requires both that the regenerated file carries the
// adopted pin and that a subsequent `cascade verify` stays clean; any
// divergence between the adopted manifest and the regenerated workflow would
// fail verify, so a clean verify is the proof the adoption is real rather than
// cosmetic.
func TestReconcileAdoptsBumpAndSurvivesRegen(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E tests")
	}
	requireShardOwns(t)

	cfg := harness.Config{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		PinMode:      config.PinModeSHA,
		Builds: []config.BuildConfig{
			{
				Name:     "build",
				Workflow: ".github/workflows/build.yaml",
				Triggers: []string{"src/**"},
			},
		},
		Deploys: []config.DeployConfig{
			{
				Name:     "deploy",
				Workflow: ".github/workflows/deploy.yaml",
			},
		},
	}

	scenario := &harness.MultiStepScenario{
		Name:        "Reconcile Adopts A Governed Pin Bump And Survives Regen",
		Description: "an external checkout pin bump landing in orchestrate.yaml is adopted by cascade reconcile and survives a regenerate",
		Config:      cfg,
		Steps: []harness.Step{
			{
				Name:   "Reconcile the bumped checkout pin",
				Action: "reconcile",
				Reconcile: &harness.ReconcileStep{
					MutatePath:     ".github/workflows/orchestrate.yaml",
					MutateFind:     "actions/checkout@.*",
					MutateReplace:  "actions/checkout@" + bumpedCheckoutRef,
					ExpectExit:     0,
					ExpectContains: []string{"actions/checkout@" + bumpedCheckoutRef},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	err := harness.RunMultiStepScenario(ctx, t, scenario)
	require.NoError(t, err, "reconcile adoption scenario failed")
}

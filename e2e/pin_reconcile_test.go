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

// TestReconcileCompanionAppendsOnSameRepoPR proves the emitted
// cascade-reconcile-companion.yaml end to end, under act, against a real
// Gitea repository: a same-repo pull request carries a simulated external
// governed-pin bump (the shape of a merged Dependabot update) landing in the
// generated orchestrate.yaml, and driving the real companion workflow through
// act with a real workflow_run event exercises the relevance trigger, the
// trusted-metadata pull request resolution, the head-as-data checkout of the
// pull request branch, the pinned cascade binary, the real (idempotent)
// `cascade reconcile` adoption, and the push back onto the pull request's own
// branch (the default "append" commit mode). A clean pass requires the
// pushed branch to carry both the adopted action_pins entry and a regenerated
// orchestrate.yaml that still carries the bumped pin, proving the adoption
// survives the companion's own regenerate rather than being cosmetic.
func TestReconcileCompanionAppendsOnSameRepoPR(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E tests")
	}
	requireShardOwns(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	result, err := harness.RunReconcileCompanionAppendScenario(ctx, t)
	require.NoError(t, err, "reconcile companion scenario failed")

	require.Equal(t, "success", result.Conclusion, "the companion's reconcile job must conclude successfully")
	require.NotEqual(t, result.HeadBefore, result.HeadAfter,
		"the companion must push an adoption commit onto the pull request's own branch")
	require.Contains(t, result.ManifestAfter, harness.ReconcileCompanionBumpTag,
		"the adopted action_pins entry must carry the bumped ref verbatim")
	require.Contains(t, result.OrchestrateAfter, "actions/checkout@"+harness.ReconcileCompanionBumpTag,
		"the regenerated orchestrate.yaml must still carry the bumped pin, proving the adoption survives regeneration")
}

package rollback

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// The first environment tracks trunk and is never promoted into, so its
// deploy-history ring is structurally always empty. A rollback there would
// resolve a target from an empty or stale ring, which is a silent wrong target.
// The guard makes that case fail fast with an actionable error. dev is the first
// environment in the manifest writeManifest builds; prod is a promoted env.

func TestPlan_FirstEnvironment_NoTarget_Guarded(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	_, err := rb.Plan("dev", "", "")
	if err == nil {
		t.Fatalf("expected guard error rolling back the first environment, got nil")
		return
	}
	if !strings.Contains(err.Error(), "first environment") {
		t.Errorf("error = %q, want it to name the first environment", err.Error())
	}
	if !strings.Contains(err.Error(), "dev") {
		t.Errorf("error = %q, want it to name the env (dev)", err.Error())
	}
	if !strings.Contains(err.Error(), "trunk") {
		t.Errorf("error = %q, want it to point at the trunk revert path", err.Error())
	}
}

func TestPlan_FirstEnvironment_WithTarget_Guarded(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	// Even an explicit --to value that matches the first env's live state must be
	// refused: the trunk-tracking env reverts via a merge, not a ring rollback.
	_, err := rb.Plan("dev", "devsha1234567", "")
	if err == nil {
		t.Fatalf("expected guard error rolling back the first environment with --to, got nil")
		return
	}
	if !strings.Contains(err.Error(), "first environment") {
		t.Errorf("error = %q, want it to name the first environment", err.Error())
	}
}

func TestPlan_FirstEnvironment_Deployable_Guarded(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	_, err := rb.Plan("dev", "", "services")
	if err == nil {
		t.Fatalf("expected guard error for a deployable-scoped first-env rollback, got nil")
		return
	}
	if !strings.Contains(err.Error(), "first environment") {
		t.Errorf("error = %q, want it to name the first environment", err.Error())
	}
}

func TestPlan_PromotedEnvironment_NotGuarded(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	// prod is a promoted (non-first) env: the guard must not fire and the
	// no-target default path must still resolve through the normal sources.
	if _, err := rb.Plan("prod", "v1.9.0", ""); err != nil {
		t.Fatalf("Plan on a promoted env should not be guarded: %v", err)
	}
}

func TestApply_FirstEnvironment_Guarded(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "prodsha9999999", "v1.9.0")
	rb := newRollbacker(t, path, fakeHistory{})

	// A plan constructed directly for the first env (bypassing Plan) must still be
	// refused by Apply: the guard is defense in depth on the only mutating path.
	plan := &Plan{
		Environment: "dev",
		Target:      Target{SHA: "devsha1234567", Version: "v2.0.0-rc.1", Source: "state"},
	}
	err := rb.Apply(plan)
	if err == nil {
		t.Fatalf("expected Apply to refuse a first-environment rollback, got nil")
		return
	}
	if !strings.Contains(err.Error(), "first environment") {
		t.Errorf("error = %q, want it to name the first environment", err.Error())
	}
}

// Guards must be inert when there is no parsed config to identify the first
// environment, so a state-only manifest still resolves through the normal path.
func TestFirstEnvErr_NoConfig_Inert(t *testing.T) {
	rb := &Rollbacker{cicdFile: &config.CICDFile{}}
	if err := rb.firstEnvErr("dev"); err != nil {
		t.Errorf("firstEnvErr with no config should be nil, got %v", err)
	}
}

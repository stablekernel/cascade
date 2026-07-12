package config

import "testing"

// TestValidate_ReservedNotWired_Rejected asserts that populating a field which
// parses but is not wired to generation is a hard error with the category-(a)
// message. These fields are shape-only today; lint must reject their use rather
// than silently accept an inert manifest.
func TestValidate_ReservedNotWired_Rejected(t *testing.T) {
	const notWired = "not implemented in this cascade version"
	tests := []struct {
		name     string
		manifest string
		wantPath string
	}{
		{
			name: "telemetry",
			manifest: `
telemetry:
  enabled: true
  adapter: none
builds:
  - name: app
    workflow: b.yaml
`,
			wantPath: "telemetry",
		},
		{
			name: "rollout.type",
			manifest: `
deploys:
  - name: app
    workflow: d.yaml
    rollout:
      type: canary
`,
			wantPath: "deploys[0].rollout.type",
		},
		{
			name: "rollout.canary",
			manifest: `
deploys:
  - name: app
    workflow: d.yaml
    rollout:
      canary:
        percent: 10
`,
			wantPath: "deploys[0].rollout.canary",
		},
		{
			name: "rollout.blue_green",
			manifest: `
deploys:
  - name: app
    workflow: d.yaml
    rollout:
      blue_green:
        switch: .github/workflows/switch.yaml
`,
			wantPath: "deploys[0].rollout.blue_green",
		},
		{
			name: "deploy_target",
			manifest: `
deploys:
  - name: app
    workflow: d.yaml
    deploy_target:
      mode: gitops
      repo: org/gitops
`,
			wantPath: "deploys[0].deploy_target",
		},
		{
			name: "release_build.version_overrides",
			manifest: `
builds:
  - name: app
    workflow: b.yaml
release_build:
  version_overrides:
    dir: .cascade/version-overrides
`,
			wantPath: "release_build.version_overrides",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(parseInline(t, tt.manifest))
			if !hasErrContaining(errs, notWired) {
				t.Fatalf("expected category-(a) %q message, got %v", notWired, errs)
			}
			if !hasErrContaining(errs, tt.wantPath) {
				t.Fatalf("expected error naming %q, got %v", tt.wantPath, errs)
			}
		})
	}
}

// TestValidate_ReservedNotWired_Component asserts the reserved-field walk reaches
// into components, so a reserved field on a component callback is rejected the
// same as at the top level.
func TestValidate_ReservedNotWired_Component(t *testing.T) {
	cfg := parseInline(t, `
schema_version: 1
components:
  api:
    path: services/api
    deploys:
      - name: api
        workflow: d.yaml
        deploy_target:
          mode: gitops
          repo: org/gitops
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, "components.api.deploys[0].deploy_target") {
		t.Fatalf("expected reserved field on component callback to be rejected, got %v", errs)
	}
	if !hasErrContaining(errs, "not implemented in this cascade version") {
		t.Fatalf("expected category-(a) message for component reserved field, got %v", errs)
	}
}

// TestValidate_ReservedWrongPlace_Rejected asserts callback timeout_minutes is a
// hard error with the category-(b) message: the timeout belongs in the called
// workflow, not on the caller job.
func TestValidate_ReservedWrongPlace_Rejected(t *testing.T) {
	const wrongPlace = "timeout belongs in the called workflow, not the caller"
	tests := []struct {
		name     string
		manifest string
	}{
		{
			name: "build callback",
			manifest: `
builds:
  - name: app
    workflow: b.yaml
    timeout_minutes: 15
`,
		},
		{
			name: "deploy callback",
			manifest: `
deploys:
  - name: app
    workflow: d.yaml
    timeout_minutes: 15
`,
		},
		{
			name: "validate callback",
			manifest: `
validate:
  workflow: v.yaml
  timeout_minutes: 15
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(parseInline(t, tt.manifest))
			if !hasErrContaining(errs, wrongPlace) {
				t.Fatalf("expected category-(b) %q message, got %v", wrongPlace, errs)
			}
		})
	}
}

// TestValidate_ConsumedRolloutFields_Clean is the guard: rollout knobs that ARE
// wired to generation (max_parallel, fail_fast) must never be swept up by the
// reserved-field rejection. Only the inert rollout members (type, canary,
// blue_green) are reserved.
func TestValidate_ConsumedRolloutFields_Clean(t *testing.T) {
	cfg := parseInline(t, `
deploys:
  - name: app
    workflow: d.yaml
    inputs:
      region: us-east-1
    rollout:
      max_parallel: 2
      fail_fast: true
`)
	for _, e := range Validate(cfg) {
		if containsAny(e, "reserved", "not implemented in this cascade version") {
			t.Fatalf("consumed rollout knobs must validate clean, got %v", e)
		}
	}
}

// TestReservedFieldRegistry_MessagesMatchCategory guards the documented registry
// against drift: every entry must render its category's operator-facing message,
// so the field-path -> category -> message mapping stays internally consistent.
func TestReservedFieldRegistry_MessagesMatchCategory(t *testing.T) {
	if len(reservedFieldRegistry) == 0 {
		t.Fatal("reserved field registry must not be empty")
	}
	for _, f := range reservedFieldRegistry {
		msg := f.category.errorFor(f.path)
		switch f.category {
		case reservedWrongPlace:
			if !hasErrContaining([]string{msg}, "timeout belongs in the called workflow, not the caller") {
				t.Errorf("%s: category (b) message wrong: %q", f.path, msg)
			}
		case reservedNotWired:
			if !hasErrContaining([]string{msg}, "not implemented in this cascade version") {
				t.Errorf("%s: category (a) message wrong: %q", f.path, msg)
			}
		default:
			t.Errorf("%s: unknown reserved category %d", f.path, f.category)
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if hasErrContaining([]string{s}, sub) {
			return true
		}
	}
	return false
}

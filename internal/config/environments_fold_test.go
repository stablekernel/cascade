package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestEnvironmentsFold_ObjectFormReachesConfig proves an environments entry in
// the object form carries its inline per-environment settings, folding what used
// to live in the separate environment_config map. The settings must reach the
// same accessors the generator and the environments command consume.
func TestEnvironmentsFold_ObjectFormReachesConfig(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments:
  - dev
  - name: prod
    gha_environment: production
    wait_timer: 10
    branch_policy: protected
    required_reviewers: [octocat, team/ops]
    secrets: [DB_PASSWORD]
    variables: [REGION]
    environment_url: https://app.example.com
`)
	if got := cfg.EnvironmentNames(); len(got) != 2 || got[0] != "dev" || got[1] != "prod" {
		t.Fatalf("EnvironmentNames = %v, want [dev prod]", got)
	}
	// The config-free bare-string entry carries no settings.
	dev, ok := cfg.EnvConfig("dev")
	if !ok || !dev.isZero() {
		t.Fatalf("dev entry should be config-free, got %+v (ok=%v)", dev, ok)
	}
	prod, ok := cfg.EnvConfig("prod")
	if !ok {
		t.Fatal("prod entry missing")
	}
	if prod.GHAEnvironment != "production" {
		t.Errorf("gha_environment = %q, want production", prod.GHAEnvironment)
	}
	if prod.WaitTimerMinutes() != 10 {
		t.Errorf("wait_timer = %d, want 10", prod.WaitTimerMinutes())
	}
	if prod.BranchPolicy != "protected" {
		t.Errorf("branch_policy = %q, want protected", prod.BranchPolicy)
	}
	if prod.EnvironmentURL != "https://app.example.com" {
		t.Errorf("environment_url = %q", prod.EnvironmentURL)
	}
	if len(prod.RequiredReviewers) != 2 || len(prod.Secrets) != 1 || len(prod.Variables) != 1 {
		t.Errorf("reviewers/secrets/variables not parsed: %+v", prod)
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
}

// TestEnvironmentsFold_BareStringSugar proves the bare-string list still parses,
// yielding config-free entries in order.
func TestEnvironmentsFold_BareStringSugar(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, staging, prod]
`)
	if got := cfg.EnvironmentNames(); strings.Join(got, ",") != "dev,staging,prod" {
		t.Fatalf("EnvironmentNames = %v", got)
	}
	for _, e := range cfg.Environments {
		if !e.isZero() || e.Role != "" {
			t.Errorf("bare entry %q should be config-free and role-free", e.Name)
		}
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
}

// TestEnvironmentsFold_RejectsLegacyEnvironmentConfig proves the removed
// top-level environment_config map is now an unknown key with a did-you-mean that
// points at the environments entries.
func TestEnvironmentsFold_RejectsLegacyEnvironmentConfig(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
environment_config:
  prod:
    gha_environment: production
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, `unknown field "environment_config"; did you mean "environments"`) {
		t.Fatalf("expected environment_config did-you-mean pointing at environments, got %v", errs)
	}
}

// TestEnvironmentsFold_RejectsUnknownEntryField proves an unmodeled key on an
// environments entry is a hard error with a suggestion.
func TestEnvironmentsFold_RejectsUnknownEntryField(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments:
  - name: prod
    gha_env: production
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, `environments[0] has unknown field "gha_env"`) {
		t.Fatalf("expected unknown-entry-field error, got %v", errs)
	}
}

// TestEnvironmentsFold_DuplicateAndEmptyNames proves duplicate and empty
// environment names are rejected.
func TestEnvironmentsFold_DuplicateAndEmptyNames(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments:
  - dev
  - dev
`)
	if errs := Validate(cfg); !hasErrContaining(errs, "duplicate environment name: dev") {
		t.Fatalf("expected duplicate-name error, got %v", errs)
	}
}

// TestEnvironmentsRole_OverridesPositionalDefault proves an explicit role moves
// the release/prerelease markers while an unset role keeps the index-based
// default (last = release, second-from-last = prerelease).
func TestEnvironmentsRole_OverridesPositionalDefault(t *testing.T) {
	// Positional default: no roles declared.
	def := parseInline(t, `
trunk_branch: main
environments: [dev, staging, prod]
`)
	if got := def.ReleaseEnvironment(); got != "prod" {
		t.Errorf("default release env = %q, want prod (last)", got)
	}
	if got := def.PrereleaseEnvironment(); got != "staging" {
		t.Errorf("default prerelease env = %q, want staging (second-from-last)", got)
	}

	// Explicit roles override position: release is NOT the last entry here.
	roled := parseInline(t, `
trunk_branch: main
environments:
  - name: dev
  - name: staging
    role: release
  - name: prod
    role: prerelease
`)
	if got := roled.ReleaseEnvironment(); got != "staging" {
		t.Errorf("role release env = %q, want staging", got)
	}
	if got := roled.PrereleaseEnvironment(); got != "prod" {
		t.Errorf("role prerelease env = %q, want prod", got)
	}
	if errs := Validate(roled); len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}

	// The explicit role flows into the promotion graph: promoting to staging is
	// the release crossing, not promoting to the last entry (prod).
	var relToStaging, relToProd bool
	for _, opt := range roled.GetAllDirectPromotionOptions() {
		if opt.ToEnv == "staging" && opt.IsRelease {
			relToStaging = true
		}
		if opt.ToEnv == "prod" && opt.IsRelease {
			relToProd = true
		}
	}
	if !relToStaging {
		t.Error("promotion to role:release env (staging) must be a release crossing")
	}
	if relToProd {
		t.Error("promotion to prod must NOT be a release crossing when role:release is elsewhere")
	}
}

// TestEnvironmentsRole_RejectsInvalidAndDuplicate proves an unknown role value
// and duplicate release/prerelease roles are rejected.
func TestEnvironmentsRole_RejectsInvalidAndDuplicate(t *testing.T) {
	bad := parseInline(t, `
trunk_branch: main
environments:
  - name: dev
    role: staging
`)
	if errs := Validate(bad); !hasErrContaining(errs, "role must be one of") {
		t.Fatalf("expected invalid-role error, got %v", errs)
	}

	dup := parseInline(t, `
trunk_branch: main
environments:
  - name: dev
    role: release
  - name: prod
    role: release
`)
	if errs := Validate(dup); !hasErrContaining(errs, "at most one environment may declare role: release") {
		t.Fatalf("expected duplicate-release-role error, got %v", errs)
	}
}

// TestEnvironmentsFold_ComponentSubsetStillValidates proves a component can still
// narrow the ladder via either bare strings or objects, and the resolved subset
// whole-replaces the inherited list (settings and all), preserving the pre-fold
// component environments behavior.
func TestEnvironmentsFold_ComponentSubsetStillValidates(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments:
  - dev
  - name: staging
    gha_environment: staging-shared
  - name: prod
    gha_environment: production
components:
  worker:
    path: services/worker
    tag_grammar:
      prefix: worker-
    environments:
      - dev
      - name: prod
        gha_environment: worker-prod
`)
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
	rc, err := cfg.ResolveComponent("worker")
	if err != nil {
		t.Fatalf("ResolveComponent: %v", err)
	}
	if got := rc.Config.EnvironmentNames(); strings.Join(got, ",") != "dev,prod" {
		t.Fatalf("component ladder = %v, want [dev prod]", got)
	}
	// Whole-replace: the component's prod entry carries ITS gha_environment, and
	// staging (dropped by the subset) is gone rather than lingering.
	prod, ok := rc.Config.EnvConfig("prod")
	if !ok || prod.GHAEnvironment != "worker-prod" {
		t.Errorf("component prod gha_environment = %q, want worker-prod", prod.GHAEnvironment)
	}
	if _, ok := rc.Config.EnvConfig("staging"); ok {
		t.Error("staging must not survive the component subset whole-replace")
	}
}

// TestEnvironmentsFold_MarshalRoundTrip proves parse-then-marshal is faithful: a
// config-free entry collapses back to the bare-string sugar, while a configured
// entry marshals to a mapping.
func TestEnvironmentsFold_MarshalRoundTrip(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments:
  - dev
  - name: prod
    gha_environment: production
    role: release
`)
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "- dev\n") {
		t.Errorf("config-free entry should marshal to bare string, got:\n%s", s)
	}
	if !strings.Contains(s, "name: prod") || !strings.Contains(s, "gha_environment: production") || !strings.Contains(s, "role: release") {
		t.Errorf("configured entry should marshal to a mapping, got:\n%s", s)
	}
	// Round trip is stable.
	var back TrunkConfig
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if strings.Join(back.EnvironmentNames(), ",") != "dev,prod" {
		t.Fatalf("round-trip names = %v", back.EnvironmentNames())
	}
	if p, _ := back.EnvConfig("prod"); p.GHAEnvironment != "production" {
		t.Errorf("round-trip lost prod config: %+v", p)
	}
}

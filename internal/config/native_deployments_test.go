package config

import "testing"

// TestParseDeployments proves the opt-in deployments block and the per-env
// environment_url field parse from a manifest.
func TestParseDeployments(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments:
  - name: production
    environment_url: "https://app.example.com"
deployments:
  enabled: true
  keep_prior_active: true
`)
	if !cfg.Deployments.IsEnabled() || !cfg.Deployments.KeepsPriorActive() {
		t.Fatalf("deployments: %#v", cfg.Deployments)
	}
	if len(cfg.Environments) != 1 || cfg.Environments[0].Name != "production" {
		t.Fatalf("environments missing production: %#v", cfg.Environments)
	}
	ec := cfg.Environments[0].EnvironmentConfig
	if ec.EnvironmentURL != "https://app.example.com" {
		t.Fatalf("environment_url: %q", ec.EnvironmentURL)
	}
}

// TestDeploymentsValidatesAtCurrentSchemaVersion proves the deployments toggle
// and environment_url are additive: a manifest that sets them validates cleanly
// at CurrentSchemaVersion, confirming the schema version was not bumped.
func TestDeploymentsValidatesAtCurrentSchemaVersion(t *testing.T) {
	cfg := &TrunkConfig{
		SchemaVersion: CurrentSchemaVersion,
		TrunkBranch:   "main",
		Environments: []EnvironmentEntry{
			{Name: "production", EnvironmentConfig: EnvironmentConfig{EnvironmentURL: "https://app.example.com"}},
		},
		Deployments: &DeploymentsConfig{Enabled: boolPtr(true), KeepPriorActive: boolPtr(true)},
	}
	for _, e := range Validate(cfg) {
		t.Fatalf("unexpected validation error for deployments at current schema version: %s", e)
	}
}

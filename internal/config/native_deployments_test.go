package config

import "testing"

// TestParseDeployments proves the opt-in deployments block and the per-env
// environment_url field parse from a manifest.
func TestParseDeployments(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [production]
deployments:
  enabled: true
  keep_prior_active: true
environment_config:
  production:
    environment_url: "https://app.example.com"
`)
	if !cfg.Deployments.IsEnabled() || !cfg.Deployments.KeepsPriorActive() {
		t.Fatalf("deployments: %#v", cfg.Deployments)
	}
	ec, ok := cfg.EnvironmentConfig["production"]
	if !ok {
		t.Fatalf("environment_config missing production: %#v", cfg.EnvironmentConfig)
	}
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
		Environments:  []string{"production"},
		Deployments:   &DeploymentsConfig{Enabled: boolPtr(true), KeepPriorActive: boolPtr(true)},
		EnvironmentConfig: map[string]EnvironmentConfig{
			"production": {EnvironmentURL: "https://app.example.com"},
		},
	}
	for _, e := range Validate(cfg) {
		t.Fatalf("unexpected validation error for deployments at current schema version: %s", e)
	}
}

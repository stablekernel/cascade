package config

import "testing"

// TestValidateTelemetryReservedFields exercises the newly reserved telemetry
// fields (webhook.url, webhook.secret_name). Validation is intentionally lenient
// and applies only when webhook is present; adapter is never enum-checked so an
// arbitrary adapter string keeps parsing and validating.
func TestValidateTelemetryReservedFields(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name        string
		telemetry   *TelemetryConfig
		wantErr     bool
		errContains string
	}{
		{
			name:      "nil telemetry is valid",
			telemetry: nil,
			wantErr:   false,
		},
		{
			name:      "enabled with no webhook is valid",
			telemetry: &TelemetryConfig{Enabled: true, Adapter: TelemetryAdapterNone},
			wantErr:   false,
		},
		{
			name: "arbitrary adapter still validates (no enum enforcement)",
			telemetry: &TelemetryConfig{
				Enabled: true,
				Adapter: "some-future-vendor",
			},
			wantErr: false,
		},
		{
			name: "full webhook with https url and safe secret name is valid",
			telemetry: &TelemetryConfig{
				Enabled: true,
				Adapter: TelemetryAdapterNone,
				Webhook: &TelemetryWebhook{
					URL:        "https://metrics.example.com/ingest",
					SecretName: "TELEMETRY_TOKEN",
				},
				JobSummary: boolPtr(true),
			},
			wantErr: false,
		},
		{
			name: "http url is accepted",
			telemetry: &TelemetryConfig{
				Webhook: &TelemetryWebhook{URL: "http://localhost:9000/ingest"},
			},
			wantErr: false,
		},
		{
			name: "non-http url is rejected",
			telemetry: &TelemetryConfig{
				Webhook: &TelemetryWebhook{URL: "ftp://metrics.example.com"},
			},
			wantErr:     true,
			errContains: "telemetry.webhook.url must be an http(s) URL",
		},
		{
			name: "secret name with dollar interpolation is rejected",
			telemetry: &TelemetryConfig{
				Webhook: &TelemetryWebhook{SecretName: "${{ secrets.X }}"},
			},
			wantErr:     true,
			errContains: "telemetry.webhook.secret_name must be a valid GitHub Actions secret name",
		},
		{
			name: "secret name starting with a digit is rejected",
			telemetry: &TelemetryConfig{
				Webhook: &TelemetryWebhook{SecretName: "1TOKEN"},
			},
			wantErr:     true,
			errContains: "secret_name must be a valid GitHub Actions secret name",
		},
		{
			name: "empty webhook fields are valid (shape only)",
			telemetry: &TelemetryConfig{
				Webhook: &TelemetryWebhook{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			errs := validateTelemetry(tt.telemetry)
			if tt.wantErr {
				if len(errs) == 0 {
					t.Fatalf("expected an error, got none")
				}
				if !hasErrContaining(errs, tt.errContains) {
					t.Fatalf("expected error containing %q, got %v", tt.errContains, errs)
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("expected no errors, got %v", errs)
			}
		})
	}
}

// TestParseTelemetryReservedFields asserts a manifest carrying the enriched
// telemetry shape (webhook, job_summary) parses into the typed fields, validates
// at CurrentSchemaVersion, and does not bump schema_version.
func TestParseTelemetryReservedFields(t *testing.T) {
	t.Parallel()

	cfg := parseInline(t, `
environments: [dev, prod]
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
telemetry:
  enabled: true
  adapter: none
  webhook:
    url: https://metrics.example.com/ingest
    secret_name: TELEMETRY_TOKEN
  job_summary: true
`)
	if cfg.Telemetry == nil {
		t.Fatalf("telemetry block did not parse")
		return
	}
	if !cfg.Telemetry.Enabled || cfg.Telemetry.Adapter != TelemetryAdapterNone {
		t.Fatalf("telemetry enabled/adapter: %#v", cfg.Telemetry)
	}
	if cfg.Telemetry.Webhook == nil {
		t.Fatalf("telemetry.webhook did not parse")
		return
	}
	if cfg.Telemetry.Webhook.URL != "https://metrics.example.com/ingest" {
		t.Fatalf("telemetry.webhook.url: %q", cfg.Telemetry.Webhook.URL)
	}
	if cfg.Telemetry.Webhook.SecretName != "TELEMETRY_TOKEN" {
		t.Fatalf("telemetry.webhook.secret_name: %q", cfg.Telemetry.Webhook.SecretName)
	}
	if cfg.Telemetry.JobSummary == nil || !*cfg.Telemetry.JobSummary {
		t.Fatalf("telemetry.job_summary: %#v", cfg.Telemetry.JobSummary)
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if got := cfg.GetSchemaVersion(); got != CurrentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d (reserved shape must not bump)", got, CurrentSchemaVersion)
	}
	if CurrentSchemaVersion != 1 {
		t.Fatalf("CurrentSchemaVersion = %d, want 1 (reserved shape must not bump)", CurrentSchemaVersion)
	}
}

// TestParseTelemetryJobSummaryUnsetVsFalse confirms the *bool pointer
// distinguishes an unset job_summary from an explicit false.
func TestParseTelemetryJobSummaryUnsetVsFalse(t *testing.T) {
	t.Parallel()

	unset := parseInline(t, `
environments: [dev]
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
telemetry:
  enabled: true
`)
	if unset.Telemetry == nil || unset.Telemetry.JobSummary != nil {
		t.Fatalf("unset job_summary should be nil, got %#v", unset.Telemetry)
	}

	explicitFalse := parseInline(t, `
environments: [dev]
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
telemetry:
  enabled: true
  job_summary: false
`)
	if explicitFalse.Telemetry == nil || explicitFalse.Telemetry.JobSummary == nil {
		t.Fatalf("explicit job_summary: false should parse to a non-nil pointer, got %#v", explicitFalse.Telemetry)
	}
	if *explicitFalse.Telemetry.JobSummary {
		t.Fatalf("explicit job_summary: false should be false")
	}
}

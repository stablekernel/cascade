package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// writeTelemetryPlanManifest writes the determinism manifest, optionally enriched
// with the reserved telemetry block (webhook + job_summary), into a temp repo and
// returns the repo root. It reuses the determinism workflow stubs so the plan set
// is substantial and order-sensitive emission paths are exercised.
func writeTelemetryPlanManifest(t *testing.T, telemetry *config.TelemetryConfig) string {
	t.Helper()
	dir := writeDeterminismWorkflows(t)

	cfg := determinismConfig()
	cfg.Telemetry = telemetry

	manifest := map[string]any{
		config.DefaultManifestKey: config.CICDFile{Config: cfg},
	}
	body, err := yaml.Marshal(manifest)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "manifest.yaml"), body, 0o644))
	return dir
}

// TestTelemetryReserved_PlanRejected asserts the generate path refuses a manifest
// that populates the reserved telemetry block. telemetry parses and structurally
// validates but is not wired to generation, so lint (which the Plan path runs)
// rejects it rather than emitting an inert plan. This exercises the rejection at
// the generator entrypoint, complementing the config-layer coverage.
func TestTelemetryReserved_PlanRejected(t *testing.T) {
	jobSummary := true
	dir := writeTelemetryPlanManifest(t, &config.TelemetryConfig{
		Enabled: true,
		Adapter: config.TelemetryAdapterNone,
		Webhook: &config.TelemetryWebhook{
			URL:        "https://metrics.example.com/ingest",
			SecretName: "TELEMETRY_TOKEN",
		},
		JobSummary: &jobSummary,
	})
	chdir(t, dir)

	_, err := Plan(PlanOptions{
		ConfigPath:        ".github/manifest.yaml",
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        ".github/workflows/orchestrate.yaml",
		PromoteOutputPath: ".github/workflows/promote.yaml",
	})
	require.Error(t, err, "Plan must reject a manifest that uses the reserved telemetry block")
	require.Contains(t, err.Error(), "telemetry is reserved and not implemented in this cascade version")
}

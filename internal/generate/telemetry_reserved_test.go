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

// telemetryPlan runs Plan rooted at dir and returns the emitted set keyed by the
// path relative to dir. Plan mixes relative (workflow) and absolute (composite
// action under baseDir) paths, so paths are normalized against the resolved dir
// to make two runs in different temp dirs comparable.
func telemetryPlan(t *testing.T, dir string) map[string]string {
	t.Helper()
	chdir(t, dir)

	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	planned, err := Plan(PlanOptions{
		ConfigPath:        ".github/manifest.yaml",
		ManifestKey:       config.DefaultManifestKey,
		ActionFolder:      "manage-release",
		OutputPath:        ".github/workflows/orchestrate.yaml",
		PromoteOutputPath: ".github/workflows/promote.yaml",
	})
	require.NoError(t, err)
	require.NotEmpty(t, planned)

	out := make(map[string]string, len(planned))
	for _, p := range planned {
		abs := p.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(resolved, abs)
		}
		rel, rerr := filepath.Rel(resolved, abs)
		require.NoError(t, rerr)
		out[rel] = p.Content
	}
	return out
}

// TestTelemetryReservedFieldsAreByteIdentical asserts that populating the reserved
// telemetry fields (webhook.url, webhook.secret_name, job_summary) produces a
// byte-identical generated plan set to a manifest that omits the telemetry block.
// These fields are reserved shape but not yet wired to generation/emit. The test
// drives the full Plan path (orchestrate, promote, composite action, and the rest)
// because telemetry is a config-level field that could affect any emitted file.
func TestTelemetryReservedFieldsAreByteIdentical(t *testing.T) {
	jobSummary := true

	// Case A: reserved telemetry fields populated.
	withTelemetry := writeTelemetryPlanManifest(t, &config.TelemetryConfig{
		Enabled: true,
		Adapter: config.TelemetryAdapterNone,
		Webhook: &config.TelemetryWebhook{
			URL:        "https://metrics.example.com/ingest",
			SecretName: "TELEMETRY_TOKEN",
		},
		JobSummary: &jobSummary,
	})
	plannedA := telemetryPlan(t, withTelemetry)

	// Case B: no telemetry block at all.
	withoutTelemetry := writeTelemetryPlanManifest(t, nil)
	plannedB := telemetryPlan(t, withoutTelemetry)

	// Guard against a vacuous comparison: the plan must be substantial for the
	// equality to be meaningful. Sum every emitted file's length and require the
	// total to clear a real-pipeline floor.
	var totalA int
	for _, content := range plannedA {
		totalA += len(content)
	}
	require.NotEmpty(t, plannedA, "plan must emit at least one file")
	require.Greater(t, totalA, 1024, "generated plan should be substantial")

	require.Equal(t, len(plannedB), len(plannedA),
		"reserved telemetry fields changed the set of emitted files")
	for path, want := range plannedB {
		got, ok := plannedA[path]
		require.Truef(t, ok, "telemetry plan omitted %s that the empty plan emitted", path)
		require.Equalf(t, want, got,
			"reserved telemetry fields must not affect generated output for %s", path)
	}
}

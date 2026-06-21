package config

import (
	"os"
	"path/filepath"
	"testing"
)

// appTokenManifest exercises the release_token_app / state_token_app surface
// through the on-disk load path, asserting it parses and validates cleanly while
// leaving the schema generation at the current version.
const appTokenManifest = `ci:
  config:
    schema_version: 1
    trunk_branch: main
    environments: [dev, prod]
    release_token_app:
      app_id: CASCADE_APP_ID
      private_key: CASCADE_APP_PRIVATE_KEY
    state_token_app:
      app_id: ${{ vars.CASCADE_APP_ID }}
      private_key: ${{ secrets.CASCADE_APP_PRIVATE_KEY }}
`

func TestAppTokenManifestE2E(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(appTokenManifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected a clean app-token manifest, got errors: %v", errs)
	}

	if !cfg.HasReleaseTokenApp() || !cfg.HasStateTokenApp() {
		t.Fatalf("app token sources did not round-trip: release=%v state=%v",
			cfg.HasReleaseTokenApp(), cfg.HasStateTokenApp())
	}
	if got := cfg.GetReleaseTokenAppID(); got != "${{ secrets.CASCADE_APP_ID }}" {
		t.Fatalf("release app_id ref = %q", got)
	}
	if got := cfg.GetStateTokenAppPrivateKey(); got != "${{ secrets.CASCADE_APP_PRIVATE_KEY }}" {
		t.Fatalf("state private_key ref = %q", got)
	}

	// The app token sources are additive: they must not change the schema
	// generation the manifest is read at.
	if cfg.GetSchemaVersion() != CurrentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", cfg.GetSchemaVersion(), CurrentSchemaVersion)
	}
	if CurrentSchemaVersion != 1 {
		t.Fatalf("CurrentSchemaVersion = %d, want 1", CurrentSchemaVersion)
	}
}

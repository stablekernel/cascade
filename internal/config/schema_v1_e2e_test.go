package config

import (
	"os"
	"path/filepath"
	"testing"
)

// fullSurfaceManifest exercises the full v1 schema surface (every reserved-shape
// field) through the on-disk load path the parse-config command uses, asserting
// it parses cleanly and validates with no errors.
const fullSurfaceManifest = `ci:
  config:
    schema_version: 1
    trunk_branch: main
    environments: [dev, prod]
    runs_on: ubuntu-latest
    job_timeout_minutes: 30
    pin_mode: sha
    action_pins:
      actions/checkout: v4.2.2
    dispatch_inputs:
      target_region:
        type: choice
        options: [us-east-1, eu-west-1]
        default: us-east-1
        description: deploy region
      force_rebuild:
        type: boolean
        default: false
    extra_triggers:
      schedule:
        - cron: "0 6 * * 1"
      repository_dispatch:
        types: [external-update, redeploy]
      workflow_run:
        workflows: [Upstream CI]
        types: [completed]
      merge_group: {}
    pr_preview:
      enabled: true
      comment: true
    validate_check:
      enabled: true
    merge_queue:
      enabled: true
    telemetry:
      enabled: false
      adapter: none
    environment_config:
      prod:
        gha_environment: production
    validate:
      workflow: .github/workflows/validate.yaml
      supports_dry_run: true
      permissions:
        contents: read
    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers: ["src/**", "!**/*.md"]
        secrets: inherit
        permissions:
          contents: read
          id-token: write
        matrix:
          dimensions:
            os: [ubuntu-latest, macos-latest]
            arch: [amd64, arm64]
          max_parallel: 2
          fail_fast: false
        artifacts:
          - name: linux-amd64
            path: dist/*.tar.gz
      - name: smoke
        run: go test ./...
        shell: bash
        runs_on: [self-hosted, linux]
        concurrency:
          group: smoke-${{ github.ref }}
          cancel_in_progress: true
        triggers: ["**/*_test.go"]
    deploys:
      - name: app
        workflow: .github/workflows/deploy.yaml
        depends_on: [build:app]
        optional_depends_on: [deploy:migrations]
        supports_dry_run: true
        auto_commits: true
        secrets:
          DEPLOY_TOKEN: DEPLOY_TOKEN
        permissions:
          contents: read
          id-token: write
        rollout:
          type: canary
          max_parallel: 1
          fail_fast: false
          canary:
            steps: [10, 50, 100]
            analysis: .github/workflows/canary-check.yaml
          blue_green:
            switch: .github/workflows/bg-switch.yaml
        deploy_target:
          mode: gitops
          repo: org/gitops-config
          path: clusters/prod/app.yaml
          field: image.tag
          value: ${{ inputs.artifact_id }}
        inputs:
          replicas: ${{ vars.PROD_REPLICAS }}
        env_inputs:
          prod:
            replicas: ${{ vars.PROD_REPLICAS }}
      - name: migrations
        workflow: .github/workflows/migrate.yaml
  state:
    prod:
      sha: abc123
      version: v1.2.0
      deploys:
        app:
          sha: abc123
          version: v1.2.0
      previous:
        - sha: old123
          version: v1.1.0
          committed_at: "2026-01-01T00:00:00Z"
`

func TestFullSurfaceManifestE2E(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(fullSurfaceManifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected a clean full-surface manifest, got errors: %v", errs)
	}

	// Spot-check that the reserved fields actually round-tripped from disk.
	if cfg.GetPinMode() != "sha" || cfg.ActionPins["actions/checkout"] != "v4.2.2" {
		t.Fatalf("pin fields not parsed: %s %v", cfg.PinMode, cfg.ActionPins)
	}
	if cfg.Builds[0].Matrix == nil || cfg.Builds[0].Permissions["id-token"] != "write" {
		t.Fatalf("build reserved fields not parsed: %#v", cfg.Builds[0])
	}
	if cfg.Builds[1].Run == "" || cfg.Builds[1].RunsOn == nil {
		t.Fatalf("inline run build not parsed: %#v", cfg.Builds[1])
	}
	if cfg.Deploys[0].Rollout.GetType() != "canary" || cfg.Deploys[0].DeployTarget.GetMode() != "gitops" {
		t.Fatalf("deploy reserved fields not parsed: %#v", cfg.Deploys[0])
	}
	if cfg.ExtraTriggers == nil || cfg.ExtraTriggers.MergeGroup == nil {
		t.Fatalf("extra_triggers not parsed: %#v", cfg.ExtraTriggers)
	}

	// Per-deployable version + previous ring survive the load path.
	st := cfg2State(t, path)
	if st["prod"].Deploys["app"].Version != "v1.2.0" {
		t.Fatalf("deploy state version not parsed: %#v", st["prod"].Deploys["app"])
	}
	if len(st["prod"].Previous) != 1 {
		t.Fatalf("previous ring not parsed: %#v", st["prod"].Previous)
	}
}

// cfg2State reloads the manifest as a full CICDFile to inspect state.
func cfg2State(t *testing.T, path string) map[string]*EnvState {
	t.Helper()
	file, err := ParseManifestFile(path, DefaultManifestKey)
	if err != nil {
		t.Fatalf("ParseManifestFile: %v", err)
	}
	return file.State
}

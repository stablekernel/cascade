package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// parseInline parses a YAML snippet into a TrunkConfig the way the manifest load
// path does (re-marshal round trip is not needed for the config: section alone).
func parseInline(t *testing.T, src string) *TrunkConfig {
	t.Helper()
	var cfg TrunkConfig
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &cfg
}

func hasErrContaining(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// --- Field parsing -----------------------------------------------------------

func TestParseSecretsUnion(t *testing.T) {
	t.Run("inherit scalar", func(t *testing.T) {
		cfg := parseInline(t, `
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
    secrets: inherit
`)
		s := cfg.Deploys[0].Secrets
		if s == nil || !s.Inherit || s.Map != nil {
			t.Fatalf("expected inherit form, got %#v", s)
		}
	})
	t.Run("explicit map", func(t *testing.T) {
		cfg := parseInline(t, `
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
    secrets:
      DEPLOY_TOKEN: DEPLOY_TOKEN
      NPM_TOKEN: PUBLISH_NPM_TOKEN
`)
		s := cfg.Deploys[0].Secrets
		if s == nil || s.Inherit || s.Map["NPM_TOKEN"] != "PUBLISH_NPM_TOKEN" {
			t.Fatalf("expected map form, got %#v", s)
		}
	})
	t.Run("inherit mapping", func(t *testing.T) {
		cfg := parseInline(t, `
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
    secrets:
      inherit: true
`)
		s := cfg.Deploys[0].Secrets
		if s == nil || !s.Inherit || s.Map != nil {
			t.Fatalf("expected inherit form from mapping, got %#v", s)
		}
	})
	t.Run("inherit false mapping treated as unset", func(t *testing.T) {
		cfg := parseInline(t, `
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
    secrets:
      inherit: false
`)
		s := cfg.Deploys[0].Secrets
		if s == nil || s.Inherit || s.Map != nil {
			t.Fatalf("expected inherit:false to parse to Inherit=false, no map, got %#v", s)
		}
	})
	t.Run("mixed inherit and secret keys rejected", func(t *testing.T) {
		var cfg TrunkConfig
		err := yaml.Unmarshal([]byte(`
deploys:
  - name: app
    workflow: w.yaml
    secrets:
      inherit: true
      NPM_TOKEN: PUBLISH_NPM_TOKEN
`), &cfg)
		if err == nil {
			t.Fatal("expected error mixing inherit with explicit secret mappings")
		}
	})
	t.Run("invalid scalar rejected", func(t *testing.T) {
		var cfg TrunkConfig
		err := yaml.Unmarshal([]byte(`
deploys:
  - name: app
    workflow: w.yaml
    secrets: bogus
`), &cfg)
		if err == nil {
			t.Fatal("expected error on invalid secrets scalar")
		}
	})
}

func TestParseRunsOnUnion(t *testing.T) {
	cfg := parseInline(t, `
builds:
  - name: a
    run: go build ./...
    runs_on: ubuntu-latest
  - name: b
    run: go build ./...
    runs_on: [self-hosted, macOS, arm64]
  - name: c
    run: go build ./...
    runs_on:
      group: gpu-runners
      labels: [cuda]
`)
	if cfg.Builds[0].RunsOn.Label != "ubuntu-latest" {
		t.Fatalf("scalar form: %#v", cfg.Builds[0].RunsOn)
	}
	if len(cfg.Builds[1].RunsOn.Labels) != 3 {
		t.Fatalf("list form: %#v", cfg.Builds[1].RunsOn)
	}
	if cfg.Builds[2].RunsOn.Group != "gpu-runners" || cfg.Builds[2].RunsOn.Labels[0] != "cuda" {
		t.Fatalf("object form: %#v", cfg.Builds[2].RunsOn)
	}
}

func TestParseMatrixAndRollout(t *testing.T) {
	cfg := parseInline(t, `
environments: [dev, prod]
builds:
  - name: app
    workflow: b.yaml
    matrix:
      dimensions:
        os: [ubuntu-latest, macos-latest]
        arch: [amd64, arm64]
      max_parallel: 2
      fail_fast: false
deploys:
  - name: app
    workflow: d.yaml
    rollout:
      type: canary
      max_parallel: 1
      fail_fast: false
      canary:
        steps: [10, 50, 100]
        analysis: .github/workflows/canary-check.yaml
      blue_green:
        switch: .github/workflows/bg-switch.yaml
`)
	m := cfg.Builds[0].Matrix
	if m == nil || len(m.Dimensions["os"]) != 2 || m.MaxParallel != 2 || m.FailFast == nil || *m.FailFast {
		t.Fatalf("matrix: %#v", m)
	}
	r := cfg.Deploys[0].Rollout
	if r == nil || r.Type != "canary" || r.Canary.Steps[2] != 100 || r.BlueGreen.Switch == "" {
		t.Fatalf("rollout: %#v", r)
	}
}

func TestParseConfigLevelFields(t *testing.T) {
	cfg := parseInline(t, `
environments:
  - dev
  - name: prod
    gha_environment: production
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
  force_rebuild:
    type: boolean
    default: false
extra_triggers:
  schedule:
    - cron: "0 6 * * 1"
  repository_dispatch:
    types: [external-update]
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
`)
	if cfg.GetPinMode() != "sha" || cfg.ActionPins["actions/checkout"] != "v4.2.2" {
		t.Fatalf("pin fields: %s %v", cfg.PinMode, cfg.ActionPins)
	}
	if cfg.RunsOn.Label != "ubuntu-latest" || cfg.JobTimeoutMinutes != 30 {
		t.Fatalf("runs_on/job_timeout: %#v %d", cfg.RunsOn, cfg.JobTimeoutMinutes)
	}
	if cfg.DispatchInputs["target_region"].Type != "choice" || len(cfg.DispatchInputs["target_region"].Options) != 2 {
		t.Fatalf("dispatch_inputs: %#v", cfg.DispatchInputs)
	}
	et := cfg.ExtraTriggers
	if et == nil || et.Schedule[0].Cron != "0 6 * * 1" || et.RepositoryDispatch == nil || et.WorkflowRun == nil || et.MergeGroup == nil {
		t.Fatalf("extra_triggers: %#v", et)
	}
	if !cfg.PRPreview.IsEnabled() || !cfg.ValidateCheck.IsEnabled() || !cfg.MergeQueue.Enabled {
		t.Fatalf("pr lanes: %#v %#v %#v", cfg.PRPreview, cfg.ValidateCheck, cfg.MergeQueue)
	}
	var prodGHAEnvironment string
	for _, e := range cfg.Environments {
		if e.Name == "prod" {
			prodGHAEnvironment = e.GHAEnvironment
		}
	}
	if cfg.Telemetry.Adapter != "none" || prodGHAEnvironment != "production" {
		t.Fatalf("telemetry/env_config: %#v %#v", cfg.Telemetry, cfg.Environments)
	}
}

func TestParsePerCallbackExtras(t *testing.T) {
	cfg := parseInline(t, `
deploys:
  - name: app
    workflow: d.yaml
    permissions:
      contents: read
      id-token: write
    optional_depends_on: [build:app]
    auto_commits: true
    deploy_target:
      mode: gitops
      repo: org/gitops-config
      path: clusters/prod/app.yaml
      field: image.tag
      value: ${{ inputs.artifact_id }}
builds:
  - name: app
    workflow: b.yaml
`)
	d := cfg.Deploys[0]
	if d.Permissions["id-token"] != "write" || !d.AutoCommits || d.DeployTarget.GetMode() != "gitops" {
		t.Fatalf("per-callback extras: %#v", d)
	}
	if len(d.OptionalDependsOn) != 1 {
		t.Fatalf("optional_depends_on: %#v", d.OptionalDependsOn)
	}
}

func TestParseInlineRunCallback(t *testing.T) {
	cfg := parseInline(t, `
builds:
  - name: smoke
    run: go test ./...
    shell: bash
    runs_on: ubuntu-latest
`)
	b := cfg.Builds[0]
	if b.Run != "go test ./..." || b.Shell != "bash" || b.Workflow != "" {
		t.Fatalf("inline run callback: %#v", b)
	}
}

func TestParseDeployStateVersionAndPreviousRing(t *testing.T) {
	var file CICDFile
	if err := yaml.Unmarshal([]byte(`
config:
  trunk_branch: main
state:
  prod:
    sha: abc
    version: v1.2.0
    deploys:
      app:
        sha: abc
        version: v1.2.0
    previous:
      - sha: old
        version: v1.1.0
        committed_at: "2026-01-01T00:00:00Z"
`), &file); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ds := file.State["prod"].Deploys["app"]
	if ds.Version != "v1.2.0" {
		t.Fatalf("deploy state version: %#v", ds)
	}
	if len(file.State["prod"].Previous) != 1 || file.State["prod"].Previous[0].Version != "v1.1.0" {
		t.Fatalf("previous ring: %#v", file.State["prod"].Previous)
	}
}

// --- Structural validation rejections ----------------------------------------

func TestValidateWorkflowRunXOR(t *testing.T) {
	tests := []struct {
		name      string
		manifest  string
		wantErrs  []string
		denyErrs  []string
		wantClean bool
	}{
		{
			name: "run set, no workflow",
			manifest: `
builds:
  - name: app
    run: go build ./...
`,
			wantErrs: []string{
				"inline run: callbacks are no longer supported",
				"workflow is required",
			},
		},
		{
			name: "workflow and run both set",
			manifest: `
builds:
  - name: app
    workflow: b.yaml
    run: go build ./...
`,
			wantErrs: []string{"inline run: callbacks are no longer supported"},
		},
		{
			name: "shell set, no workflow, no run",
			manifest: `
builds:
  - name: app
    shell: bash
`,
			wantErrs: []string{
				"shell: is no longer supported",
				"workflow is required",
			},
		},
		{
			name: "neither run nor workflow",
			manifest: `
builds:
  - name: app
`,
			wantErrs: []string{"workflow is required"},
		},
		{
			name: "workflow only is clean",
			manifest: `
builds:
  - name: app
    workflow: b.yaml
`,
			denyErrs:  []string{"inline run: callbacks are no longer supported", "shell: is no longer supported", "workflow is required"},
			wantClean: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(parseInline(t, tt.manifest))
			for _, want := range tt.wantErrs {
				if !hasErrContaining(errs, want) {
					t.Fatalf("expected error containing %q, got %v", want, errs)
				}
			}
			for _, deny := range tt.denyErrs {
				if hasErrContaining(errs, deny) {
					t.Fatalf("did not expect error containing %q, got %v", deny, errs)
				}
			}
		})
	}
}

func TestValidateRunsOnRejectedOnReusableWorkflow(t *testing.T) {
	cfg := parseInline(t, `
builds:
  - name: app
    workflow: b.yaml
    runs_on: ubuntu-latest
`)
	if errs := Validate(cfg); !hasErrContaining(errs, "runs_on is not valid on a reusable-workflow callback") {
		t.Fatalf("expected runs_on rejection, got %v", errs)
	}
}

// TestValidateRunRejected asserts a run-only callback is rejected with the
// inline-removed message, and a shell-only callback is rejected with the
// shell-removed message, while a workflow-only callback validates clean.
func TestValidateRunRejected(t *testing.T) {
	t.Run("run-only rejected", func(t *testing.T) {
		cfg := parseInline(t, `
builds:
  - name: app
    run: go build ./...
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "inline run: callbacks are no longer supported") {
			t.Fatalf("expected inline run rejection, got %v", errs)
		}
	})
	t.Run("shell-only rejected", func(t *testing.T) {
		cfg := parseInline(t, `
builds:
  - name: app
    shell: bash
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "shell: is no longer supported") {
			t.Fatalf("expected shell rejection, got %v", errs)
		}
	})
	t.Run("workflow-only clean", func(t *testing.T) {
		cfg := parseInline(t, `
builds:
  - name: app
    workflow: b.yaml
`)
		for _, e := range Validate(cfg) {
			if strings.Contains(e, "no longer supported") || strings.Contains(e, "workflow is required") {
				t.Fatalf("workflow-only callback must validate clean, got %v", e)
			}
		}
	})
}

// TestValidate_TimeoutMinutesOnCallback_Rejected asserts that a per-callback
// timeout_minutes on a reusable-workflow callback (builds, deploys, validate) is
// rejected at validation. GitHub forbids timeout-minutes on a job that calls a
// reusable workflow, and every cascade callback is a reusable-workflow uses: job,
// so the timeout must live inside the called workflow instead.
func TestValidate_TimeoutMinutesOnCallback_Rejected(t *testing.T) {
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
			if !hasErrContaining(errs, "timeout belongs in the called workflow, not the caller") {
				t.Fatalf("expected timeout_minutes rejection, got %v", errs)
			}
			if !hasErrContaining(errs, "GitHub forbids timeout-minutes on a job that calls a reusable workflow") {
				t.Fatalf("expected actionable timeout-minutes guidance, got %v", errs)
			}
		})
	}
}

// TestValidate_CallbackWithoutTimeout_Clean asserts the control cases that must
// keep validating clean: a callback without timeout_minutes, and a manifest-level
// config.job_timeout_minutes (the cascade-owned job timeout, a different field).
func TestValidate_CallbackWithoutTimeout_Clean(t *testing.T) {
	t.Run("callback without timeout_minutes clean", func(t *testing.T) {
		cfg := parseInline(t, `
builds:
  - name: app
    workflow: b.yaml
deploys:
  - name: svc
    workflow: d.yaml
`)
		for _, e := range Validate(cfg) {
			if strings.Contains(e, "timeout_minutes is not valid") {
				t.Fatalf("callback without timeout_minutes must validate clean, got %v", e)
			}
		}
	})
	t.Run("config.job_timeout_minutes not rejected", func(t *testing.T) {
		cfg := parseInline(t, `
job_timeout_minutes: 20
builds:
  - name: app
    workflow: b.yaml
`)
		if cfg.JobTimeoutMinutes != 20 {
			t.Fatalf("job_timeout_minutes should parse to 20, got %d", cfg.JobTimeoutMinutes)
		}
		for _, e := range Validate(cfg) {
			if strings.Contains(e, "timeout_minutes is not valid") {
				t.Fatalf("manifest-level job_timeout_minutes must not be rejected, got %v", e)
			}
		}
	})
}

func TestValidateConcurrencyRejectedOnReusableWorkflow(t *testing.T) {
	cfg := parseInline(t, `
deploys:
  - name: app
    workflow: d.yaml
    concurrency:
      group: deploy-app
      cancel_in_progress: false
`)
	if errs := Validate(cfg); !hasErrContaining(errs, "concurrency is not valid on a reusable-workflow callback") {
		t.Fatalf("expected concurrency rejection, got %v", errs)
	}
}

func TestValidatePermissions(t *testing.T) {
	t.Run("unknown scope rejected", func(t *testing.T) {
		cfg := parseInline(t, `
deploys:
  - name: app
    workflow: d.yaml
    permissions:
      bogus: read
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "unknown permission scope") {
			t.Fatalf("expected scope rejection, got %v", errs)
		}
	})
	t.Run("bad value rejected", func(t *testing.T) {
		cfg := parseInline(t, `
deploys:
  - name: app
    workflow: d.yaml
    permissions:
      contents: bogus
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "must be one of: read, write, none") {
			t.Fatalf("expected value rejection, got %v", errs)
		}
	})
}

func TestValidateRolloutEnvGating(t *testing.T) {
	t.Run("canary requires environments", func(t *testing.T) {
		cfg := parseInline(t, `
deploys:
  - name: app
    workflow: d.yaml
    rollout:
      type: canary
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "requires environments") {
			t.Fatalf("expected env gating rejection, got %v", errs)
		}
	})
	t.Run("bad type rejected", func(t *testing.T) {
		cfg := parseInline(t, `
environments: [dev]
deploys:
  - name: app
    workflow: d.yaml
    rollout:
      type: bogus
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "rollout.type must be one of") {
			t.Fatalf("expected rollout type rejection, got %v", errs)
		}
	})
}

func TestValidateDeployTargetMode(t *testing.T) {
	cfg := parseInline(t, `
deploys:
  - name: app
    workflow: d.yaml
    deploy_target:
      mode: bogus
`)
	if errs := Validate(cfg); !hasErrContaining(errs, "deploy_target.mode must be one of") {
		t.Fatalf("expected deploy_target rejection, got %v", errs)
	}
}

func TestValidateConfigLevelRules(t *testing.T) {
	t.Run("pin_mode rejected", func(t *testing.T) {
		cfg := parseInline(t, "pin_mode: bogus\n")
		if errs := Validate(cfg); !hasErrContaining(errs, "pin_mode must be one of") {
			t.Fatalf("expected pin_mode rejection, got %v", errs)
		}
	})
	t.Run("cli_version_sha non-hex rejected", func(t *testing.T) {
		cfg := parseInline(t, "cli_version_sha: not-a-sha\n")
		if errs := Validate(cfg); !hasErrContaining(errs, "cli_version_sha must be a 40-character lowercase hex commit SHA") {
			t.Fatalf("expected cli_version_sha rejection, got %v", errs)
		}
	})
	t.Run("cli_version_sha short hex rejected", func(t *testing.T) {
		cfg := parseInline(t, "cli_version_sha: 9dc69a1f\n")
		if errs := Validate(cfg); !hasErrContaining(errs, "cli_version_sha must be a 40-character lowercase hex commit SHA") {
			t.Fatalf("expected short cli_version_sha rejection, got %v", errs)
		}
	})
	t.Run("cli_version_sha valid 40-hex accepted", func(t *testing.T) {
		cfg := parseInline(t, "cli_version_sha: 9dc69a1f66753a3865c38c34eca5a931f677c803\n")
		if errs := Validate(cfg); hasErrContaining(errs, "cli_version_sha") {
			t.Fatalf("expected valid cli_version_sha to pass, got %v", errs)
		}
	})
	t.Run("reserved dispatch input shadow rejected", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  dry_run:
    type: boolean
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "shadows a reserved dispatch input name") {
			t.Fatalf("expected shadow rejection, got %v", errs)
		}
	})
	t.Run("choice input needs options", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  region:
    type: choice
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "choice input but has no options") {
			t.Fatalf("expected choice options rejection, got %v", errs)
		}
	})
	t.Run("dispatch input name with space rejected", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  "bad name":
    type: string
`)
		if errs := Validate(cfg); !hasErrContaining(errs, `dispatch_inputs "bad name" must contain only`) {
			t.Fatalf("expected dispatch input name rejection, got %v", errs)
		}
	})
	t.Run("dispatch input name with dot rejected", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  "region.primary":
    type: string
`)
		if errs := Validate(cfg); !hasErrContaining(errs, `dispatch_inputs "region.primary" must contain only`) {
			t.Fatalf("expected dispatch input name rejection, got %v", errs)
		}
	})
	t.Run("dispatch input name with expression fragment rejected", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  "x${{ secrets.TOKEN }}":
    type: string
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "must contain only") {
			t.Fatalf("expected dispatch input name rejection, got %v", errs)
		}
	})
	t.Run("safe dispatch input name accepted", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  target_region:
    type: string
`)
		if errs := Validate(cfg); hasErrContaining(errs, "must contain only") {
			t.Fatalf("expected safe dispatch input name to pass, got %v", errs)
		}
	})
	t.Run("choice option with space rejected", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  region:
    type: choice
    options:
      - "us east 1"
`)
		if errs := Validate(cfg); !hasErrContaining(errs, `dispatch_inputs.region option "us east 1" must contain only`) {
			t.Fatalf("expected choice option rejection, got %v", errs)
		}
	})
	t.Run("choice option with expression fragment rejected", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  region:
    type: choice
    options:
      - "${{ github.token }}"
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "dispatch_inputs.region option") {
			t.Fatalf("expected choice option rejection, got %v", errs)
		}
	})
	t.Run("empty choice option rejected", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  region:
    type: choice
    options:
      - ""
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "dispatch_inputs.region option") {
			t.Fatalf("expected empty choice option rejection, got %v", errs)
		}
	})
	t.Run("dotted and hyphenated choice options accepted", func(t *testing.T) {
		cfg := parseInline(t, `
dispatch_inputs:
  region:
    type: choice
    options:
      - us-east-1
      - v1.2.3
`)
		if errs := Validate(cfg); hasErrContaining(errs, "option") {
			t.Fatalf("expected dotted/hyphenated options to pass, got %v", errs)
		}
	})
	t.Run("removed environment_config block rejected with a did-you-mean", func(t *testing.T) {
		// environment_config was folded into the environments: list; the
		// standalone block is now an unknown top-level field with a
		// did-you-mean pointing at environments.
		cfg := parseInline(t, `
environments: [dev]
environment_config:
  prod:
    gha_environment: production
`)
		if errs := Validate(cfg); !hasErrContaining(errs, `unknown field "environment_config"; did you mean "environments"`) {
			t.Fatalf("expected environment_config did-you-mean rejection, got %v", errs)
		}
	})
}

func TestValidateOptionalDependsOnResolution(t *testing.T) {
	t.Run("resolves like depends_on", func(t *testing.T) {
		cfg := parseInline(t, `
builds:
  - name: app
    workflow: b.yaml
deploys:
  - name: svc
    workflow: d.yaml
    optional_depends_on: [build:app]
`)
		for _, e := range Validate(cfg) {
			if strings.Contains(e, "optional_depends_on") {
				t.Fatalf("valid optional dep flagged: %v", e)
			}
		}
	})
	t.Run("unresolved rejected", func(t *testing.T) {
		cfg := parseInline(t, `
deploys:
  - name: svc
    workflow: d.yaml
    optional_depends_on: [build:missing]
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "optional_depends_on") {
			t.Fatalf("expected unresolved optional dep rejection, got %v", errs)
		}
	})
}

// --- Review follow-up coverage ----------------------------------------------

func TestValidateValidateCallbackRules(t *testing.T) {
	t.Run("workflow and run mutually exclusive", func(t *testing.T) {
		cfg := parseInline(t, `
validate:
  workflow: v.yaml
  run: go vet ./...
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "inline run: callbacks are no longer supported") {
			t.Fatalf("expected inline run rejection, got %v", errs)
		}
	})
	t.Run("runs_on rejected on reusable workflow", func(t *testing.T) {
		cfg := parseInline(t, `
validate:
  workflow: v.yaml
  runs_on: ubuntu-latest
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "runs_on is not valid on a reusable-workflow callback") {
			t.Fatalf("expected runs_on rejection, got %v", errs)
		}
	})
	t.Run("concurrency rejected on reusable workflow", func(t *testing.T) {
		cfg := parseInline(t, `
validate:
  workflow: v.yaml
  concurrency:
    group: validate
    cancel_in_progress: false
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "concurrency is not valid on a reusable-workflow callback") {
			t.Fatalf("expected concurrency rejection, got %v", errs)
		}
	})
	t.Run("run-only validate rejected", func(t *testing.T) {
		cfg := parseInline(t, `
validate:
  run: go vet ./...
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "inline run: callbacks are no longer supported") {
			t.Fatalf("expected inline run rejection on validate, got %v", errs)
		}
	})
}

func TestSecretsNullTreatedAsUnset(t *testing.T) {
	cfg := parseInline(t, `
deploys:
  - name: app
    workflow: d.yaml
    secrets:
`)
	if cfg.Deploys[0].Secrets != nil {
		t.Fatalf("bare secrets: should be nil/unset, got %#v", cfg.Deploys[0].Secrets)
	}
	if errs := Validate(cfg); hasErrContaining(errs, "secrets") {
		t.Fatalf("unset secrets should not error, got %v", errs)
	}
}

func TestRunsOnEmptyMappingRejected(t *testing.T) {
	var cfg TrunkConfig
	err := yaml.Unmarshal([]byte(`
builds:
  - name: app
    run: go build ./...
    runs_on:
      foo: bar
`), &cfg)
	if err == nil {
		t.Fatal("expected error on runs_on mapping with neither group nor labels")
	}
}

func TestOptionalDependsOnParticipatesInCycleDetection(t *testing.T) {
	// build:a --depends_on--> build:b --optional_depends_on--> build:a (cycle)
	cfg := parseInline(t, `
builds:
  - name: a
    workflow: a.yaml
    depends_on: [build:b]
  - name: b
    workflow: b.yaml
    optional_depends_on: [build:a]
`)
	if errs := Validate(cfg); !hasErrContaining(errs, "circular dependency") {
		t.Fatalf("expected a cycle through optional_depends_on, got %v", errs)
	}
}

func TestGetPinModeNilReceiver(t *testing.T) {
	var c *TrunkConfig
	if c.GetPinMode() != PinModeTag {
		t.Fatalf("nil-receiver GetPinMode should return default tag")
	}
}

func TestParseExternalDeployReservedFields(t *testing.T) {
	var file CICDFile
	if err := yaml.Unmarshal([]byte(`
config:
  trunk_branch: main
  external:
    - repo: org/cdk-infra
      deploys:
        - name: cdk
          workflow: org/cdk-infra/.github/workflows/deploy.yaml@main
          permissions:
            id-token: write
          rollout:
            type: rolling
          deploy_target:
            mode: gitops
            repo: org/gitops
`), &file); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := file.Config.External[0].Deploys[0]
	if d.Permissions["id-token"] != "write" || d.Rollout.GetType() != "rolling" || d.DeployTarget.GetMode() != "gitops" {
		t.Fatalf("external deploy reserved fields not parsed: %#v", d)
	}
}

func TestValidateExternalDeployRules(t *testing.T) {
	t.Run("runs_on rejected on reusable external workflow", func(t *testing.T) {
		cfg := parseInline(t, `
external:
  - repo: org/infra
    deploys:
      - name: cdk
        workflow: org/infra/.github/workflows/d.yaml@main
        runs_on: ubuntu-latest
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "runs_on is not valid on a reusable-workflow callback") {
			t.Fatalf("expected external runs_on rejection, got %v", errs)
		}
	})
	t.Run("missing workflow rejected", func(t *testing.T) {
		cfg := parseInline(t, `
external:
  - repo: org/infra
    deploys:
      - name: cdk
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "workflow is required") {
			t.Fatalf("expected external missing-workflow rejection, got %v", errs)
		}
	})
	t.Run("run rejected on external deploy", func(t *testing.T) {
		cfg := parseInline(t, `
external:
  - repo: org/infra
    deploys:
      - name: cdk
        run: echo deploy
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "external deploys are reusable-workflow only; run is not supported") {
			t.Fatalf("expected external run rejection, got %v", errs)
		}
	})
	t.Run("shell rejected on external deploy", func(t *testing.T) {
		cfg := parseInline(t, `
external:
  - repo: org/infra
    deploys:
      - name: cdk
        workflow: org/infra/.github/workflows/d.yaml@main
        shell: bash
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "external deploys are reusable-workflow only; shell is not supported") {
			t.Fatalf("expected external shell rejection, got %v", errs)
		}
	})
}

// --- Reserved components shape (#176) ---------------------------------------

func TestComponentsShapeParseAndValidate(t *testing.T) {
	t.Run("valid components block passes", func(t *testing.T) {
		cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
components:
  api:
    path: services/api
    tag_grammar:
      prefix: api-v
  worker:
    path: services/worker
    tag_grammar:
      prefix: worker-v
`)
		if cfg.Components == nil {
			t.Fatal("Components map should be parsed")
			return
		}
		if cfg.Components["api"].Path != "services/api" {
			t.Fatalf("unexpected path: %q", cfg.Components["api"].Path)
		}
		if p, _ := cfg.GetComponentTagPrefix("api"); p != "api-v" {
			t.Fatalf("unexpected tag_grammar.prefix: %q", p)
		}
		errs := Validate(cfg)
		if len(errs) != 0 {
			t.Fatalf("expected no errors, got: %v", errs)
		}
	})

	t.Run("non-job-id-safe component name rejected", func(t *testing.T) {
		cfg := parseInline(t, `
trunk_branch: main
components:
  "bad name!":
    path: services/bad
`)
		errs := Validate(cfg)
		if !hasErrContaining(errs, "bad name!") {
			t.Fatalf("expected error for invalid component name, got: %v", errs)
		}
	})

	t.Run("path with dotdot rejected", func(t *testing.T) {
		cfg := parseInline(t, `
trunk_branch: main
components:
  mycomp:
    path: ../outside
`)
		errs := Validate(cfg)
		if !hasErrContaining(errs, "path") {
			t.Fatalf("expected error for path with .., got: %v", errs)
		}
	})

	t.Run("absolute path rejected", func(t *testing.T) {
		cfg := parseInline(t, `
trunk_branch: main
components:
  mycomp:
    path: /absolute/path
`)
		errs := Validate(cfg)
		if !hasErrContaining(errs, "path") {
			t.Fatalf("expected error for absolute path, got: %v", errs)
		}
	})

	t.Run("omitting components is nil and zero errors", func(t *testing.T) {
		cfg := parseInline(t, `
trunk_branch: main
environments: [dev, prod]
builds:
  - name: app
    workflow: .github/workflows/build.yaml
    triggers: ["src/**"]
`)
		if cfg.Components != nil {
			t.Fatalf("expected nil Components, got: %v", cfg.Components)
		}
		errs := Validate(cfg)
		if len(errs) != 0 {
			t.Fatalf("expected no errors, got: %v", errs)
		}
	})
}

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
environment_config:
  prod:
    gha_environment: production
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
	if !cfg.PRPreview.Enabled || !cfg.ValidateCheck.Enabled || !cfg.MergeQueue.Enabled {
		t.Fatalf("pr lanes: %#v %#v %#v", cfg.PRPreview, cfg.ValidateCheck, cfg.MergeQueue)
	}
	if cfg.Telemetry.Adapter != "none" || cfg.EnvironmentConfig["prod"].GHAEnvironment != "production" {
		t.Fatalf("telemetry/env_config: %#v %#v", cfg.Telemetry, cfg.EnvironmentConfig)
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
	t.Run("both set rejected", func(t *testing.T) {
		cfg := parseInline(t, `
builds:
  - name: app
    workflow: b.yaml
    run: go build ./...
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "mutually exclusive") {
			t.Fatalf("expected XOR rejection, got %v", errs)
		}
	})
	t.Run("neither set rejected", func(t *testing.T) {
		cfg := parseInline(t, `
builds:
  - name: app
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "one of workflow or run is required") {
			t.Fatalf("expected missing-callback rejection, got %v", errs)
		}
	})
	t.Run("shell without run rejected", func(t *testing.T) {
		cfg := parseInline(t, `
builds:
  - name: app
    workflow: b.yaml
    shell: bash
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "shell is only valid alongside run") {
			t.Fatalf("expected shell rejection, got %v", errs)
		}
	})
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

func TestValidateRunsOnAllowedOnInlineRun(t *testing.T) {
	cfg := parseInline(t, `
builds:
  - name: app
    run: go build ./...
    runs_on: ubuntu-latest
`)
	for _, e := range Validate(cfg) {
		if strings.Contains(e, "runs_on") {
			t.Fatalf("runs_on should be allowed on inline run, got %v", e)
		}
	}
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
	t.Run("environment_config unknown env rejected", func(t *testing.T) {
		cfg := parseInline(t, `
environments: [dev]
environment_config:
  prod:
    gha_environment: production
`)
		if errs := Validate(cfg); !hasErrContaining(errs, "not in environments") {
			t.Fatalf("expected env_config rejection, got %v", errs)
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
		if errs := Validate(cfg); !hasErrContaining(errs, "mutually exclusive") {
			t.Fatalf("expected XOR rejection, got %v", errs)
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
	t.Run("runs_on allowed on inline run validate", func(t *testing.T) {
		cfg := parseInline(t, `
validate:
  run: go vet ./...
  runs_on: ubuntu-latest
`)
		for _, e := range Validate(cfg) {
			if strings.Contains(e, "runs_on") {
				t.Fatalf("runs_on should be allowed on inline run validate, got %v", e)
			}
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

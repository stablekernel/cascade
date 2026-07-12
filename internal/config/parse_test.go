package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	// Create a temporary config file with ci: key at top level
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	configContent := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - test
      - prod
    validate:
      workflow: .github/workflows/validate.yaml
      supports_dry_run: true
    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers:
          - "src/**"
          - "go.mod"
      - name: worker
        workflow: .github/workflows/build-worker.yaml
        triggers:
          - "worker/**"
    deploys:
      - name: infra
        workflow: .github/workflows/deploy-infra.yaml
        triggers:
          - "cdk/**"
      - name: services
        workflow: .github/workflows/deploy-services.yaml
        triggers:
          - "src/**"
        depends_on:
          - infra
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := Parse(configPath)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Verify config settings
	if cfg.TrunkBranch != "main" {
		t.Errorf("TrunkBranch = %q, want %q", cfg.TrunkBranch, "main")
	}

	// Verify validate config
	if cfg.Validate == nil {
		t.Fatal("Validate is nil")
		return
	}
	if cfg.Validate.Workflow != ".github/workflows/validate.yaml" {
		t.Errorf("Validate.Workflow = %q, want %q", cfg.Validate.Workflow, ".github/workflows/validate.yaml")
	}
	if !cfg.Validate.DryRunSupported() {
		t.Error("Validate.SupportsDryRun = false, want true")
	}

	// Verify builds
	if len(cfg.Builds) != 2 {
		t.Fatalf("len(Builds) = %d, want 2", len(cfg.Builds))
	}
	if cfg.Builds[0].Name != "app" {
		t.Errorf("Builds[0].Name = %q, want %q", cfg.Builds[0].Name, "app")
	}
	if len(cfg.Builds[0].Triggers) != 2 {
		t.Errorf("len(Builds[0].Triggers) = %d, want 2", len(cfg.Builds[0].Triggers))
	}

	// Verify top-level environments
	if len(cfg.Environments) != 3 {
		t.Errorf("len(Environments) = %d, want 3", len(cfg.Environments))
	}

	// Verify deploys
	if len(cfg.Deploys) != 2 {
		t.Fatalf("len(Deploys) = %d, want 2", len(cfg.Deploys))
	}
	if cfg.Deploys[0].Name != "infra" {
		t.Errorf("Deploys[0].Name = %q, want %q", cfg.Deploys[0].Name, "infra")
	}
	if len(cfg.Deploys[1].DependsOn) != 1 || cfg.Deploys[1].DependsOn[0] != "infra" {
		t.Errorf("Deploys[1].DependsOn = %v, want [infra]", cfg.Deploys[1].DependsOn)
	}
}

func TestParse_WithInputs(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	configContent := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    builds:
      - name: app
        workflow: .github/workflows/build-app.yaml
        triggers:
          - "src/**"
        inputs:
          dockerfile_path: "./src/Dockerfile"
          push_to_ecr: true
    deploys:
      - name: app
        workflow: .github/workflows/deploy-app.yaml
        triggers:
          - "src/**"
        inputs:
          cluster_name: "my-cluster"
          namespace: "default"
        env_inputs:
          dev:
            replicas: 1
            debug_logging: true
          prod:
            replicas: 3
            debug_logging: false
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := Parse(configPath)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Verify build inputs
	if cfg.Builds[0].Inputs == nil {
		t.Fatal("Builds[0].Inputs is nil")
		return
	}
	if cfg.Builds[0].Inputs["dockerfile_path"] != "./src/Dockerfile" {
		t.Errorf("Builds[0].Inputs[dockerfile_path] = %v, want ./src/Dockerfile", cfg.Builds[0].Inputs["dockerfile_path"])
	}
	if cfg.Builds[0].Inputs["push_to_ecr"] != true {
		t.Errorf("Builds[0].Inputs[push_to_ecr] = %v, want true", cfg.Builds[0].Inputs["push_to_ecr"])
	}

	// Verify deploy inputs
	if cfg.Deploys[0].Inputs == nil {
		t.Fatal("Deploys[0].Inputs is nil")
		return
	}
	if cfg.Deploys[0].Inputs["cluster_name"] != "my-cluster" {
		t.Errorf("Deploys[0].Inputs[cluster_name] = %v, want my-cluster", cfg.Deploys[0].Inputs["cluster_name"])
	}

	// Verify env_inputs
	if cfg.Deploys[0].EnvInputs == nil {
		t.Fatal("Deploys[0].EnvInputs is nil")
		return
	}
	if cfg.Deploys[0].EnvInputs["dev"]["replicas"] != 1 {
		t.Errorf("Deploys[0].EnvInputs[dev][replicas] = %v, want 1", cfg.Deploys[0].EnvInputs["dev"]["replicas"])
	}
	if cfg.Deploys[0].EnvInputs["prod"]["replicas"] != 3 {
		t.Errorf("Deploys[0].EnvInputs[prod][replicas] = %v, want 3", cfg.Deploys[0].EnvInputs["prod"]["replicas"])
	}

	// Validate should pass
	errors := Validate(cfg)
	if len(errors) != 0 {
		t.Errorf("Validate() returned errors: %v", errors)
	}
}

func TestParse_FileNotFound(t *testing.T) {
	_, err := Parse("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("Parse() expected error for nonexistent file")
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.yaml")

	if err := os.WriteFile(configPath, []byte("invalid: yaml: content: ["), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	_, err := Parse(configPath)
	if err == nil {
		t.Error("Parse() expected error for invalid YAML")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name       string
		config     TrunkConfig
		wantErrors int
	}{
		{
			name: "valid config",
			config: TrunkConfig{
				TrunkBranch:  "main",
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "app", Workflow: ".github/workflows/build.yaml"},
				},
				Deploys: []DeployConfig{
					{Name: "cdk", Workflow: ".github/workflows/deploy.yaml"},
				},
			},
			wantErrors: 0,
		},
		{
			name: "empty environments (valid - no-environment setup)",
			config: TrunkConfig{
				TrunkBranch: "main",
			},
			wantErrors: 0,
		},
		{
			name: "missing build name",
			config: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Workflow: ".github/workflows/build.yaml"},
				},
			},
			wantErrors: 1,
		},
		{
			name: "missing build workflow",
			config: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "app"},
				},
			},
			wantErrors: 1,
		},
		{
			name: "duplicate build names",
			config: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "app", Workflow: ".github/workflows/build1.yaml"},
					{Name: "app", Workflow: ".github/workflows/build2.yaml"},
				},
			},
			wantErrors: 1,
		},
		{
			name: "missing deploy name",
			config: TrunkConfig{
				Environments: EnvNames("dev"),
				Deploys: []DeployConfig{
					{Workflow: ".github/workflows/deploy.yaml"},
				},
			},
			wantErrors: 1,
		},
		{
			name: "missing deploy workflow",
			config: TrunkConfig{
				Environments: EnvNames("dev"),
				Deploys: []DeployConfig{
					{Name: "cdk"},
				},
			},
			wantErrors: 1,
		},
		{
			name: "duplicate deploy names",
			config: TrunkConfig{
				Environments: EnvNames("dev"),
				Deploys: []DeployConfig{
					{Name: "cdk", Workflow: ".github/workflows/deploy1.yaml"},
					{Name: "cdk", Workflow: ".github/workflows/deploy2.yaml"},
				},
			},
			wantErrors: 1,
		},
		{
			name: "valid env_inputs matching environments",
			config: TrunkConfig{
				Environments: EnvNames("dev", "test", "prod"),
				Deploys: []DeployConfig{
					{
						Name:     "app",
						Workflow: ".github/workflows/deploy.yaml",
						EnvInputs: map[string]map[string]interface{}{
							"dev":  {"replicas": 1},
							"prod": {"replicas": 3},
						},
					},
				},
			},
			wantErrors: 0,
		},
		{
			name: "invalid env_inputs key not in environments",
			config: TrunkConfig{
				Environments: EnvNames("dev", "prod"),
				Deploys: []DeployConfig{
					{
						Name:     "app",
						Workflow: ".github/workflows/deploy.yaml",
						EnvInputs: map[string]map[string]interface{}{
							"staging": {"replicas": 2},
						},
					},
				},
			},
			wantErrors: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errors := Validate(&tt.config)
			if len(errors) != tt.wantErrors {
				t.Errorf("Validate() returned %d errors, want %d: %v", len(errors), tt.wantErrors, errors)
			}
		})
	}
}

func TestGetBuildNames(t *testing.T) {
	cfg := &TrunkConfig{
		Builds: []BuildConfig{
			{Name: "app"},
			{Name: "worker"},
			{Name: "tools"},
		},
	}

	names := GetBuildNames(cfg)
	if len(names) != 3 {
		t.Fatalf("GetBuildNames() returned %d names, want 3", len(names))
	}

	expected := []string{"app", "worker", "tools"}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("names[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestGetDeployNames(t *testing.T) {
	cfg := &TrunkConfig{
		Deploys: []DeployConfig{
			{Name: "infra"},
			{Name: "services"},
		},
	}

	names := GetDeployNames(cfg)
	if len(names) != 2 {
		t.Fatalf("GetDeployNames() returned %d names, want 2", len(names))
	}

	expected := []string{"infra", "services"}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("names[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestParse_NewSchemaFields(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	configContent := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
        triggers:
          - "src/**"
        depends_on:
          - base
        run_policy: always
        on_failure: continue
        retries: 2
        inputs:
          key: value
        env_inputs:
          dev:
            key: dev-value
      - name: base
        workflow: .github/workflows/base.yaml
    deploys:
      - name: service
        workflow: .github/workflows/deploy.yaml
        triggers:
          - "deploy/**"
        depends_on:
          - app
        run_policy: default
        on_failure: abort
        retries: 0
        inputs:
          cluster: my-cluster
        env_inputs:
          dev:
            replicas: 1
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := Parse(configPath)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Top-level environments
	if len(cfg.Environments) != 2 {
		t.Errorf("len(Environments) = %d, want 2", len(cfg.Environments))
	}
	if cfg.Environments[0].Name != "dev" || cfg.Environments[1].Name != "prod" {
		t.Errorf("Environments = %v, want [dev prod]", cfg.Environments)
	}

	// Build fields
	if len(cfg.Builds[0].DependsOn) != 1 || cfg.Builds[0].DependsOn[0] != "base" {
		t.Errorf("Builds[0].DependsOn = %v, want [base]", cfg.Builds[0].DependsOn)
	}
	if cfg.Builds[0].RunPolicy != "always" {
		t.Errorf("Builds[0].RunPolicy = %q, want always", cfg.Builds[0].RunPolicy)
	}
	if cfg.Builds[0].OnFailure != "continue" {
		t.Errorf("Builds[0].OnFailure = %q, want continue", cfg.Builds[0].OnFailure)
	}
	if cfg.Builds[0].Retries != 2 {
		t.Errorf("Builds[0].Retries = %d, want 2", cfg.Builds[0].Retries)
	}
	if cfg.Builds[0].Inputs["key"] != "value" {
		t.Errorf("Builds[0].Inputs[key] = %v, want value", cfg.Builds[0].Inputs["key"])
	}
	if cfg.Builds[0].EnvInputs["dev"]["key"] != "dev-value" {
		t.Errorf("Builds[0].EnvInputs[dev][key] = %v, want dev-value", cfg.Builds[0].EnvInputs["dev"]["key"])
	}

	// Deploy fields
	if cfg.Deploys[0].RunPolicy != "default" {
		t.Errorf("Deploys[0].RunPolicy = %q, want default", cfg.Deploys[0].RunPolicy)
	}
	if cfg.Deploys[0].OnFailure != "abort" {
		t.Errorf("Deploys[0].OnFailure = %q, want abort", cfg.Deploys[0].OnFailure)
	}
	if cfg.Deploys[0].Retries != 0 {
		t.Errorf("Deploys[0].Retries = %d, want 0", cfg.Deploys[0].Retries)
	}
}

func TestParse_ReleaseAndChangelogConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	configContent := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
      - prod
    release_build:
      tag: goreleaser.tag
    changelog:
      workflow: .github/workflows/custom-changelog.yaml
    builds:
      - name: goreleaser
        workflow: .github/workflows/goreleaser.yaml
        triggers:
          - "cmd/**"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := Parse(configPath)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Release config
	if cfg.ReleaseBuild == nil {
		t.Fatal("Release is nil")
		return
	}
	if cfg.ReleaseBuild.IsDisabled() {
		t.Error("Release.Disabled should be false (enabled by default)")
	}
	if cfg.ReleaseBuild.Tag != "goreleaser.tag" {
		t.Errorf("Release.Tag = %q, want goreleaser.tag", cfg.ReleaseBuild.Tag)
	}

	// Changelog config
	if cfg.Changelog == nil {
		t.Fatal("Changelog is nil")
		return
	}
	if cfg.Changelog.Workflow != ".github/workflows/custom-changelog.yaml" {
		t.Errorf("Changelog.Workflow = %q, want .github/workflows/custom-changelog.yaml", cfg.Changelog.Workflow)
	}
}

func TestParse_ReleaseDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "manifest.yaml")

	configContent := `ci:
  config:
    trunk_branch: main
    environments:
      - dev
    release_build:
      disabled: true
    builds:
      - name: app
        workflow: .github/workflows/build.yaml
        triggers:
          - "src/**"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := Parse(configPath)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if cfg.ReleaseBuild == nil {
		t.Fatal("Release is nil")
		return
	}
	if !cfg.ReleaseBuild.IsDisabled() {
		t.Error("Release.Disabled should be true")
	}
}

func TestValidate_NewFields(t *testing.T) {
	tests := []struct {
		name     string
		cfg      TrunkConfig
		wantErrs []string
	}{
		{
			name: "valid run_policy values",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "a", Workflow: "w.yaml", RunPolicy: "default"},
					{Name: "b", Workflow: "w.yaml", RunPolicy: "always"},
					{Name: "c", Workflow: "w.yaml", RunPolicy: "force"},
				},
			},
			wantErrs: nil,
		},
		{
			name: "invalid run_policy",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "a", Workflow: "w.yaml", RunPolicy: "invalid"},
				},
			},
			wantErrs: []string{"builds[0].run_policy must be one of: default, always, force"},
		},
		{
			name: "invalid on_failure",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "a", Workflow: "w.yaml", OnFailure: "invalid"},
				},
			},
			wantErrs: []string{"builds[0].on_failure must be one of: abort, continue"},
		},
		{
			name: "retries out of range",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "a", Workflow: "w.yaml", Retries: 5},
				},
			},
			wantErrs: []string{"builds[0].retries must be between 0 and 3"},
		},
		{
			name: "invalid depends_on reference",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "a", Workflow: "w.yaml", DependsOn: []string{"nonexistent"}},
				},
			},
			wantErrs: []string{"builds[0].depends_on: dependency 'nonexistent' not found in builds, deploys, or external"},
		},
		{
			name: "build env_inputs key not in environments",
			cfg: TrunkConfig{
				Environments: EnvNames("dev", "prod"),
				Builds: []BuildConfig{
					{Name: "a", Workflow: "w.yaml", EnvInputs: map[string]map[string]interface{}{
						"staging": {"key": "value"},
					}},
				},
			},
			wantErrs: []string{"builds[0].env_inputs has key 'staging' which is not in environments [dev prod]"},
		},
		{
			name: "circular dependency",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "a", Workflow: "w.yaml", DependsOn: []string{"b"}},
					{Name: "b", Workflow: "w.yaml", DependsOn: []string{"a"}},
				},
			},
			wantErrs: []string{"circular dependency detected: build-a -> build-b -> build-a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(&tt.cfg)
			if tt.wantErrs == nil {
				if len(errs) != 0 {
					t.Errorf("Validate() returned errors, want none: %v", errs)
				}
			} else {
				for _, want := range tt.wantErrs {
					found := false
					for _, err := range errs {
						// For circular dependency errors, check if it contains the key phrase
						// since the exact path can vary depending on traversal order
						if want == "circular dependency detected: build-a -> build-b -> build-a" {
							if err == "circular dependency detected: build-a -> build-b -> build-a" || err == "circular dependency detected: build-b -> build-a -> build-b" {
								found = true
								break
							}
						} else if err == want {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("Validate() missing expected error: %q, got: %v", want, errs)
					}
				}
			}
		})
	}
}

func TestHasExternalRelease(t *testing.T) {
	tests := []struct {
		name     string
		release  *ReleaseBuildConfig
		expected bool
	}{
		{"nil release", nil, false},
		{"no tag", &ReleaseBuildConfig{}, false},
		{"with tag", &ReleaseBuildConfig{Tag: "goreleaser.tag"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TrunkConfig{ReleaseBuild: tt.release}
			if got := cfg.HasExternalRelease(); got != tt.expected {
				t.Errorf("HasExternalRelease() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestHasCustomChangelog(t *testing.T) {
	tests := []struct {
		name      string
		changelog *ChangelogConfig
		expected  bool
	}{
		{"nil changelog", nil, false},
		{"empty workflow", &ChangelogConfig{}, false},
		{"with workflow", &ChangelogConfig{Workflow: ".github/workflows/changelog.yaml"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TrunkConfig{Changelog: tt.changelog}
			if got := cfg.HasCustomChangelog(); got != tt.expected {
				t.Errorf("HasCustomChangelog() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestChangelogEnabled(t *testing.T) {
	tests := []struct {
		name      string
		changelog *ChangelogConfig
		expected  bool
	}{
		{"nil changelog - enabled by default", nil, true},
		{"disabled false - enabled", &ChangelogConfig{Disabled: boolPtr(false)}, true},
		{"disabled true - explicitly disabled", &ChangelogConfig{Disabled: boolPtr(true)}, false},
		{"with workflow - custom enabled", &ChangelogConfig{Workflow: ".github/workflows/changelog.yaml"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TrunkConfig{Changelog: tt.changelog}
			if got := cfg.ChangelogEnabled(); got != tt.expected {
				t.Errorf("ChangelogEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestReleaseEnabled(t *testing.T) {
	tests := []struct {
		name     string
		release  *ReleaseBuildConfig
		expected bool
	}{
		{"nil release - enabled by default", nil, true},
		{"disabled false - enabled", &ReleaseBuildConfig{Disabled: boolPtr(false)}, true},
		{"disabled true - explicitly disabled", &ReleaseBuildConfig{Disabled: boolPtr(true)}, false},
		{"with tag - external release enabled", &ReleaseBuildConfig{Tag: "goreleaser.tag"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &TrunkConfig{ReleaseBuild: tt.release}
			if got := cfg.ReleaseEnabled(); got != tt.expected {
				t.Errorf("ReleaseEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestValidate_ReleaseTag(t *testing.T) {
	tests := []struct {
		name     string
		cfg      TrunkConfig
		wantErrs []string
	}{
		{
			name: "valid release_build.tag reference",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				ReleaseBuild:      &ReleaseBuildConfig{Tag: "goreleaser.tag"},
				Builds: []BuildConfig{
					{Name: "goreleaser", Workflow: "w.yaml"},
				},
			},
			wantErrs: nil,
		},
		{
			name: "invalid release_build.tag - unknown callback",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				ReleaseBuild:      &ReleaseBuildConfig{Tag: "nonexistent.tag"},
				Builds: []BuildConfig{
					{Name: "app", Workflow: "w.yaml"},
				},
			},
			wantErrs: []string{"release_build.tag references unknown callback: nonexistent"},
		},
		{
			name: "invalid release_build.tag - bad format",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				ReleaseBuild:      &ReleaseBuildConfig{Tag: "invalid"},
				Builds: []BuildConfig{
					{Name: "app", Workflow: "w.yaml"},
				},
			},
			wantErrs: []string{"release_build.tag invalid format"},
		},
		{
			name: "release_build.tag with deploy callback",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				ReleaseBuild:      &ReleaseBuildConfig{Tag: "release.version"},
				Deploys: []DeployConfig{
					{Name: "release", Workflow: "w.yaml"},
				},
			},
			wantErrs: nil,
		},
		{
			name: "deploy depends_on references valid build",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "app", Workflow: "w.yaml", Triggers: []string{"src/**"}},
				},
				Deploys: []DeployConfig{
					{Name: "app-deploy", Workflow: "d.yaml", DependsOn: []string{"app"}},
				},
			},
			wantErrs: nil,
		},
		{
			name: "deploy depends_on references unknown build",
			cfg: TrunkConfig{
				Environments: EnvNames("dev"),
				Builds: []BuildConfig{
					{Name: "app", Workflow: "w.yaml", Triggers: []string{"src/**"}},
				},
				Deploys: []DeployConfig{
					{Name: "app-deploy", Workflow: "d.yaml", DependsOn: []string{"nonexistent"}},
				},
			},
			wantErrs: []string{"deploys[0].depends_on: dependency 'nonexistent' not found in builds, deploys, or external"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(&tt.cfg)
			if tt.wantErrs == nil {
				if len(errs) != 0 {
					t.Errorf("Validate() returned errors, want none: %v", errs)
				}
			} else {
				for _, want := range tt.wantErrs {
					found := false
					for _, err := range errs {
						if strings.Contains(err, want) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("Validate() missing expected error containing: %q, got: %v", want, errs)
					}
				}
			}
		})
	}
}

// TestValidate_JobIDSafeNames asserts that build, deploy, external-deploy, and
// environment names which would become part of a GitHub Actions job ID
// (build-<name>, deploy-<name>, and env-keyed identifiers) are rejected at
// config validation when they contain characters outside the job-ID grammar.
//
// A GitHub job ID must start with a letter or _ and contain only [A-Za-z0-9_-].
// Because the name is used as a suffix after build-/deploy-, a leading digit,
// uppercase letters, and hyphens are all fine; only characters outside the
// allowed set (such as ., spaces, and /) are rejected. Sanitizing is avoided
// deliberately: two distinct names could collapse to one job ID.
func TestValidate_JobIDSafeNames(t *testing.T) {
	tests := []struct {
		name     string
		config   TrunkConfig
		wantErr  string // substring that must appear; "" means expect no errors
		wantNone bool
	}{
		{
			name: "build name with dot rejected",
			config: TrunkConfig{
				TrunkBranch:  "main",
				Environments: EnvNames("dev"),
				Builds:       []BuildConfig{{Name: "app.web", Workflow: ".github/workflows/build.yaml"}},
			},
			wantErr: `builds[0].name "app.web"`,
		},
		{
			name: "build name with space rejected",
			config: TrunkConfig{
				TrunkBranch:  "main",
				Environments: EnvNames("dev"),
				Builds:       []BuildConfig{{Name: "my app", Workflow: ".github/workflows/build.yaml"}},
			},
			wantErr: `builds[0].name "my app"`,
		},
		{
			name: "deploy name with slash rejected",
			config: TrunkConfig{
				TrunkBranch:  "main",
				Environments: EnvNames("dev"),
				Deploys:      []DeployConfig{{Name: "svc/api", Workflow: ".github/workflows/deploy.yaml"}},
			},
			wantErr: `deploys[0].name "svc/api"`,
		},
		{
			name: "external deploy name with dot rejected",
			config: TrunkConfig{
				TrunkBranch:  "main",
				Environments: EnvNames("dev"),
				External: []ExternalRepoConfig{{
					Repo:    "owner/repo",
					Deploys: []ExternalDeployConfig{{Name: "svc.api", Workflow: ".github/workflows/deploy.yaml"}},
				}},
			},
			wantErr: `external[0].deploys[0].name "svc.api"`,
		},
		{
			name: "environment name with dot rejected",
			config: TrunkConfig{
				TrunkBranch:  "main",
				Environments: EnvNames("us.east"),
			},
			wantErr: `environments[0] "us.east"`,
		},
		{
			name: "valid names: hyphen, uppercase, leading digit, underscore",
			config: TrunkConfig{
				TrunkBranch:  "main",
				Environments: EnvNames("dev-1", "Prod", "2nd", "us_west"),
				Builds: []BuildConfig{
					{Name: "my-app", Workflow: ".github/workflows/build.yaml"},
					{Name: "MyApp", Workflow: ".github/workflows/build.yaml"},
					{Name: "_internal", Workflow: ".github/workflows/build.yaml"},
				},
				Deploys: []DeployConfig{
					{Name: "1svc", Workflow: ".github/workflows/deploy.yaml"},
				},
			},
			wantNone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			errs := Validate(&cfg)
			if tt.wantNone {
				for _, e := range errs {
					if strings.Contains(e, "must contain only") {
						t.Errorf("Validate() unexpectedly rejected a valid name: %q", e)
					}
				}
				return
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e, tt.wantErr) && strings.Contains(e, "must contain only") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Validate() missing job-ID-safe error containing %q, got: %v", tt.wantErr, errs)
			}
		})
	}
}

// TestExternalDeployOnUpdate_Parse verifies that the on_update.deploy block
// parses into the new types from YAML and that a present block with no workflow
// is rejected while an absent block keeps the receiver record-only.
func TestExternalDeployOnUpdate_Parse(t *testing.T) {
	tmpDir := t.TempDir()

	writeManifest := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(tmpDir, "manifest.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		return p
	}

	const withDeploy = `ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
    external:
      - repo: example/cdk-infra
        ref: main
        deploys:
          - name: cdk
            workflow: example/cdk-infra/.github/workflows/deploy.yaml
            on_update:
              deploy:
                workflow: example/cdk-infra/.github/workflows/deploy.yaml
`

	cfg, err := Parse(writeManifest(t, withDeploy))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(cfg.External) != 1 || len(cfg.External[0].Deploys) != 1 {
		t.Fatalf("unexpected external shape: %+v", cfg.External)
	}
	d := cfg.External[0].Deploys[0]
	if d.OnUpdate == nil || d.OnUpdate.Deploy == nil {
		t.Fatalf("on_update.deploy did not parse: %+v", d.OnUpdate)
	}
	if got, want := d.OnUpdate.Deploy.Workflow, "example/cdk-infra/.github/workflows/deploy.yaml"; got != want {
		t.Errorf("on_update.deploy.workflow = %q, want %q", got, want)
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Errorf("Validate() rejected a valid on_update config: %v", errs)
	}
}

// TestExternalDeployOnUpdate_Validation covers the additive validation: absent
// block stays record-only and valid, while on_update.deploy with an empty
// workflow is rejected with the documented message.
func TestExternalDeployOnUpdate_Validation(t *testing.T) {
	base := func() *TrunkConfig {
		return &TrunkConfig{
			TrunkBranch:  "main",
			Environments: EnvNames("dev", "prod"),
			External: []ExternalRepoConfig{
				{
					Repo: "example/cdk-infra",
					Ref:  "main",
					Deploys: []ExternalDeployConfig{
						{Name: "cdk", Workflow: "example/cdk-infra/.github/workflows/deploy.yaml"},
					},
				},
			},
		}
	}

	t.Run("absent on_update is record-only and valid", func(t *testing.T) {
		cfg := base()
		if errs := Validate(cfg); len(errs) != 0 {
			t.Errorf("Validate() returned errors for record-only config: %v", errs)
		}
	})

	t.Run("on_update.deploy without workflow is rejected", func(t *testing.T) {
		cfg := base()
		cfg.External[0].Deploys[0].OnUpdate = &OnUpdateConfig{Deploy: &OnUpdateDeploy{}}
		errs := Validate(cfg)
		found := false
		for _, e := range errs {
			if strings.Contains(e, "on_update.deploy.workflow is required when on_update.deploy is set") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Validate() missing on_update.deploy.workflow required error, got: %v", errs)
		}
	})

	t.Run("on_update.deploy with workflow is valid", func(t *testing.T) {
		cfg := base()
		cfg.External[0].Deploys[0].OnUpdate = &OnUpdateConfig{
			Deploy: &OnUpdateDeploy{Workflow: "example/cdk-infra/.github/workflows/deploy.yaml"},
		}
		if errs := Validate(cfg); len(errs) != 0 {
			t.Errorf("Validate() rejected a valid on_update config: %v", errs)
		}
	})
}

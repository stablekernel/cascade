package config

import "testing"

// TestValidateEnvironmentConfigFields exercises the additive per-environment
// fields under environment_config. Validation is lenient and applies only when a
// field is present, so a manifest that omits these fields is never rejected.
// Secrets and variables are NAMES only and are checked for a safe name shape.
func TestValidateEnvironmentConfigFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		envConfig   map[string]EnvironmentConfig
		wantErr     bool
		errContains string
	}{
		{
			name:      "nil environment_config is valid",
			envConfig: nil,
			wantErr:   false,
		},
		{
			name:      "empty per-env block is valid (shape only)",
			envConfig: map[string]EnvironmentConfig{"prod": {}},
			wantErr:   false,
		},
		{
			name: "full valid block",
			envConfig: map[string]EnvironmentConfig{
				"prod": {
					GHAEnvironment:    "production",
					RequiredReviewers: []string{"octocat", "team/ops"},
					WaitTimer:         intPtr(10),
					BranchPolicy:      EnvBranchPolicyCustom,
					BranchPatterns:    []string{"main", "release/*"},
					TagPatterns:       []string{"v*"},
					Secrets:           []string{"MY_SECRET", "DB_PASSWORD"},
					Variables:         []string{"REGION", "TIER"},
				},
			},
			wantErr: false,
		},
		{
			name: "wait_timer zero is valid",
			envConfig: map[string]EnvironmentConfig{
				"prod": {WaitTimer: intPtr(0)},
			},
			wantErr: false,
		},
		{
			name: "wait_timer at maximum is valid",
			envConfig: map[string]EnvironmentConfig{
				"prod": {WaitTimer: intPtr(MaxWaitTimerMinutes)},
			},
			wantErr: false,
		},
		{
			name: "wait_timer above maximum is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {WaitTimer: intPtr(MaxWaitTimerMinutes + 1)},
			},
			wantErr:     true,
			errContains: "wait_timer must be between 0 and 43200 minutes",
		},
		{
			name: "negative wait_timer is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {WaitTimer: intPtr(-1)},
			},
			wantErr:     true,
			errContains: "wait_timer must be between 0 and 43200 minutes",
		},
		{
			name: "protected branch policy is valid",
			envConfig: map[string]EnvironmentConfig{
				"prod": {BranchPolicy: EnvBranchPolicyProtected},
			},
			wantErr: false,
		},
		{
			name: "all branch policy is valid",
			envConfig: map[string]EnvironmentConfig{
				"prod": {BranchPolicy: EnvBranchPolicyAll},
			},
			wantErr: false,
		},
		{
			name: "unknown branch policy is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {BranchPolicy: "sometimes"},
			},
			wantErr:     true,
			errContains: "branch_policy must be one of: protected, custom, all",
		},
		{
			name: "branch_patterns without custom policy is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {BranchPolicy: EnvBranchPolicyProtected, BranchPatterns: []string{"main"}},
			},
			wantErr:     true,
			errContains: "branch_patterns is only valid when branch_policy is custom",
		},
		{
			name: "tag_patterns without custom policy is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {TagPatterns: []string{"v*"}},
			},
			wantErr:     true,
			errContains: "tag_patterns is only valid when branch_policy is custom",
		},
		{
			name: "empty reviewer slug is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {RequiredReviewers: []string{""}},
			},
			wantErr:     true,
			errContains: "required_reviewers[0]",
		},
		{
			name: "whitespace reviewer slug is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {RequiredReviewers: []string{"team ops"}},
			},
			wantErr:     true,
			errContains: "required_reviewers[0]",
		},
		{
			name: "reviewer slug with too many segments is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {RequiredReviewers: []string{"my-org/team/ops"}},
			},
			wantErr:     true,
			errContains: "required_reviewers[0]",
		},
		{
			name: "secret name starting with a digit is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {Secrets: []string{"1SECRET"}},
			},
			wantErr:     true,
			errContains: "secrets[0]",
		},
		{
			name: "secret name with interpolation is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {Secrets: []string{"${{ secrets.X }}"}},
			},
			wantErr:     true,
			errContains: "secrets[0]",
		},
		{
			name: "variable name with whitespace is rejected",
			envConfig: map[string]EnvironmentConfig{
				"prod": {Variables: []string{"BAD NAME"}},
			},
			wantErr:     true,
			errContains: "variables[0]",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &TrunkConfig{
				Environments:      []string{"prod"},
				EnvironmentConfig: tt.envConfig,
			}
			errs := validateEnvironmentConfig(cfg)
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

// TestParseEnvironmentConfigReservedFields asserts a manifest carrying the
// additive environment_config fields parses into the typed fields, validates at
// CurrentSchemaVersion, and does not bump schema_version.
func TestParseEnvironmentConfigReservedFields(t *testing.T) {
	t.Parallel()

	cfg := parseInline(t, `
environments: [dev, prod]
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
environment_config:
  prod:
    gha_environment: production
    required_reviewers: [octocat, team/ops]
    wait_timer: 10
    branch_policy: custom
    branch_patterns: [main, "release/*"]
    tag_patterns: ["v*"]
    secrets: [MY_SECRET, DB_PASSWORD]
    variables: [REGION, TIER]
`)

	ec, ok := cfg.EnvironmentConfig["prod"]
	if !ok {
		t.Fatalf("environment_config.prod did not parse")
	}
	if ec.GHAEnvironment != "production" {
		t.Fatalf("gha_environment: %q", ec.GHAEnvironment)
	}
	if got, want := len(ec.RequiredReviewers), 2; got != want {
		t.Fatalf("required_reviewers len = %d, want %d", got, want)
	}
	if ec.WaitTimerMinutes() != 10 {
		t.Fatalf("wait_timer = %d, want 10", ec.WaitTimerMinutes())
	}
	if ec.BranchPolicy != EnvBranchPolicyCustom {
		t.Fatalf("branch_policy = %q", ec.BranchPolicy)
	}
	if got, want := len(ec.BranchPatterns), 2; got != want {
		t.Fatalf("branch_patterns len = %d, want %d", got, want)
	}
	if got, want := len(ec.TagPatterns), 1; got != want {
		t.Fatalf("tag_patterns len = %d, want %d", got, want)
	}
	if got, want := len(ec.Secrets), 2; got != want {
		t.Fatalf("secrets len = %d, want %d", got, want)
	}
	if got, want := len(ec.Variables), 2; got != want {
		t.Fatalf("variables len = %d, want %d", got, want)
	}

	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if got := cfg.GetSchemaVersion(); got != CurrentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d (additive fields must not bump)", got, CurrentSchemaVersion)
	}
	if CurrentSchemaVersion != 1 {
		t.Fatalf("CurrentSchemaVersion = %d, want 1 (additive fields must not bump)", CurrentSchemaVersion)
	}
}

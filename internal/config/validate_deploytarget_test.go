package config

import "testing"

// TestValidateDeployTargetReservedFields exercises the reserved GitOps fields
// (branch, track_sha) on the deploy_target block. They are meaningful only when
// mode is gitops; branch is also checked for a safe ref shape regardless of mode.
func TestValidateDeployTargetReservedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		target      *DeployTarget
		wantErr     bool
		errContains string
	}{
		{
			name: "gitops with branch and track_sha is valid",
			target: &DeployTarget{
				Mode:     DeployTargetModeGitOps,
				Repo:     "org/gitops-config",
				Path:     "envs/prod/values.yaml",
				Field:    "image.tag",
				Value:    "v1.2.3",
				Branch:   "release/prod",
				TrackSHA: true,
			},
			wantErr: false,
		},
		{
			name:        "dispatch with track_sha is rejected",
			target:      &DeployTarget{Mode: DeployTargetModeDispatch, TrackSHA: true},
			wantErr:     true,
			errContains: "track_sha is only valid when mode is gitops",
		},
		{
			name:        "empty mode (default dispatch) with track_sha is rejected",
			target:      &DeployTarget{TrackSHA: true},
			wantErr:     true,
			errContains: "track_sha is only valid when mode is gitops",
		},
		{
			name:        "dispatch with branch is rejected",
			target:      &DeployTarget{Mode: DeployTargetModeDispatch, Branch: "release/prod"},
			wantErr:     true,
			errContains: "branch is only valid when mode is gitops",
		},
		{
			name:        "branch with leading slash is rejected",
			target:      &DeployTarget{Mode: DeployTargetModeGitOps, Branch: "/bad"},
			wantErr:     true,
			errContains: "must not have leading/trailing slashes or whitespace",
		},
		{
			name:        "branch with trailing slash is rejected",
			target:      &DeployTarget{Mode: DeployTargetModeGitOps, Branch: "bad/"},
			wantErr:     true,
			errContains: "must not have leading/trailing slashes or whitespace",
		},
		{
			name:        "branch with whitespace is rejected",
			target:      &DeployTarget{Mode: DeployTargetModeGitOps, Branch: "has space"},
			wantErr:     true,
			errContains: "must not have leading/trailing slashes or whitespace",
		},
		{
			name:    "gitops with internal-slash branch is valid",
			target:  &DeployTarget{Mode: DeployTargetModeGitOps, Branch: "release/prod"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			errs := validateDeployTarget("deploys[0]", tt.target)
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

// TestValidateDeployTargetReservedFieldsViaManifest asserts a manifest carrying
// the enriched deploy_target shape parses and validates at CurrentSchemaVersion
// without bumping schema_version.
func TestValidateDeployTargetReservedFieldsViaManifest(t *testing.T) {
	t.Parallel()

	cfg := parseInline(t, `
environments: [dev, prod]
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
    deploy_target:
      mode: gitops
      repo: org/gitops-config
      path: envs/prod/values.yaml
      field: image.tag
      value: "v1.2.3"
      branch: release/prod
      track_sha: true
`)
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

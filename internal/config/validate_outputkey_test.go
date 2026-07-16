package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// outputKeyBaseConfig is a minimal valid multi-env manifest used to isolate the
// output-key collision validation rules.
func outputKeyBaseConfig() *TrunkConfig {
	return &TrunkConfig{
		TrunkBranch:  "main",
		Environments: EnvNames("dev", "prod"),
	}
}

// TestValidate_OutputKeyCollisions covers the hyphen/underscore collapse rule:
// job IDs and expression references replace hyphens with underscores (see
// OutputKey), so two names in the same section that differ only by "-" versus
// "_" would emit the same output key and one would silently shadow the other.
// Such manifests must be rejected at validation time.
func TestValidate_OutputKeyCollisions(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(cfg *TrunkConfig)
		wantErrs   bool
		wantSubstr []string
	}{
		{
			name: "colliding build names rejected",
			mutate: func(cfg *TrunkConfig) {
				cfg.Builds = []BuildConfig{
					{Name: "api-x", Workflow: ".github/workflows/build1.yaml"},
					{Name: "api_x", Workflow: ".github/workflows/build2.yaml"},
				}
			},
			wantErrs:   true,
			wantSubstr: []string{"api-x", "api_x", "output key"},
		},
		{
			name: "colliding deploy names rejected",
			mutate: func(cfg *TrunkConfig) {
				cfg.Deploys = []DeployConfig{
					{Name: "svc-a", Workflow: ".github/workflows/deploy1.yaml"},
					{Name: "svc_a", Workflow: ".github/workflows/deploy2.yaml"},
				}
			},
			wantErrs:   true,
			wantSubstr: []string{"svc-a", "svc_a", "output key"},
		},
		{
			name: "colliding environment names rejected",
			mutate: func(cfg *TrunkConfig) {
				cfg.Environments = EnvNames("dev", "stage-1", "stage_1")
			},
			wantErrs:   true,
			wantSubstr: []string{"stage-1", "stage_1", "output key"},
		},
		{
			name: "distinct hyphenated names still pass",
			mutate: func(cfg *TrunkConfig) {
				cfg.Builds = []BuildConfig{
					{Name: "api-x", Workflow: ".github/workflows/build1.yaml"},
					{Name: "api-y", Workflow: ".github/workflows/build2.yaml"},
				}
				cfg.Deploys = []DeployConfig{
					{Name: "svc_a", Workflow: ".github/workflows/deploy1.yaml"},
					{Name: "svc_b", Workflow: ".github/workflows/deploy2.yaml"},
				}
			},
			wantErrs: false,
		},
		{
			name: "single entries unaffected",
			mutate: func(cfg *TrunkConfig) {
				cfg.Builds = []BuildConfig{
					{Name: "my-app", Workflow: ".github/workflows/build.yaml"},
				}
				cfg.Deploys = []DeployConfig{
					{Name: "my_app", Workflow: ".github/workflows/deploy.yaml"},
				}
			},
			wantErrs: false,
		},
		{
			name: "build and deploy may share a collapsed key across sections",
			mutate: func(cfg *TrunkConfig) {
				// Output keys are prefixed per section (run_build_* versus
				// run_deploy_*), so a cross-section twin never collides.
				cfg.Builds = []BuildConfig{
					{Name: "api-x", Workflow: ".github/workflows/build.yaml"},
				}
				cfg.Deploys = []DeployConfig{
					{Name: "api_x", Workflow: ".github/workflows/deploy.yaml"},
				}
			},
			wantErrs: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := outputKeyBaseConfig()
			tt.mutate(cfg)
			errs := Validate(cfg)
			if tt.wantErrs {
				assert.NotEmpty(t, errs, "colliding output keys must be rejected")
				joined := strings.Join(errs, "\n")
				for _, sub := range tt.wantSubstr {
					assert.Contains(t, joined, sub)
				}
			} else {
				assert.Empty(t, errs, "non-colliding names must pass validation")
			}
		})
	}
}

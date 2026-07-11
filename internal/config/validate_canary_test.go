package config

import (
	"strings"
	"testing"
)

// TestValidateCanaryFields exercises the reserved canary sub-block validation
// rules added alongside the CanaryConfig field expansion.
func TestValidateCanaryFields(t *testing.T) {
	t.Parallel()

	t.Run("canary and rollout.type are reserved and rejected by lint", func(t *testing.T) {
		t.Parallel()
		cfg := parseInline(t, `
environments: [dev, prod]
deploys:
  - name: app
    workflow: .github/workflows/deploy.yaml
    rollout:
      type: canary
      canary:
        percent: 10
        bake_time: 30m
        promote_callback: .github/workflows/promote.yaml
        rollback_callback: .github/workflows/rollback.yaml
`)
		errs := Validate(cfg)
		if !hasErrContaining(errs, "deploys[0].rollout.type is reserved and not implemented in this cascade version") {
			t.Fatalf("expected reserved rollout.type rejection, got %v", errs)
		}
		if !hasErrContaining(errs, "deploys[0].rollout.canary is reserved and not implemented in this cascade version") {
			t.Fatalf("expected reserved rollout.canary rejection, got %v", errs)
		}
	})

	t.Run("percent valid range", func(t *testing.T) {
		t.Parallel()
		for _, pct := range []int{1, 50, 100} {
			pct := pct
			t.Run("", func(t *testing.T) {
				t.Parallel()
				c := &CanaryConfig{Percent: pct}
				if errs := validateCanaryConfig("deploys[0]", c); len(errs) != 0 {
					t.Errorf("percent %d: expected no errors, got %v", pct, errs)
				}
			})
		}
	})

	t.Run("percent zero is unset and valid", func(t *testing.T) {
		t.Parallel()
		c := &CanaryConfig{Percent: 0}
		if errs := validateCanaryConfig("deploys[0]", c); len(errs) != 0 {
			t.Fatalf("percent 0 (unset): expected no errors, got %v", errs)
		}
	})

	t.Run("percent 101 rejected", func(t *testing.T) {
		t.Parallel()
		c := &CanaryConfig{Percent: 101}
		errs := validateCanaryConfig("deploys[0]", c)
		if !hasErrContaining(errs, "rollout.canary.percent must be between 1 and 100") {
			t.Fatalf("expected percent rejection, got %v", errs)
		}
	})

	t.Run("percent -1 rejected", func(t *testing.T) {
		t.Parallel()
		c := &CanaryConfig{Percent: -1}
		errs := validateCanaryConfig("deploys[0]", c)
		if !hasErrContaining(errs, "rollout.canary.percent must be between 1 and 100") {
			t.Fatalf("expected percent rejection, got %v", errs)
		}
	})

	t.Run("bake_time valid durations", func(t *testing.T) {
		t.Parallel()
		for _, d := range []string{"30s", "5m"} {
			d := d
			t.Run(d, func(t *testing.T) {
				t.Parallel()
				c := &CanaryConfig{BakeTime: d}
				if errs := validateCanaryConfig("deploys[0]", c); len(errs) != 0 {
					t.Errorf("bake_time %q: expected no errors, got %v", d, errs)
				}
			})
		}
	})

	t.Run("bake_time invalid rejected", func(t *testing.T) {
		t.Parallel()
		c := &CanaryConfig{BakeTime: "notaduration"}
		errs := validateCanaryConfig("deploys[0]", c)
		if !hasErrContaining(errs, "rollout.canary.bake_time must be a valid Go duration") {
			t.Fatalf("expected bake_time rejection, got %v", errs)
		}
	})

	t.Run("promote_callback valid paths", func(t *testing.T) {
		t.Parallel()
		for _, path := range []string{".github/workflows/x.yml", "x.yml"} {
			path := path
			t.Run(path, func(t *testing.T) {
				t.Parallel()
				c := &CanaryConfig{PromoteCallback: path}
				if errs := validateCanaryConfig("deploys[0]", c); len(errs) != 0 {
					t.Errorf("promote_callback %q: expected no errors, got %v", path, errs)
				}
			})
		}
	})

	t.Run("promote_callback invalid paths rejected", func(t *testing.T) {
		t.Parallel()
		for _, path := range []string{"../evil.yml", "foo/bar.yml"} {
			path := path
			t.Run(path, func(t *testing.T) {
				t.Parallel()
				c := &CanaryConfig{PromoteCallback: path}
				errs := validateCanaryConfig("deploys[0]", c)
				if !hasErrContaining(errs, "promote_callback") {
					t.Errorf("promote_callback %q: expected rejection, got %v", path, errs)
				}
				for _, e := range errs {
					if strings.Contains(e, "promote_callback") && !strings.Contains(e, "local callback workflow must be") {
						t.Errorf("promote_callback %q: error has wrong message: %v", path, errs)
					}
				}
			})
		}
	})

	t.Run("rollback_callback valid paths", func(t *testing.T) {
		t.Parallel()
		for _, path := range []string{".github/workflows/x.yml", "x.yml"} {
			path := path
			t.Run(path, func(t *testing.T) {
				t.Parallel()
				c := &CanaryConfig{RollbackCallback: path}
				if errs := validateCanaryConfig("deploys[0]", c); len(errs) != 0 {
					t.Errorf("rollback_callback %q: expected no errors, got %v", path, errs)
				}
			})
		}
	})

	t.Run("rollback_callback invalid paths rejected", func(t *testing.T) {
		t.Parallel()
		for _, path := range []string{"../evil.yml", "foo/bar.yml"} {
			path := path
			t.Run(path, func(t *testing.T) {
				t.Parallel()
				c := &CanaryConfig{RollbackCallback: path}
				errs := validateCanaryConfig("deploys[0]", c)
				if !hasErrContaining(errs, "rollback_callback") {
					t.Errorf("rollback_callback %q: expected rejection, got %v", path, errs)
				}
			})
		}
	})
}

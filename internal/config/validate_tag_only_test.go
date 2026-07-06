package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func errsContain(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

// TestValidate_ReleaseTagOnly asserts the guardrails on release.tag_only: it
// requires a release.workflow (the dispatch target) and release_trigger: dispatch
// (the mode that emits the dispatch). Without both, a tag-only cut would produce
// a tag that nothing builds or publishes.
func TestValidate_ReleaseTagOnly(t *testing.T) {
	base := func() *TrunkConfig {
		return &TrunkConfig{
			TrunkBranch:    "main",
			ReleaseTrigger: ReleaseTriggerDispatch,
			Release:        &ReleaseConfig{Workflow: ".github/workflows/release.yaml", TagOnly: true},
		}
	}

	t.Run("valid: workflow set and dispatch mode", func(t *testing.T) {
		errs := Validate(base())
		assert.False(t, errsContain(errs, "tag_only"), "well-formed tag_only config must not raise a tag_only error: %v", errs)
	})

	t.Run("missing workflow is rejected", func(t *testing.T) {
		cfg := base()
		cfg.Release.Workflow = ""
		errs := Validate(cfg)
		assert.True(t, errsContain(errs, "release.tag_only requires release.workflow"), "want workflow requirement error, got %v", errs)
	})

	t.Run("push mode is rejected", func(t *testing.T) {
		cfg := base()
		cfg.ReleaseTrigger = ReleaseTriggerPush
		errs := Validate(cfg)
		assert.True(t, errsContain(errs, "release.tag_only requires release_trigger: dispatch"), "want dispatch-mode requirement error, got %v", errs)
	})

	t.Run("unset tag_only imposes no requirement", func(t *testing.T) {
		cfg := base()
		cfg.Release.TagOnly = false
		cfg.Release.Workflow = ""
		cfg.ReleaseTrigger = ReleaseTriggerPush
		errs := Validate(cfg)
		assert.False(t, errsContain(errs, "tag_only"), "tag_only rules must not fire when tag_only is unset: %v", errs)
	})
}

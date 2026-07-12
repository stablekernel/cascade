package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// extraTriggersBaseConfig is a minimal valid manifest used to isolate the
// extra_triggers.merge_group validation rule.
func extraTriggersBaseConfig() *TrunkConfig {
	return &TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
	}
}

func TestValidate_ExtraTriggersMergeGroup_TopLevelRejected(t *testing.T) {
	cfg := extraTriggersBaseConfig()
	cfg.ExtraTriggers = &ExtraTriggers{MergeGroup: &MergeGroupTrigger{}}

	errs := Validate(cfg)
	joined := strings.Join(errs, "\n")

	assert.NotEmpty(t, errs, "extra_triggers.merge_group on the orchestrate workflow must be rejected")
	assert.Contains(t, joined, "extra_triggers.merge_group")
	assert.Contains(t, joined, "merge_queue.enabled",
		"the rejection must point the user at the merge_queue.enabled read-only lane")
}

func TestValidate_ExtraTriggersMergeGroup_PerComponentRejected(t *testing.T) {
	cfg := extraTriggersBaseConfig()
	cfg.Components = map[string]ComponentConfig{
		"api": {
			Path:       "services/api",
			TagGrammar: &TagGrammarConfig{Prefix: strptr("api-v")},
			ExtraTriggers: &ExtraTriggers{
				MergeGroup: &MergeGroupTrigger{},
			},
		},
	}

	errs := Validate(cfg)
	joined := strings.Join(errs, "\n")

	assert.NotEmpty(t, errs, "a component's extra_triggers.merge_group must be rejected")
	assert.Contains(t, joined, "components.api.extra_triggers.merge_group")
	assert.Contains(t, joined, "merge_queue.enabled")
}

func TestValidate_ExtraTriggersMergeGroup_MergeQueueEnabledAccepted(t *testing.T) {
	cfg := extraTriggersBaseConfig()
	cfg.MergeQueue = &MergeQueueConfig{Enabled: true}

	errs := Validate(cfg)
	assert.Empty(t, errs, "merge_queue.enabled is the supported read-only lane and must pass validation")
}

func TestValidate_ExtraTriggersMergeGroup_OtherExtraTriggersAccepted(t *testing.T) {
	cfg := extraTriggersBaseConfig()
	cfg.ExtraTriggers = &ExtraTriggers{
		Schedule:           []ScheduleEntry{{Cron: "0 2 * * *"}},
		RepositoryDispatch: &RepositoryDispatchTrigger{Types: []string{"external-update"}},
		WorkflowRun:        &WorkflowRunTrigger{Workflows: []string{"Upstream CI"}, Types: []string{"completed"}},
	}

	errs := Validate(cfg)
	assert.Empty(t, errs,
		"schedule, repository_dispatch, and workflow_run extra triggers stay legitimate and must pass validation")
}

func TestValidate_ExtraTriggersMergeGroup_NilIsValid(t *testing.T) {
	cfg := extraTriggersBaseConfig()
	cfg.ExtraTriggers = nil

	errs := Validate(cfg)
	assert.Empty(t, errs)
}

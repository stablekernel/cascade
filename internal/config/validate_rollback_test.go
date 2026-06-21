package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// rollbackBaseConfig is a minimal valid multi-env manifest used to isolate the
// rollback.repository_dispatch validation rules.
func rollbackBaseConfig() *TrunkConfig {
	return &TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
	}
}

func TestValidate_RollbackRepositoryDispatch_Valid(t *testing.T) {
	cfg := rollbackBaseConfig()
	cfg.Rollback = &RollbackConfig{
		RepositoryDispatch: &RepositoryDispatchTrigger{
			Types: []string{"rollback-requested"},
		},
	}
	errs := Validate(cfg)
	assert.Empty(t, errs, "a valid rollback.repository_dispatch must pass validation")
}

func TestValidate_RollbackRepositoryDispatch_NilIsValid(t *testing.T) {
	cfg := rollbackBaseConfig()
	cfg.Rollback = nil
	errs := Validate(cfg)
	assert.Empty(t, errs)
}

func TestValidate_RollbackRepositoryDispatch_EmptyTypesRejected(t *testing.T) {
	cfg := rollbackBaseConfig()
	cfg.Rollback = &RollbackConfig{
		RepositoryDispatch: &RepositoryDispatchTrigger{Types: nil},
	}
	errs := Validate(cfg)
	assert.NotEmpty(t, errs, "an empty repository_dispatch types list must be rejected")
	assert.Contains(t, strings.Join(errs, "\n"), "rollback.repository_dispatch")
}

func TestValidate_RollbackRepositoryDispatch_BlankTypeRejected(t *testing.T) {
	cfg := rollbackBaseConfig()
	cfg.Rollback = &RollbackConfig{
		RepositoryDispatch: &RepositoryDispatchTrigger{Types: []string{"  "}},
	}
	errs := Validate(cfg)
	assert.NotEmpty(t, errs, "a blank event type must be rejected")
}

func TestValidate_RollbackRepositoryDispatch_UnsafeTypeRejected(t *testing.T) {
	cfg := rollbackBaseConfig()
	cfg.Rollback = &RollbackConfig{
		RepositoryDispatch: &RepositoryDispatchTrigger{Types: []string{"bad type/name"}},
	}
	errs := Validate(cfg)
	assert.NotEmpty(t, errs, "an unsafe event type must be rejected")
}

// TestValidate_RollbackRepositoryDispatch_SchemaVersionUnchanged proves a
// manifest using rollback.repository_dispatch validates at CurrentSchemaVersion
// with no schema_version bump required.
func TestValidate_RollbackRepositoryDispatch_SchemaVersionUnchanged(t *testing.T) {
	cfg := rollbackBaseConfig()
	cfg.SchemaVersion = CurrentSchemaVersion
	cfg.Rollback = &RollbackConfig{
		RepositoryDispatch: &RepositoryDispatchTrigger{Types: []string{"rollback-requested"}},
	}
	errs := Validate(cfg)
	assert.Empty(t, errs)
	assert.Equal(t, CurrentSchemaVersion, cfg.GetSchemaVersion())
}

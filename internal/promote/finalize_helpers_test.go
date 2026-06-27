package promote

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetEnv covers both the present-value and default-fallback branches.
func TestGetEnv(t *testing.T) {
	t.Run("returns value when set", func(t *testing.T) {
		t.Setenv("CASCADE_GETENV_PROBE", "actual")
		assert.Equal(t, "actual", getEnv("CASCADE_GETENV_PROBE", "fallback"))
	})

	t.Run("returns default when empty", func(t *testing.T) {
		t.Setenv("CASCADE_GETENV_PROBE", "")
		assert.Equal(t, "fallback", getEnv("CASCADE_GETENV_PROBE", "fallback"))
	})

	t.Run("returns default when unset", func(t *testing.T) {
		assert.Equal(t, "fallback", getEnv("CASCADE_GETENV_DEFINITELY_UNSET", "fallback"))
	})
}

// TestGitIdentity verifies the manifest git config maps to the statewrite
// identity, and that a nil config yields the empty (bot-default) identity.
func TestGitIdentity(t *testing.T) {
	t.Run("nil config yields empty identity", func(t *testing.T) {
		id := gitIdentity(nil)
		assert.Empty(t, id.Name)
		assert.Empty(t, id.Email)
	})

	t.Run("config without git block uses bot defaults", func(t *testing.T) {
		id := gitIdentity(&config.TrunkConfig{})
		assert.Equal(t, "github-actions[bot]", id.Name)
		assert.Equal(t, "github-actions[bot]@users.noreply.github.com", id.Email)
	})

	t.Run("config with git block uses configured values", func(t *testing.T) {
		cfg := &config.TrunkConfig{Git: &config.GitConfig{
			UserName:  "Release Bot",
			UserEmail: "release@example.com",
		}}
		id := gitIdentity(cfg)
		assert.Equal(t, "Release Bot", id.Name)
		assert.Equal(t, "release@example.com", id.Email)
	})
}

// newExternalFinalizer builds a Finalizer whose manifest declares one external
// repo with a single external deploy, plus a source env that recorded a SHA and
// version for that deploy. It is the fixture for the external-deploy helpers.
func newExternalFinalizer(deployName string) *Finalizer {
	return &Finalizer{
		targetEnv: "uat",
		actor:     "deployer",
		cicdFile: &config.CICDFile{
			Config: &config.TrunkConfig{
				External: []config.ExternalRepoConfig{{
					Repo: "org/satellite",
					Deploys: []config.ExternalDeployConfig{{
						Name: deployName,
					}},
				}},
			},
			State: map[string]*config.EnvState{
				"test": {
					External: map[string]*config.ExternalDeployState{
						deployName: {
							Repo:    "org/satellite",
							SHA:     "ext-sha-123",
							Version: "v9.9.9",
						},
					},
				},
			},
		},
		promotionResult: &PromotionResult{
			Promotions: []EnvPromotion{{
				Environment: "uat",
				SourceEnv:   "test",
			}},
		},
	}
}

// TestIsExternalDeploy distinguishes external deploys from unknown names.
func TestIsExternalDeploy(t *testing.T) {
	f := newExternalFinalizer("cdk")
	assert.True(t, f.isExternalDeploy("cdk"), "declared external deploy must be recognized")
	assert.False(t, f.isExternalDeploy("not-external"), "unknown name is not external")
}

// TestGetExternalDeployRepo returns the owning repo for a known deploy and empty
// for an unknown one.
func TestGetExternalDeployRepo(t *testing.T) {
	f := newExternalFinalizer("cdk")
	assert.Equal(t, "org/satellite", f.getExternalDeployRepo("cdk"))
	assert.Equal(t, "", f.getExternalDeployRepo("missing"))
}

// TestGetExternalDeploySHA reads the SHA recorded for the deploy in the source
// environment state and returns empty when there is no promotion context.
func TestGetExternalDeploySHA(t *testing.T) {
	f := newExternalFinalizer("cdk")
	assert.Equal(t, "ext-sha-123", f.getExternalDeploySHA("cdk"))
	assert.Equal(t, "", f.getExternalDeploySHA("missing"), "unknown deploy has no SHA")

	f.promotionResult = nil
	assert.Equal(t, "", f.getExternalDeploySHA("cdk"), "no promotion context yields empty SHA")
}

// TestGetExternalDeployVersion mirrors the SHA lookup for the version field.
func TestGetExternalDeployVersion(t *testing.T) {
	f := newExternalFinalizer("cdk")
	assert.Equal(t, "v9.9.9", f.getExternalDeployVersion("cdk"))
	assert.Equal(t, "", f.getExternalDeployVersion("missing"))

	f.promotionResult = &PromotionResult{}
	assert.Equal(t, "", f.getExternalDeployVersion("cdk"), "empty promotions yields empty version")
}

// TestUpdateExternalDeployState writes the source SHA, version, repo, and actor
// onto the target env's external state.
func TestUpdateExternalDeployState(t *testing.T) {
	f := newExternalFinalizer("cdk")
	f.updateExternalDeployState("cdk", "2026-01-02T03:04:05Z")

	target := f.cicdFile.State["uat"]
	require.NotNil(t, target, "target env state must be created")
	es := target.External["cdk"]
	require.NotNil(t, es, "external deploy state must be recorded")
	assert.Equal(t, "org/satellite", es.Repo)
	assert.Equal(t, "ext-sha-123", es.SHA)
	assert.Equal(t, "v9.9.9", es.Version)
	assert.Equal(t, "2026-01-02T03:04:05Z", es.DeployedAt)
	assert.Equal(t, "deployer", es.DeployedBy)
}

// TestUpdateExternalDeployState_NoSourceSHA is a no-op when the source env has
// not recorded a SHA for the deploy.
func TestUpdateExternalDeployState_NoSourceSHA(t *testing.T) {
	f := newExternalFinalizer("cdk")
	// Drop the source SHA so the helper takes its early-return branch.
	f.cicdFile.State["test"].External["cdk"].SHA = ""

	f.updateExternalDeployState("cdk", "2026-01-02T03:04:05Z")

	assert.Nil(t, f.cicdFile.State["uat"], "no SHA means no target state is created")
}

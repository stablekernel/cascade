package promote

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
)

// TestDetectDeployChanges_BuildLinkedInheritsTriggers pins the documented
// promotion contract (reference/manifest.md, deploy types): a build-linked
// deploy inherits its build's triggers for change detection during promotions.
// Before the fix, preflight gated on d.Triggers directly, so a build-linked
// deploy with no triggers of its own hit the "no triggers = always deploy"
// path and was scheduled on every promotion even when nothing it depends on
// changed.
func TestDetectDeployChanges_BuildLinkedInheritsTriggers(t *testing.T) {
	repoDir, shas := initNegationRepo(t)

	newPreflighter := func(targetSHA string) *Preflighter {
		return &Preflighter{
			baseDir: repoDir,
			cicdFile: &config.CICDFile{
				Config: &config.TrunkConfig{
					Builds: []config.BuildConfig{
						{Name: "api", Triggers: []string{"src/**"}},
					},
					Deploys: []config.DeployConfig{
						{Name: "api-deploy", DependsOn: []string{"api"}},
					},
				},
				State: map[string]*config.EnvState{
					"prod": {SHA: targetSHA},
				},
			},
		}
	}

	// Docs-only change between target state and promotion source: the deploy
	// inherits the build's src/** triggers, so it must NOT be scheduled.
	p := newPreflighter(shas[0])
	local, external := p.detectDeployChanges(shas[1], "prod")
	assert.Empty(t, local, "build-linked deploy scheduled for a change outside its build's triggers")
	assert.Empty(t, external)

	// A src/** change matches the inherited triggers: the deploy is scheduled.
	p = newPreflighter(shas[2])
	local, _ = p.detectDeployChanges(shas[3], "prod")
	assert.Equal(t, []string{"api-deploy"}, local)
}

// TestDetectDeployChanges_OwnTriggersStillApply guards the pre-existing
// trigger-based deploy behavior around the inheritance fix: a deploy with its
// own triggers and no depends_on is still gated on those triggers.
func TestDetectDeployChanges_OwnTriggersStillApply(t *testing.T) {
	repoDir, shas := initNegationRepo(t)

	newPreflighter := func(targetSHA string) *Preflighter {
		return &Preflighter{
			baseDir: repoDir,
			cicdFile: &config.CICDFile{
				Config: &config.TrunkConfig{
					Deploys: []config.DeployConfig{
						{Name: "docs-site", Triggers: []string{"docs/**"}},
					},
				},
				State: map[string]*config.EnvState{
					"prod": {SHA: targetSHA},
				},
			},
		}
	}

	p := newPreflighter(shas[0])
	local, _ := p.detectDeployChanges(shas[1], "prod")
	assert.Equal(t, []string{"docs-site"}, local)

	p = newPreflighter(shas[2])
	local, _ = p.detectDeployChanges(shas[3], "prod")
	assert.Empty(t, local)
}

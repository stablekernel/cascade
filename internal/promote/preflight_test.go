package promote

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stretchr/testify/require"
)

func TestContainsBreakingCommit(t *testing.T) {
	// Conventional commits with `!` after type/scope are breaking, as are
	// any commits whose body contains BREAKING CHANGE: footer.
	tests := []struct {
		name    string
		commits []git.Commit
		want    bool
	}{
		{
			name:    "empty",
			commits: nil,
			want:    false,
		},
		{
			name: "no breaking commits",
			commits: []git.Commit{
				{Subject: "feat: add login flow"},
				{Subject: "fix: handle empty email"},
				{Subject: "docs: update README"},
			},
			want: false,
		},
		{
			name: "feat! is breaking",
			commits: []git.Commit{
				{Subject: "feat!: drop legacy auth API"},
			},
			want: true,
		},
		{
			name: "scope with bang is breaking",
			commits: []git.Commit{
				{Subject: "fix(api)!: rename endpoint"},
			},
			want: true,
		},
		{
			name: "BREAKING CHANGE footer is breaking",
			commits: []git.Commit{
				{Subject: "feat: rework session", Body: "BREAKING CHANGE: cookies replaced with JWT"},
			},
			want: true,
		},
		{
			name: "single breaking among many is breaking",
			commits: []git.Commit{
				{Subject: "fix: typo"},
				{Subject: "feat!: rewrite ingest"},
				{Subject: "chore: bump deps"},
			},
			want: true,
		},
		{
			name: "non-conventional commit doesn't crash",
			commits: []git.Commit{
				{Subject: "wip"},
				{Subject: "merge branch 'main' into feat/x"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsBreakingCommit(tt.commits)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPreflight_DefaultPromotion(t *testing.T) {
	// Setup test config with dev having state
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys: []config.DeployConfig{
				{Name: "infra", Triggers: []string{"infra/**"}},
				{Name: "app", Triggers: []string{"src/**"}},
			},
		},
		State: map[string]*config.EnvState{
			"dev": {SHA: "abc123", Version: "v1.0.0-1"},
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeDefault,
		BaseDir: "",
	})
	result, err := pf.Run()

	require.NoError(t, err)
	require.True(t, result.CanProceed)
	require.Equal(t, "dev", result.SourceEnv)
	require.Equal(t, "test", result.TargetEnv)
	require.Equal(t, "abc123", result.SourceSHA)
	require.Equal(t, ModeDefault, result.Mode)
}

func TestPreflight_CascadePromotion(t *testing.T) {
	// Setup test config with dev and test having different state
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys: []config.DeployConfig{
				{Name: "infra", Triggers: []string{"infra/**"}},
				{Name: "app", Triggers: []string{"src/**"}},
			},
		},
		State: map[string]*config.EnvState{
			"dev":  {SHA: "abc123", Version: "v1.0.0-1"},
			"test": {SHA: "def456", Version: "v0.9.0-1"},
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeCascade,
		Target:  "dev-to-uat",
		BaseDir: "",
	})
	result, err := pf.Run()

	require.NoError(t, err)
	require.True(t, result.CanProceed)
	require.True(t, result.IsCascade)
	require.Equal(t, "dev", result.SourceEnv)
	require.Equal(t, ModeCascade, result.Mode)
	require.Equal(t, "dev-to-uat", result.Target)
}

func TestPreflight_AllEnvironmentsInSync(t *testing.T) {
	// Setup test config where ALL environments including prod are in sync
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys: []config.DeployConfig{
				{Name: "infra", Triggers: []string{"infra/**"}},
			},
		},
		State: map[string]*config.EnvState{
			"dev":  {SHA: "abc123", Version: "v1.0.0-1"},
			"test": {SHA: "abc123", Version: "v1.0.0-1"},
			"uat":  {SHA: "abc123", Version: "v1.0.0-1"},
			"prod": {SHA: "abc123", Version: "v1.0.0"},
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeDefault,
		BaseDir: "",
	})
	result, err := pf.Run()

	// Should fail because all environments including prod are in sync
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "all environments are up to date")
}

func TestPreflight_ExtractsPromotionFields(t *testing.T) {
	// Setup test config for testing field extraction
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys: []config.DeployConfig{
				{Name: "infra", Triggers: []string{"infra/**"}},
				{Name: "app", Triggers: []string{"src/**"}},
			},
		},
		State: map[string]*config.EnvState{
			"dev": {SHA: "abc123", Version: "v1.0.0-1"},
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeDefault,
		BaseDir: "",
	})
	result, err := pf.Run()

	require.NoError(t, err)
	require.Equal(t, "dev", result.SourceEnv)
	require.Equal(t, "test", result.TargetEnv)
	require.Equal(t, "abc123", result.SourceSHA)
	require.Equal(t, "v1.0.0-1", result.SourceVersion)
	require.Contains(t, result.EnvsToUpdate, "test")
	require.False(t, result.IsPrereleaseEnv)
	require.False(t, result.IsFinalEnv)
}

// TestPreflight_SourceImageDigest_FirstSortedNonEmpty asserts that the env-level
// SourceImageDigest is taken from the source env's builds, selecting the first
// build (by sorted build name) that has a non-empty ArtifactID.
func TestPreflight_SourceImageDigest_FirstSortedNonEmpty(t *testing.T) {
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys: []config.DeployConfig{
				{Name: "app", Triggers: []string{"src/**"}},
			},
		},
		State: map[string]*config.EnvState{
			"dev": {
				SHA:     "abc123",
				Version: "v1.0.0-1",
				Builds: map[string]*config.BuildState{
					// "zeta" sorts after "alpha"; "alpha" has an empty
					// ArtifactID, so the first sorted NON-empty wins ("beta").
					"zeta":  {ArtifactID: "sha256:zzz"},
					"alpha": {ArtifactID: ""},
					"beta":  {ArtifactID: "sha256:bbb"},
				},
			},
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeDefault,
		BaseDir: "",
	})
	result, err := pf.Run()

	require.NoError(t, err)
	require.Equal(t, "sha256:bbb", result.SourceImageDigest,
		"SourceImageDigest must be the first sorted build with a non-empty artifact_id")
}

// TestPreflight_SourceImageDigest_EmptyWhenNoArtifact asserts SourceImageDigest
// stays empty (so the deploy input is omitted gracefully) when no source build
// has an artifact_id, preserving non-breaking behavior for deploys without a
// content digest.
func TestPreflight_SourceImageDigest_EmptyWhenNoArtifact(t *testing.T) {
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys: []config.DeployConfig{
				{Name: "app", Triggers: []string{"src/**"}},
			},
		},
		State: map[string]*config.EnvState{
			"dev": {
				SHA:     "abc123",
				Version: "v1.0.0-1",
				Builds: map[string]*config.BuildState{
					"app": {ArtifactID: ""},
				},
			},
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeDefault,
		BaseDir: "",
	})
	result, err := pf.Run()

	require.NoError(t, err)
	require.Empty(t, result.SourceImageDigest,
		"SourceImageDigest must be empty when no source build has an artifact_id")
}

func TestPreflight_PrereleaseFinalEnv(t *testing.T) {
	// Test prerelease and final env detection
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys:      []config.DeployConfig{{Name: "app"}},
		},
		State: map[string]*config.EnvState{
			"test": {SHA: "abc123", Version: "v1.0.0-rc.0"},
		},
	}

	// Test cascade promotion to uat (prerelease env - second from top)
	pf := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeCascade,
		Target:  "test-to-uat",
		BaseDir: "",
	})
	result, err := pf.Run()

	require.NoError(t, err)
	require.True(t, result.IsPrereleaseEnv)
	require.False(t, result.IsFinalEnv)

	// Test cascade promotion to prod (final env - top)
	cfg.State["uat"] = &config.EnvState{SHA: "abc123", Version: "v1.0.0-rc.0"}
	pf2 := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeCascade,
		Target:  "uat-to-prod",
		BaseDir: "",
	})
	result2, err2 := pf2.Run()

	require.NoError(t, err2)
	require.False(t, result2.IsPrereleaseEnv)
	require.True(t, result2.IsFinalEnv)
}

func TestPreflight_DeployChecks(t *testing.T) {
	// Test that deploy checks filter deploys to run
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat"),
			Deploys: []config.DeployConfig{
				{Name: "infra", Triggers: []string{"infra/**"}},
				{Name: "app", Triggers: []string{"src/**"}},
				{Name: "web", Triggers: []string{"web/**"}},
			},
		},
		State: map[string]*config.EnvState{
			"dev": {SHA: "abc123", Version: "v1.0.0-1"},
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeDefault,
		BaseDir: "",
	})
	// Explicitly disable some deploys
	pf.SetDeployCheck("infra", false)
	pf.SetDeployCheck("app", true)
	// web is not set, should default to included

	result, err := pf.Run()

	require.NoError(t, err)
	// infra should be excluded even if it has changes
	require.NotContains(t, result.DeploysToRun, "infra")
}

func TestPreflight_ForceFlag(t *testing.T) {
	// Test that force flag is passed through
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys:      []config.DeployConfig{{Name: "app"}},
		},
		State: map[string]*config.EnvState{
			"dev": {SHA: "abc123", Version: "v1.0.0-1"},
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:  cfg,
		Mode:    ModeDefault,
		Force:   true,
		BaseDir: "",
	})
	result, err := pf.Run()

	require.NoError(t, err)
	require.True(t, result.Force)
	require.Equal(t, ModeDefault, result.Mode)
}

func TestPreflight_RollbackSHA(t *testing.T) {
	// Test that rollback SHA is populated from target env's current state
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys:      []config.DeployConfig{{Name: "app"}},
		},
		State: map[string]*config.EnvState{
			"dev":  {SHA: "newsha123", Version: "v1.1.0-rc.0"},
			"test": {SHA: "oldsha456", Version: "v1.0.0-rc.0"},
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:            cfg,
		Mode:              ModeDefault,
		BaseDir:           "",
		RollbackOnFailure: true,
	})
	result, err := pf.Run()

	require.NoError(t, err)
	// RollbackSHA should be the target env's (test) current SHA
	require.Equal(t, "oldsha456", result.RollbackSHA)
	require.True(t, result.RollbackOnFailure)
}

func TestPreflight_RollbackSHAEmpty_WhenNoTargetState(t *testing.T) {
	// Test that rollback SHA is empty when target env has no state (first deployment)
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat", "prod"),
			Deploys:      []config.DeployConfig{{Name: "app"}},
		},
		State: map[string]*config.EnvState{
			"dev": {SHA: "abc123", Version: "v1.0.0-rc.0"},
			// test has no state - first deployment
		},
	}

	pf := NewPreflighter(PreflighterOptions{
		Config:            cfg,
		Mode:              ModeDefault,
		BaseDir:           "",
		RollbackOnFailure: true,
	})
	result, err := pf.Run()

	require.NoError(t, err)
	// RollbackSHA should be empty since target (test) has no prior state
	require.Empty(t, result.RollbackSHA)
	// ChangelogBaseSHA should also be empty for first deployment
	require.Empty(t, result.ChangelogBaseSHA)
}

func TestPreflight_DeploysFilter(t *testing.T) {
	// Test that deploys filter restricts which deploys are included
	cfg := &config.CICDFile{
		Config: &config.TrunkConfig{
			Environments: config.EnvNames("dev", "test", "uat"),
			Deploys: []config.DeployConfig{
				{Name: "infra", Triggers: []string{}}, // No triggers = always deploy
				{Name: "app", Triggers: []string{}},
				{Name: "web", Triggers: []string{}},
			},
		},
		State: map[string]*config.EnvState{
			"dev": {SHA: "abc123", Version: "v1.0.0-1"},
		},
	}

	// Filter to only include infra and app
	pf := NewPreflighter(PreflighterOptions{
		Config:        cfg,
		Mode:          ModeDefault,
		BaseDir:       "",
		DeploysFilter: []string{"infra", "app"},
	})
	result, err := pf.Run()

	require.NoError(t, err)
	// Only infra and app should be in deploys to run (web is filtered out)
	require.Contains(t, result.DeploysToRun, "infra")
	require.Contains(t, result.DeploysToRun, "app")
	require.NotContains(t, result.DeploysToRun, "web")
}

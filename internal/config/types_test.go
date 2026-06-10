package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetPromotionModes(t *testing.T) {
	cfg := &TrunkConfig{Environments: []string{"dev", "test", "prod"}}
	modes := cfg.GetPromotionModes()

	assert.Len(t, modes, 2)
	assert.Equal(t, ModeDefault, modes[0])
	assert.Equal(t, ModeCascade, modes[1])
}

func TestGetCascadeTargets(t *testing.T) {
	// GetCascadeTargets returns all direct promotion options for manual cascade mode
	// For [dev, test, uat, prod] (4 envs):
	// - Direct env-to-env: dev-to-test, dev-to-uat, dev-to-prod, test-to-uat, test-to-prod, uat-to-prod = 6

	cfg := &TrunkConfig{Environments: []string{"dev", "test", "uat", "prod"}}
	options := cfg.GetCascadeTargets()

	// 6 direct promotions
	assert.Len(t, options, 6)

	// All should be direct promotions
	for _, opt := range options {
		assert.NotEmpty(t, opt.FromEnv)
		assert.NotEmpty(t, opt.ToEnv)
	}
}

func TestGetAllDirectPromotionOptions(t *testing.T) {
	// For 4 envs [dev, test, uat, prod], generates 6 options:
	// - dev-to-test, dev-to-uat, dev-to-prod = 3
	// - test-to-uat, test-to-prod = 2
	// - uat-to-prod = 1
	// Total: 6
	//
	// Release states are determined by POSITION:
	// - uat (second-from-top) = prerelease env
	// - prod (top) = release env

	cfg := &TrunkConfig{Environments: []string{"dev", "test", "uat", "prod"}}
	options := cfg.GetAllDirectPromotionOptions()

	assert.Len(t, options, 6)

	// Verify all expected options exist
	expectedNames := []string{
		"dev-to-test", "dev-to-uat", "dev-to-prod",
		"test-to-uat", "test-to-prod",
		"uat-to-prod",
	}

	foundNames := make(map[string]bool)
	for _, opt := range options {
		foundNames[opt.Name] = true
	}

	for _, name := range expectedNames {
		assert.True(t, foundNames[name], "should have %s", name)
	}

	// Verify prerelease options (target = uat, second-from-top) have IsPrerelease=true
	for _, opt := range options {
		if opt.ToEnv == "uat" { // uat is prerelease env
			assert.True(t, opt.IsPrerelease, "%s should have IsPrerelease=true", opt.Name)
		}
	}

	// Verify prod options have IsRelease=true and IncludesProd=true
	for _, opt := range options {
		if opt.ToEnv == "prod" {
			assert.True(t, opt.IsRelease, "%s should have IsRelease=true", opt.Name)
			assert.True(t, opt.IncludesProd, "%s should have IncludesProd=true", opt.Name)
		}
	}
}

func TestGetAllDirectPromotionOptions_TwoEnvs(t *testing.T) {
	// For 2 envs [dev, prod], generates only:
	// - dev-to-prod = 1
	// Total: 1
	//
	// With 2 envs, prod is both prerelease AND release env (position-based)

	cfg := &TrunkConfig{Environments: []string{"dev", "prod"}}
	options := cfg.GetAllDirectPromotionOptions()

	assert.Len(t, options, 1)
	assert.Equal(t, "dev-to-prod", options[0].Name)
	assert.Equal(t, "dev", options[0].FromEnv)
	assert.Equal(t, "prod", options[0].ToEnv)
	assert.True(t, options[0].IsRelease)
	assert.True(t, options[0].IncludesProd)
}

func TestGetAllDirectPromotionOptions_SingleEnv(t *testing.T) {
	// For 1 env, returns nil (no promotions possible)
	cfg := &TrunkConfig{Environments: []string{"prod"}}
	options := cfg.GetAllDirectPromotionOptions()

	assert.Nil(t, options)
}

func TestIsSingleEnvironment(t *testing.T) {
	single := &TrunkConfig{Environments: []string{"prod"}}
	assert.True(t, single.IsSingleEnvironment())

	two := &TrunkConfig{Environments: []string{"dev", "prod"}}
	assert.False(t, two.IsSingleEnvironment())

	four := &TrunkConfig{Environments: []string{"dev", "test", "uat", "prod"}}
	assert.False(t, four.IsSingleEnvironment())

	empty := &TrunkConfig{Environments: []string{}}
	assert.False(t, empty.IsSingleEnvironment())
}

func TestIsFirstEnvironment(t *testing.T) {
	cfg := &TrunkConfig{Environments: []string{"dev", "staging", "prod"}}

	assert.True(t, cfg.IsFirstEnvironment("dev"))
	assert.False(t, cfg.IsFirstEnvironment("staging"))
	assert.False(t, cfg.IsFirstEnvironment("prod"))
	assert.False(t, cfg.IsFirstEnvironment("unknown"))

	// Empty environments
	empty := &TrunkConfig{}
	assert.False(t, empty.IsFirstEnvironment("dev"))
}

func TestIsLastEnvironment(t *testing.T) {
	cfg := &TrunkConfig{Environments: []string{"dev", "staging", "prod"}}

	assert.False(t, cfg.IsLastEnvironment("dev"))
	assert.False(t, cfg.IsLastEnvironment("staging"))
	assert.True(t, cfg.IsLastEnvironment("prod"))
	assert.False(t, cfg.IsLastEnvironment("unknown"))

	// Empty environments
	empty := &TrunkConfig{}
	assert.False(t, empty.IsLastEnvironment("prod"))
}

func TestGetEnvironmentIndex(t *testing.T) {
	cfg := &TrunkConfig{Environments: []string{"dev", "staging", "uat", "prod"}}

	assert.Equal(t, 0, cfg.GetEnvironmentIndex("dev"))
	assert.Equal(t, 1, cfg.GetEnvironmentIndex("staging"))
	assert.Equal(t, 2, cfg.GetEnvironmentIndex("uat"))
	assert.Equal(t, 3, cfg.GetEnvironmentIndex("prod"))
	assert.Equal(t, -1, cfg.GetEnvironmentIndex("unknown"))
}

func TestGetNextEnvironment(t *testing.T) {
	cfg := &TrunkConfig{Environments: []string{"dev", "staging", "uat", "prod"}}

	assert.Equal(t, "staging", cfg.GetNextEnvironment("dev"))
	assert.Equal(t, "uat", cfg.GetNextEnvironment("staging"))
	assert.Equal(t, "prod", cfg.GetNextEnvironment("uat"))
	assert.Equal(t, "", cfg.GetNextEnvironment("prod"))
	assert.Equal(t, "", cfg.GetNextEnvironment("unknown"))
}

func TestGetEnvironmentsInRange(t *testing.T) {
	cfg := &TrunkConfig{Environments: []string{"dev", "staging", "uat", "perf", "prod"}}

	tests := []struct {
		name     string
		start    string
		end      string
		expected []string
	}{
		{
			name:     "dev to uat",
			start:    "dev",
			end:      "uat",
			expected: []string{"dev", "staging", "uat"},
		},
		{
			name:     "staging to perf",
			start:    "staging",
			end:      "perf",
			expected: []string{"staging", "uat", "perf"},
		},
		{
			name:     "single environment",
			start:    "dev",
			end:      "dev",
			expected: []string{"dev"},
		},
		{
			name:     "full range",
			start:    "dev",
			end:      "prod",
			expected: []string{"dev", "staging", "uat", "perf", "prod"},
		},
		{
			name:     "invalid start",
			start:    "unknown",
			end:      "prod",
			expected: nil,
		},
		{
			name:     "invalid end",
			start:    "dev",
			end:      "unknown",
			expected: nil,
		},
		{
			name:     "reverse order",
			start:    "prod",
			end:      "dev",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cfg.GetEnvironmentsInRange(tt.start, tt.end)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetTriggersForDeploy(t *testing.T) {
	cfg := &TrunkConfig{
		Builds: []BuildConfig{
			{Name: "app", Triggers: []string{"src/**", "go.mod"}},
			{Name: "worker", Triggers: []string{"worker/**"}},
		},
		Deploys: []DeployConfig{
			{Name: "app-deploy", DependsOn: []string{"app"}}, // Should get app's triggers
			{Name: "cdk", Triggers: []string{".aws/cdk/**"}}, // Should get own triggers
			{Name: "notify"}, // No triggers (always deploy)
		},
	}

	tests := []struct {
		deployName string
		want       []string
	}{
		{"app-deploy", []string{"src/**", "go.mod"}},
		{"cdk", []string{".aws/cdk/**"}},
		{"notify", nil},
		{"nonexistent", nil},
	}

	for _, tt := range tests {
		t.Run(tt.deployName, func(t *testing.T) {
			got := cfg.GetTriggersForDeploy(tt.deployName)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Note: TestReleaseEnabled, TestHasExternalRelease, TestHasCustomChangelog
// are in parse_test.go

// =============================================================================
// GetTriggersForDeploy Edge Case Tests
// =============================================================================

func TestGetTriggersForDeploy_MultipleBuildsFirstMatch(t *testing.T) {
	// When deploy depends_on references a build, it should get that build's triggers
	cfg := &TrunkConfig{
		Builds: []BuildConfig{
			{Name: "api", Triggers: []string{"api/**"}},
			{Name: "web", Triggers: []string{"web/**"}},
			{Name: "worker", Triggers: []string{"worker/**"}},
		},
		Deploys: []DeployConfig{
			{Name: "web-deploy", DependsOn: []string{"web"}}, // Should get web's triggers
		},
	}

	triggers := cfg.GetTriggersForDeploy("web-deploy")
	assert.Equal(t, []string{"web/**"}, triggers)
}

func TestGetTriggersForDeploy_MultipleDependsOnUsesFirst(t *testing.T) {
	// When deploy has multiple depends_on, it uses the first one's triggers
	cfg := &TrunkConfig{
		Builds: []BuildConfig{
			{Name: "api", Triggers: []string{"api/**"}},
			{Name: "shared", Triggers: []string{"shared/**"}},
		},
		Deploys: []DeployConfig{
			{Name: "full-deploy", DependsOn: []string{"api", "shared"}}, // Should get api's triggers (first)
		},
	}

	triggers := cfg.GetTriggersForDeploy("full-deploy")
	assert.Equal(t, []string{"api/**"}, triggers)
}

func TestGetTriggersForDeploy_DependsOnNonexistentBuild(t *testing.T) {
	// If depends_on references a non-existent build, return nil
	cfg := &TrunkConfig{
		Builds: []BuildConfig{
			{Name: "app", Triggers: []string{"src/**"}},
		},
		Deploys: []DeployConfig{
			{Name: "bad-deploy", DependsOn: []string{"nonexistent"}},
		},
	}

	triggers := cfg.GetTriggersForDeploy("bad-deploy")
	assert.Nil(t, triggers)
}

func TestGetTriggersForDeploy_EmptyDependsOn(t *testing.T) {
	// Empty depends_on with no triggers = unconstrained
	cfg := &TrunkConfig{
		Builds: []BuildConfig{
			{Name: "app", Triggers: []string{"src/**"}},
		},
		Deploys: []DeployConfig{
			{Name: "notify", DependsOn: []string{}}, // Empty array
		},
	}

	triggers := cfg.GetTriggersForDeploy("notify")
	assert.Nil(t, triggers)
}

func TestGetTriggersForDeploy_TriggersPreferredOverDependsOn(t *testing.T) {
	// When deploy has both triggers AND depends_on, depends_on takes precedence
	// (This is the current behavior - build-linked deploys inherit build's triggers)
	cfg := &TrunkConfig{
		Builds: []BuildConfig{
			{Name: "app", Triggers: []string{"src/**"}},
		},
		Deploys: []DeployConfig{
			{Name: "hybrid", DependsOn: []string{"app"}, Triggers: []string{"deploy/**"}},
		},
	}

	triggers := cfg.GetTriggersForDeploy("hybrid")
	// depends_on takes precedence, so it should return build's triggers
	assert.Equal(t, []string{"src/**"}, triggers)
}

func TestGetTriggersForDeploy_EmptyTriggers(t *testing.T) {
	// Empty triggers array with no depends_on = unconstrained
	cfg := &TrunkConfig{
		Deploys: []DeployConfig{
			{Name: "always", Triggers: []string{}}, // Empty array
		},
	}

	triggers := cfg.GetTriggersForDeploy("always")
	assert.Nil(t, triggers)
}

func TestGetTriggersForDeploy_MultipleTriggerPatterns(t *testing.T) {
	// Multiple trigger patterns should all be returned
	cfg := &TrunkConfig{
		Deploys: []DeployConfig{
			{Name: "infra", Triggers: []string{"terraform/**", "*.tf", "modules/**/*.tf", "environments/**"}},
		},
	}

	triggers := cfg.GetTriggersForDeploy("infra")
	assert.Equal(t, []string{"terraform/**", "*.tf", "modules/**/*.tf", "environments/**"}, triggers)
}

func TestGetTriggersForDeploy_BuildWithNoTriggers(t *testing.T) {
	// Build-linked deploy where build has no triggers
	cfg := &TrunkConfig{
		Builds: []BuildConfig{
			{Name: "force-build", Triggers: []string{}}, // Build with no triggers (always runs)
		},
		Deploys: []DeployConfig{
			{Name: "force-deploy", DependsOn: []string{"force-build"}},
		},
	}

	triggers := cfg.GetTriggersForDeploy("force-deploy")
	// Should return empty slice (not nil) since build exists but has no triggers
	assert.Equal(t, []string{}, triggers)
}

// =============================================================================
// Config Getter Method Tests
// =============================================================================

func TestGetCLIVersion(t *testing.T) {
	// Default when not set
	cfg := &TrunkConfig{}
	assert.Equal(t, "latest", cfg.GetCLIVersion())

	// Configured value
	cfg.CLIVersion = "v1.2.3"
	assert.Equal(t, "v1.2.3", cfg.GetCLIVersion())
}

func TestGetTagPrefix(t *testing.T) {
	// Default when not set
	cfg := &TrunkConfig{}
	assert.Equal(t, "v", cfg.GetTagPrefix())

	// Configured value
	cfg.TagPrefix = "release-"
	assert.Equal(t, "release-", cfg.GetTagPrefix())
}

func TestGetReleaseToken(t *testing.T) {
	// Default when not set
	cfg := &TrunkConfig{}
	assert.Equal(t, "${{ secrets.GITHUB_TOKEN }}", cfg.GetReleaseToken())

	// Configured value (full expression)
	cfg.ReleaseToken = "${{ secrets.CUSTOM_RELEASE_TOKEN }}"
	assert.Equal(t, "${{ secrets.CUSTOM_RELEASE_TOKEN }}", cfg.GetReleaseToken())
}

func TestGetStateToken(t *testing.T) {
	// Default when not set
	cfg := &TrunkConfig{}
	assert.Equal(t, "${{ secrets.GITHUB_TOKEN }}", cfg.GetStateToken())

	// Configured value (full expression)
	cfg.StateToken = "${{ secrets.CASCADE_BOT_TOKEN }}"
	assert.Equal(t, "${{ secrets.CASCADE_BOT_TOKEN }}", cfg.GetStateToken())
}

func TestGetGitMode(t *testing.T) {
	// Default when no git config
	cfg := &TrunkConfig{}
	assert.Equal(t, GitModeDefault, cfg.GetGitMode())

	// Default when git config exists but mode not set
	cfg.Git = &GitConfig{}
	assert.Equal(t, GitModeDefault, cfg.GetGitMode())

	// Configured modes
	cfg.Git.Mode = GitModeCustom
	assert.Equal(t, GitModeCustom, cfg.GetGitMode())

	cfg.Git.Mode = GitModeExternal
	assert.Equal(t, GitModeExternal, cfg.GetGitMode())
}

func TestGetGitUserName(t *testing.T) {
	// Default when no git config
	cfg := &TrunkConfig{}
	assert.Equal(t, "github-actions[bot]", cfg.GetGitUserName())

	// Default when git config exists but user_name not set
	cfg.Git = &GitConfig{}
	assert.Equal(t, "github-actions[bot]", cfg.GetGitUserName())

	// Configured value
	cfg.Git.UserName = "Custom Bot"
	assert.Equal(t, "Custom Bot", cfg.GetGitUserName())
}

func TestGetGitUserEmail(t *testing.T) {
	// Default when no git config
	cfg := &TrunkConfig{}
	assert.Equal(t, "github-actions[bot]@users.noreply.github.com", cfg.GetGitUserEmail())

	// Default when git config exists but user_email not set
	cfg.Git = &GitConfig{}
	assert.Equal(t, "github-actions[bot]@users.noreply.github.com", cfg.GetGitUserEmail())

	// Configured value
	cfg.Git.UserEmail = "custom@example.com"
	assert.Equal(t, "custom@example.com", cfg.GetGitUserEmail())
}

func TestHasGPGSigning(t *testing.T) {
	// No git config
	cfg := &TrunkConfig{}
	assert.False(t, cfg.HasGPGSigning())

	// Git config but no GPG
	cfg.Git = &GitConfig{}
	assert.False(t, cfg.HasGPGSigning())

	// Only key ID
	cfg.Git.GPGKeyID = "ABC123"
	assert.False(t, cfg.HasGPGSigning())

	// Only key secret
	cfg.Git.GPGKeyID = ""
	cfg.Git.GPGKeySecret = "GPG_PRIVATE_KEY"
	assert.False(t, cfg.HasGPGSigning())

	// Both set
	cfg.Git.GPGKeyID = "ABC123"
	cfg.Git.GPGKeySecret = "GPG_PRIVATE_KEY"
	assert.True(t, cfg.HasGPGSigning())
}

// Note: TestReleaseEnabled, TestHasExternalRelease, TestChangelogEnabled, TestHasCustomChangelog
// are already defined in parse_test.go

func TestHasReleaseArtifacts(t *testing.T) {
	// No builds
	cfg := &TrunkConfig{}
	assert.False(t, cfg.HasReleaseArtifacts())

	// Builds without artifacts
	cfg.Builds = []BuildConfig{{Name: "app"}}
	assert.False(t, cfg.HasReleaseArtifacts())

	// Build with empty artifacts
	cfg.Builds = []BuildConfig{{Name: "app", Artifacts: []ArtifactConfig{}}}
	assert.False(t, cfg.HasReleaseArtifacts())

	// Build with artifacts
	cfg.Builds = []BuildConfig{{Name: "app", Artifacts: []ArtifactConfig{{Name: "linux", Path: "dist/*.tar.gz"}}}}
	assert.True(t, cfg.HasReleaseArtifacts())
}

func TestGetAllReleaseArtifacts(t *testing.T) {
	cfg := &TrunkConfig{
		Builds: []BuildConfig{
			{
				Name: "cli",
				Artifacts: []ArtifactConfig{
					{Name: "linux-amd64", Path: "dist/linux/*.tar.gz"},
					{Name: "darwin-arm64", Path: "dist/darwin/*.tar.gz"},
				},
			},
			{
				Name: "web",
				Artifacts: []ArtifactConfig{
					{Name: "bundle", Path: "dist/*.js"},
				},
			},
			{
				Name: "docs", // No artifacts
			},
		},
	}

	artifacts := cfg.GetAllReleaseArtifacts()
	assert.Len(t, artifacts, 3)

	// Check that build names are preserved
	assert.Equal(t, "cli", artifacts[0].BuildName)
	assert.Equal(t, "linux-amd64", artifacts[0].Artifact.Name)

	assert.Equal(t, "cli", artifacts[1].BuildName)
	assert.Equal(t, "darwin-arm64", artifacts[1].Artifact.Name)

	assert.Equal(t, "web", artifacts[2].BuildName)
	assert.Equal(t, "bundle", artifacts[2].Artifact.Name)
}

// =============================================================================
// Helper Function Tests
// =============================================================================

func TestJobID(t *testing.T) {
	assert.Equal(t, "build-app", JobID(CallbackTypeBuild, "app"))
	assert.Equal(t, "deploy-infra", JobID(CallbackTypeDeploy, "infra"))
	assert.Equal(t, "validate", JobID(CallbackTypeValidate, "ignored"))
}

func TestOutputKey(t *testing.T) {
	assert.Equal(t, "build_app", OutputKey("build-app"))
	assert.Equal(t, "deploy_my_infra", OutputKey("deploy-my-infra"))
	assert.Equal(t, "validate", OutputKey("validate"))
}

func TestDisplayName(t *testing.T) {
	assert.Equal(t, "Build (app)", DisplayName(CallbackTypeBuild, "app"))
	assert.Equal(t, "Deploy (infra)", DisplayName(CallbackTypeDeploy, "infra"))
	assert.Equal(t, "Validate (validate)", DisplayName(CallbackTypeValidate, "ignored"))
	assert.Equal(t, "unknown", DisplayName("unknown", "unknown"))
}

func TestResolveDependency(t *testing.T) {
	cfg := &TrunkConfig{
		Builds:   []BuildConfig{{Name: "app"}, {Name: "worker"}},
		Deploys:  []DeployConfig{{Name: "infra"}, {Name: "api"}},
		Validate: &ValidateConfig{Workflow: "validate.yaml"},
	}

	tests := []struct {
		depRef   string
		fromType string
		wantID   string
		wantErr  bool
	}{
		// Explicit prefixes
		{"build:app", CallbackTypeDeploy, "build-app", false},
		{"build:worker", CallbackTypeDeploy, "build-worker", false},
		{"deploy:infra", CallbackTypeDeploy, "deploy-infra", false},
		{"validate:anything", CallbackTypeBuild, "validate", false},
		{"build:nonexistent", CallbackTypeDeploy, "", true},
		{"deploy:nonexistent", CallbackTypeDeploy, "", true},
		{"unknown:app", CallbackTypeDeploy, "", true},

		// Implicit - deploys prefer builds
		{"app", CallbackTypeDeploy, "build-app", false},
		{"infra", CallbackTypeDeploy, "deploy-infra", false},

		// Implicit - builds can only depend on builds
		{"worker", CallbackTypeBuild, "build-worker", false},

		// Not found
		{"nonexistent", CallbackTypeDeploy, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.depRef+"_from_"+tt.fromType, func(t *testing.T) {
			gotID, err := cfg.ResolveDependency(tt.depRef, tt.fromType)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantID, gotID)
			}
		})
	}
}

func TestResolveDependency_SameNamePrefersBuilds(t *testing.T) {
	// Same name exists as both build and deploy - deploys prefer builds
	cfg := &TrunkConfig{
		Builds:  []BuildConfig{{Name: "shared"}},
		Deploys: []DeployConfig{{Name: "shared"}},
	}

	// Deploys prefer builds when name exists in both
	result, err := cfg.ResolveDependency("shared", CallbackTypeDeploy)
	assert.NoError(t, err)
	assert.Equal(t, "build-shared", result)
}

func TestResolveDependency_NoValidate(t *testing.T) {
	cfg := &TrunkConfig{}

	_, err := cfg.ResolveDependency("validate:anything", CallbackTypeBuild)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validate not configured")
}

// Tests for multi-repo orchestration (external repos)

func TestIsPrimary(t *testing.T) {
	tests := []struct {
		name     string
		config   *TrunkConfig
		expected bool
	}{
		{
			name:     "no external repos",
			config:   &TrunkConfig{},
			expected: false,
		},
		{
			name: "with external repos",
			config: &TrunkConfig{
				External: []ExternalRepoConfig{
					{Repo: "org/cdk-infra", Deploys: []ExternalDeployConfig{{Name: "cdk"}}},
				},
			},
			expected: true,
		},
		{
			name: "with empty external",
			config: &TrunkConfig{
				External: []ExternalRepoConfig{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.IsPrimary())
		})
	}
}

func TestIsSatellite(t *testing.T) {
	tests := []struct {
		name     string
		config   *TrunkConfig
		expected bool
	}{
		{
			name:     "no notify config",
			config:   &TrunkConfig{},
			expected: false,
		},
		{
			name: "with notify config",
			config: &TrunkConfig{
				Notify: &NotifyConfig{Repo: "org/backend"},
			},
			expected: true,
		},
		{
			name: "with nil notify",
			config: &TrunkConfig{
				Notify: nil,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.IsSatellite())
		})
	}
}

func TestGetExternalDeploy(t *testing.T) {
	cfg := &TrunkConfig{
		External: []ExternalRepoConfig{
			{
				Repo: "org/cdk-infra",
				Ref:  "main",
				Deploys: []ExternalDeployConfig{
					{Name: "cdk", Workflow: ".github/workflows/deploy-cdk.yaml"},
				},
			},
			{
				Repo: "org/k8s-manifests",
				Ref:  "master",
				Deploys: []ExternalDeployConfig{
					{Name: "k8s", Workflow: "org/k8s-manifests/.github/workflows/deploy.yaml@v1"},
				},
			},
		},
	}

	// Test finding existing deploy
	deploy, repo := cfg.GetExternalDeploy("cdk")
	assert.NotNil(t, deploy)
	assert.NotNil(t, repo)
	assert.Equal(t, "cdk", deploy.Name)
	assert.Equal(t, "org/cdk-infra", repo.Repo)

	// Test finding another deploy
	deploy, repo = cfg.GetExternalDeploy("k8s")
	assert.NotNil(t, deploy)
	assert.NotNil(t, repo)
	assert.Equal(t, "k8s", deploy.Name)
	assert.Equal(t, "org/k8s-manifests", repo.Repo)

	// Test non-existent deploy
	deploy, repo = cfg.GetExternalDeploy("nonexistent")
	assert.Nil(t, deploy)
	assert.Nil(t, repo)
}

func TestGetAllExternalDeploys(t *testing.T) {
	cfg := &TrunkConfig{
		External: []ExternalRepoConfig{
			{
				Repo: "org/cdk-infra",
				Deploys: []ExternalDeployConfig{
					{Name: "cdk"},
					{Name: "terraform"},
				},
			},
			{
				Repo: "org/k8s-manifests",
				Deploys: []ExternalDeployConfig{
					{Name: "k8s"},
				},
			},
		},
	}

	deploys := cfg.GetAllExternalDeploys()
	assert.Len(t, deploys, 3)

	// Verify all deploys are present
	names := make(map[string]bool)
	for _, d := range deploys {
		names[d.Deploy.Name] = true
	}
	assert.True(t, names["cdk"])
	assert.True(t, names["terraform"])
	assert.True(t, names["k8s"])
}

func TestGetAllExternalDeployNames(t *testing.T) {
	cfg := &TrunkConfig{
		External: []ExternalRepoConfig{
			{
				Repo: "org/cdk-infra",
				Deploys: []ExternalDeployConfig{
					{Name: "cdk"},
					{Name: "terraform"},
				},
			},
		},
	}

	names := cfg.GetAllExternalDeployNames()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "cdk")
	assert.Contains(t, names, "terraform")
}

func TestNotifyConfig_GetWorkflow(t *testing.T) {
	tests := []struct {
		name     string
		notify   *NotifyConfig
		expected string
	}{
		{
			name:     "default workflow",
			notify:   &NotifyConfig{Repo: "org/backend"},
			expected: "external-update.yaml",
		},
		{
			name:     "custom workflow",
			notify:   &NotifyConfig{Repo: "org/backend", Workflow: "custom-update.yaml"},
			expected: "custom-update.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.notify.GetWorkflow())
		})
	}
}

func TestNotifyConfig_GetToken(t *testing.T) {
	tests := []struct {
		name     string
		notify   *NotifyConfig
		expected string
	}{
		{
			name:     "default token",
			notify:   &NotifyConfig{Repo: "org/backend"},
			expected: "${{ secrets.PRIMARY_REPO_TOKEN }}",
		},
		{
			name:     "custom token",
			notify:   &NotifyConfig{Repo: "org/backend", Token: "${{ secrets.CUSTOM_TOKEN }}"},
			expected: "${{ secrets.CUSTOM_TOKEN }}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.notify.GetToken())
		})
	}
}

func TestExternalRepoConfig_GetRef(t *testing.T) {
	tests := []struct {
		name        string
		config      ExternalRepoConfig
		trunkBranch string
		expected    string
	}{
		{
			name:        "explicit ref",
			config:      ExternalRepoConfig{Repo: "org/repo", Ref: "v1"},
			trunkBranch: "main",
			expected:    "v1",
		},
		{
			name:        "no ref uses trunk branch",
			config:      ExternalRepoConfig{Repo: "org/repo"},
			trunkBranch: "main",
			expected:    "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.config.GetRef(tt.trunkBranch))
		})
	}
}

func TestResolveDependency_External(t *testing.T) {
	cfg := &TrunkConfig{
		External: []ExternalRepoConfig{
			{
				Repo: "org/cdk-infra",
				Deploys: []ExternalDeployConfig{
					{Name: "cdk"},
				},
			},
		},
	}

	// Deploy can depend on external deploys
	result, err := cfg.ResolveDependency("cdk", CallbackTypeDeploy)
	assert.NoError(t, err)
	assert.Equal(t, "external-cdk", result)
}

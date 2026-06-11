package promote

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
)

func TestDefaultPromotion(t *testing.T) {
	tests := []struct {
		name              string
		state             map[string]*config.EnvState
		environments      []string
		wantSuccess       bool
		wantError         string
		wantPromotions    int
		wantReleaseAction string
	}{
		{
			name:         "single step promotion from dev to test",
			environments: []string{"dev", "test", "uat", "prod"},
			state: map[string]*config.EnvState{
				"dev": {SHA: "abc123", Version: "v1.0.0-rc.0"},
			},
			wantSuccess:       true,
			wantPromotions:    1,
			wantReleaseAction: "",
		},
		{
			name:         "sequential promotion through all envs",
			environments: []string{"dev", "test", "uat", "prod"},
			state: map[string]*config.EnvState{
				"dev":  {SHA: "sha3", Version: "v1.3.0-rc.0"},
				"test": {SHA: "sha2", Version: "v1.2.0-rc.0"},
				"uat":  {SHA: "sha1", Version: "v1.1.0-rc.0"},
			},
			wantSuccess:       true,
			wantPromotions:    3,         // test←dev, uat←test, prod←uat
			wantReleaseAction: "publish", // Reaching prod triggers publish
		},
		{
			name:         "all environments in sync - nothing to do",
			environments: []string{"dev", "test", "uat", "prod"},
			state: map[string]*config.EnvState{
				"dev":  {SHA: "abc123", Version: "v1.0.0-rc.0"},
				"test": {SHA: "abc123", Version: "v1.0.0-rc.0"},
				"uat":  {SHA: "abc123", Version: "v1.0.0-rc.0"},
				"prod": {SHA: "abc123", Version: "v1.0.0"},
			},
			wantSuccess:    false,
			wantError:      "all environments are up to date - nothing to promote",
			wantPromotions: 0,
		},
		{
			name:         "empty state - nothing to promote",
			environments: []string{"dev", "test", "uat", "prod"},
			state:        map[string]*config.EnvState{},
			wantSuccess:  false,
			wantError:    "all environments are up to date - nothing to promote",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp config file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "cicd.yaml")

			cicdFile := &config.CICDFile{
				Config: &config.TrunkConfig{
					TrunkBranch:  "master",
					Environments: tt.environments,
				},
				State: tt.state,
			}

			// Wrap in ci: key for proper parsing
			wrapper := map[string]interface{}{
				"ci": cicdFile,
			}
			data, err := yaml.Marshal(wrapper)
			if err != nil {
				t.Fatalf("failed to marshal config: %v", err)
			}
			if err := os.WriteFile(configPath, data, 0644); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}

			promoter, err := NewPromoter(PromoterOptions{
				ConfigPath: configPath,
				DryRun:     true,
				Actor:      "test-actor",
			})
			if err != nil {
				t.Fatalf("failed to create promoter: %v", err)
			}

			result, err := promoter.Promote(ModeDefault, "")
			if err != nil {
				t.Fatalf("Promote returned error: %v", err)
			}

			if result.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v (error: %s)", result.Success, tt.wantSuccess, result.Error)
			}

			if tt.wantError != "" && result.Error != tt.wantError {
				t.Errorf("Error = %q, want %q", result.Error, tt.wantError)
			}

			if result.Success {
				if len(result.Promotions) != tt.wantPromotions {
					t.Errorf("Promotions count = %d, want %d", len(result.Promotions), tt.wantPromotions)
				}

				if result.ReleaseAction != tt.wantReleaseAction {
					t.Errorf("ReleaseAction = %q, want %q", result.ReleaseAction, tt.wantReleaseAction)
				}
			}
		})
	}
}

func TestCascadePromotion(t *testing.T) {
	tests := []struct {
		name              string
		state             map[string]*config.EnvState
		target            string
		wantSuccess       bool
		wantError         string
		wantPromotions    int
		wantEnvs          []string
		wantReleaseAction string
	}{
		{
			name: "cascade dev-to-uat",
			state: map[string]*config.EnvState{
				"dev":  {SHA: "abc123", Version: "v1.0.0-rc.0"},
				"test": {SHA: "old000", Version: "v0.9.0-rc.0"},
				"uat":  {SHA: "old000", Version: "v0.9.0-rc.0"},
			},
			target:            "dev-to-uat",
			wantSuccess:       true,
			wantPromotions:    2,
			wantEnvs:          []string{"test", "uat"},
			wantReleaseAction: "prerelease",
		},
		{
			name: "cascade dev-to-prod",
			state: map[string]*config.EnvState{
				"dev":  {SHA: "abc123", Version: "v1.0.0-rc.0"},
				"test": {SHA: "old000", Version: "v0.9.0-rc.0"},
				"uat":  {SHA: "old000", Version: "v0.9.0-rc.0"},
				"prod": {SHA: "old000", Version: "v0.9.0"},
			},
			target:      "dev-to-prod",
			wantSuccess: true,
			// Promotes test, uat, release (publish marker), then prod.
			// "release" is the implicit virtual env where the publish action
			// lands; it's materialized as its own promotion (NeedsDeploy=false).
			wantPromotions:    4,
			wantEnvs:          []string{"test", "uat", "release", "prod"},
			wantReleaseAction: "publish",
		},
		{
			name: "cascade test-to-uat",
			state: map[string]*config.EnvState{
				"dev":  {SHA: "sha3", Version: "v1.3.0-rc.0"},
				"test": {SHA: "sha2", Version: "v1.2.0-rc.0"},
				"uat":  {SHA: "sha1", Version: "v1.1.0-rc.0"},
			},
			target:            "test-to-uat",
			wantSuccess:       true,
			wantPromotions:    1,
			wantEnvs:          []string{"uat"},
			wantReleaseAction: "prerelease",
		},
		{
			name: "cascade missing target",
			state: map[string]*config.EnvState{
				"dev": {SHA: "abc123", Version: "v1.0.0-rc.0"},
			},
			target:      "",
			wantSuccess: false,
			wantError:   "cascade mode requires a target (e.g., dev-to-prod)",
		},
		{
			name: "cascade invalid format",
			state: map[string]*config.EnvState{
				"dev": {SHA: "abc123", Version: "v1.0.0-rc.0"},
			},
			target:      "dev-uat",
			wantSuccess: false,
			wantError:   "invalid cascade target format: dev-uat (expected 'source-to-target')",
		},
		{
			name: "cascade source has no state",
			state: map[string]*config.EnvState{
				"test": {SHA: "abc123", Version: "v1.0.0-rc.0"},
			},
			target:      "dev-to-uat",
			wantSuccess: false,
			wantError:   "source environment 'dev' has no deployments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp config file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "cicd.yaml")

			cicdFile := &config.CICDFile{
				Config: &config.TrunkConfig{
					TrunkBranch:  "master",
					Environments: []string{"dev", "test", "uat", "prod"},
				},
				State: tt.state,
			}

			// Wrap in ci: key for proper parsing
			wrapper := map[string]interface{}{
				"ci": cicdFile,
			}
			data, err := yaml.Marshal(wrapper)
			if err != nil {
				t.Fatalf("failed to marshal config: %v", err)
			}
			if err := os.WriteFile(configPath, data, 0644); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}

			promoter, err := NewPromoter(PromoterOptions{
				ConfigPath: configPath,
				DryRun:     true,
				Actor:      "test-actor",
			})
			if err != nil {
				t.Fatalf("failed to create promoter: %v", err)
			}

			result, err := promoter.Promote(ModeCascade, tt.target)
			if err != nil {
				t.Fatalf("Promote returned error: %v", err)
			}

			if result.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v (error: %s)", result.Success, tt.wantSuccess, result.Error)
			}

			if tt.wantError != "" && result.Error != tt.wantError {
				t.Errorf("Error = %q, want %q", result.Error, tt.wantError)
			}

			if result.Success {
				if len(result.Promotions) != tt.wantPromotions {
					t.Errorf("Promotions count = %d, want %d", len(result.Promotions), tt.wantPromotions)
				}

				for i, promo := range result.Promotions {
					if i < len(tt.wantEnvs) && promo.Environment != tt.wantEnvs[i] {
						t.Errorf("Promotion[%d].Environment = %q, want %q", i, promo.Environment, tt.wantEnvs[i])
					}
				}

				if result.ReleaseAction != tt.wantReleaseAction {
					t.Errorf("ReleaseAction = %q, want %q", result.ReleaseAction, tt.wantReleaseAction)
				}

				// Verify cascade mode is set
				if !result.IsCascade {
					t.Error("IsCascade should be true for cascade mode")
				}
				if result.Mode != ModeCascade {
					t.Errorf("Mode = %q, want %q", result.Mode, ModeCascade)
				}
			}
		})
	}
}

func TestDefaultPromotion_StripsRCSuffixOnPublishEnv(t *testing.T) {
	// Same root cause as the cascade variant: when sequential default-mode
	// promotion crosses from the prerelease env into the publish env, the
	// EnvPromotion for the publish env must have the RC suffix stripped.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cicd.yaml")

	cicdFile := &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "master",
			Environments: []string{"dev", "test", "uat", "prod"},
		},
		State: map[string]*config.EnvState{
			"dev":  {SHA: "abc123", Version: "v1.0.0-rc.0"},
			"test": {SHA: "abc123", Version: "v1.0.0-rc.0"},
			"uat":  {SHA: "abc123", Version: "v1.0.0-rc.0"},
		},
	}
	wrapper := map[string]interface{}{"ci": cicdFile}
	data, err := yaml.Marshal(wrapper)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	promoter, err := NewPromoter(PromoterOptions{
		ConfigPath: configPath,
		DryRun:     true,
		Actor:      "test-actor",
	})
	if err != nil {
		t.Fatalf("failed to create promoter: %v", err)
	}

	result, err := promoter.Promote(ModeDefault, "")
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.ReleaseAction != "publish" {
		t.Fatalf("ReleaseAction = %q, want %q", result.ReleaseAction, "publish")
	}

	var releasePromo *EnvPromotion
	for i, p := range result.Promotions {
		if p.Environment == "release" {
			releasePromo = &result.Promotions[i]
			break
		}
	}
	if releasePromo == nil {
		t.Fatal("no promotion found for env 'release'")
	}

	if releasePromo.Version != "v1.0.0" {
		t.Errorf("Promotion[release].Version = %q, want %q (RC suffix should be stripped on the publish/release marker)",
			releasePromo.Version, "v1.0.0")
	}
}

func TestCascadePromotion_StripsRCSuffixOnPublishEnv(t *testing.T) {
	// When a cascade ends at the publish env (ReleaseAction == "publish"),
	// the EnvPromotion for that env must have the RC suffix stripped from
	// its Version. Otherwise finalize.go writes "v1.0.0-rc.0" to
	// state[publishEnv].version when it should be "v1.0.0".
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cicd.yaml")

	cicdFile := &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "master",
			Environments: []string{"dev", "test", "uat", "prod"},
		},
		State: map[string]*config.EnvState{
			"dev":  {SHA: "abc123", Version: "v1.0.0-rc.0"},
			"test": {SHA: "abc123", Version: "v1.0.0-rc.0"},
			"uat":  {SHA: "abc123", Version: "v1.0.0-rc.0"},
		},
	}
	wrapper := map[string]interface{}{"ci": cicdFile}
	data, err := yaml.Marshal(wrapper)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	promoter, err := NewPromoter(PromoterOptions{
		ConfigPath: configPath,
		DryRun:     true,
		Actor:      "test-actor",
	})
	if err != nil {
		t.Fatalf("failed to create promoter: %v", err)
	}

	result, err := promoter.Promote(ModeCascade, "uat-to-prod")
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.ReleaseAction != "publish" {
		t.Fatalf("ReleaseAction = %q, want %q", result.ReleaseAction, "publish")
	}

	var releasePromo *EnvPromotion
	for i, p := range result.Promotions {
		if p.Environment == "release" {
			releasePromo = &result.Promotions[i]
			break
		}
	}
	if releasePromo == nil {
		t.Fatal("no promotion found for env 'release'")
	}

	if releasePromo.Version != "v1.0.0" {
		t.Errorf("Promotion[release].Version = %q, want %q (RC suffix should be stripped on the publish/release marker)",
			releasePromo.Version, "v1.0.0")
	}
}

func TestCascadePromotion_AtomicBehavior(t *testing.T) {
	// Test that cascade mode pushes the same source SHA to all intermediate envs
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cicd.yaml")

	cicdFile := &config.CICDFile{
		Config: &config.TrunkConfig{
			TrunkBranch:  "master",
			Environments: []string{"dev", "test", "uat", "prod"},
		},
		State: map[string]*config.EnvState{
			"dev":  {SHA: "new-sha", Version: "v1.0.0-rc.0"},
			"test": {SHA: "old-sha", Version: "v0.9.0-rc.0"},
			"uat":  {SHA: "old-sha", Version: "v0.9.0-rc.0"},
			"prod": {SHA: "old-sha", Version: "v0.9.0"},
		},
	}

	// Wrap in ci: key for proper parsing
	wrapper := map[string]interface{}{
		"ci": cicdFile,
	}
	data, err := yaml.Marshal(wrapper)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	promoter, err := NewPromoter(PromoterOptions{
		ConfigPath: configPath,
		DryRun:     true,
		Actor:      "test-actor",
	})
	if err != nil {
		t.Fatalf("failed to create promoter: %v", err)
	}

	result, err := promoter.Promote(ModeCascade, "dev-to-prod")
	if err != nil {
		t.Fatalf("Promote returned error: %v", err)
	}

	if !result.Success {
		t.Fatalf("Promote failed: %s", result.Error)
	}

	// All promotions should have the same SHA (the source dev SHA)
	for _, promo := range result.Promotions {
		if promo.SHA != "new-sha" {
			t.Errorf("Promotion to %s has SHA %q, want %q (source SHA)", promo.Environment, promo.SHA, "new-sha")
		}
		if promo.SourceEnv != "dev" {
			t.Errorf("Promotion to %s has SourceEnv %q, want %q", promo.Environment, promo.SourceEnv, "dev")
		}
	}
}

func TestStripRCSuffix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"v1.0.0-rc.0", "v1.0.0"},
		{"v1.0.0-rc.5", "v1.0.0"},
		{"v2.3.4-rc.123", "v2.3.4"},
		{"v1.0.0", "v1.0.0"},
		{"v1.0.0-beta", "v1.0.0-beta"},
	}

	for _, tt := range tests {
		result := stripRCSuffix(tt.input)
		if result != tt.expected {
			t.Errorf("stripRCSuffix(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestStripRCSuffix_HotfixVersion(t *testing.T) {
	if got := stripRCSuffix("v1.4.0-rc.2.hotfix.1"); got != "v1.4.0" {
		t.Errorf("stripRCSuffix(%q) = %q, want %q", "v1.4.0-rc.2.hotfix.1", got, "v1.4.0")
	}
}

func TestPromotionResultToJSON(t *testing.T) {
	result := &PromotionResult{
		Success:   true,
		Mode:      ModeCascade,
		Target:    "dev-to-prod",
		IsCascade: true,
		Promotions: []EnvPromotion{
			{Environment: "test", SourceEnv: "dev", SHA: "abc123", Version: "v1.0.0-rc.0", NeedsDeploy: true},
		},
		FinalEnv:      "test",
		ReleaseAction: "prerelease",
		ReleaseData: &ReleaseData{
			SHA:        "abc123",
			RCVersion:  "v1.0.0-rc.0",
			SemVersion: "v1.0.0",
		},
	}

	json := result.ToJSON()
	if json == "" {
		t.Error("ToJSON returned empty string")
	}

	// Basic validation that key fields are present
	if !contains(json, `"success": true`) {
		t.Error("JSON missing success field")
	}
	if !contains(json, `"mode": "cascade"`) {
		t.Error("JSON missing mode field")
	}
	if !contains(json, `"is_cascade": true`) {
		t.Error("JSON missing is_cascade field")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestIndexOf(t *testing.T) {
	slice := []string{"dev", "test", "uat", "prod"}

	tests := []struct {
		item     string
		expected int
	}{
		{"dev", 0},
		{"test", 1},
		{"uat", 2},
		{"prod", 3},
		{"staging", -1},
		{"", -1},
	}

	for _, tt := range tests {
		result := indexOf(slice, tt.item)
		if result != tt.expected {
			t.Errorf("indexOf(slice, %q) = %d, want %d", tt.item, result, tt.expected)
		}
	}

	// Empty slice
	if indexOf(nil, "test") != -1 {
		t.Error("indexOf(nil, ...) should return -1")
	}
	if indexOf([]string{}, "test") != -1 {
		t.Error("indexOf([], ...) should return -1")
	}
}

func TestTruncateSHA(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"abcdef1234567890", "abcdef1"},
		{"abc", "abc"},
		{"", ""},
		{"1234567", "1234567"},
		{"12345678", "1234567"},
		{"abcdefg", "abcdefg"}, // exactly 7 chars
	}

	for _, tt := range tests {
		result := truncateSHA(tt.input)
		if result != tt.expected {
			t.Errorf("truncateSHA(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

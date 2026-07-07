package promote

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/taggrammar"
)

// PromotionMode defines how the promotion operates
type PromotionMode string

const (
	// ModeDefault promotes each environment to its immediate next (sequential)
	// Supports force flag to continue on failure
	ModeDefault PromotionMode = "default"

	// ModeCascade promotes source through all intermediate envs to target (atomic)
	// Bails entirely on any failure - no partial state updates
	ModeCascade PromotionMode = "cascade"
)

// EnvPromotion describes what should happen for a single environment
type EnvPromotion struct {
	Environment string `json:"environment"`
	SourceEnv   string `json:"source_env"`
	SHA         string `json:"sha"`
	Version     string `json:"version"`
	NeedsDeploy bool   `json:"needs_deploy"`
}

// PromotionResult is the output of a promotion operation
type PromotionResult struct {
	Success        bool           `json:"success"`
	Mode           PromotionMode  `json:"mode"`
	Target         string         `json:"target,omitempty"`       // Target for cascade mode (e.g., "dev-to-prod")
	Force          bool           `json:"force,omitempty"`        // Force flag for default mode
	IsCascade      bool           `json:"is_cascade"`             // True for cascade mode
	Promotions     []EnvPromotion `json:"promotions"`             // Successful promotions
	FailedEnvs     []string       `json:"failed_envs,omitempty"`  // Envs that failed (default+force)
	SkippedEnvs    []string       `json:"skipped_envs,omitempty"` // Envs skipped due to no-op
	FinalEnv       string         `json:"final_env,omitempty"`
	ReleaseAction  string         `json:"release_action,omitempty"` // "prerelease", "publish", or ""
	ReleaseData    *ReleaseData   `json:"release_data,omitempty"`
	ProdDeployment *EnvPromotion  `json:"prod_deployment,omitempty"`
	Error          string         `json:"error,omitempty"`
}

// ReleaseData contains info for release management
type ReleaseData struct {
	SHA        string `json:"sha"`
	RCVersion  string `json:"rc_version"`  // e.g., v1.0.0-rc.0
	SemVersion string `json:"sem_version"` // e.g., v1.0.0
}

// Promoter handles promotion logic
type Promoter struct {
	configPath string
	cicdFile   *config.CICDFile
	dryRun     bool
	actor      string
	force      bool // For default mode: continue on failure
	ancestor   AncestorFunc
}

// PromoterOptions configures the Promoter
type PromoterOptions struct {
	ConfigPath string
	DryRun     bool
	Actor      string
	Force      bool // For default mode: continue on failure
}

// NewPromoter creates a new Promoter. Optional behavior (such as the
// git-ancestry checker used by the divergence guards) is supplied through
// functional options so the required inputs stay positional.
func NewPromoter(opts PromoterOptions, options ...Option) (*Promoter, error) {
	cicdFile, err := config.ParseManifestFile(opts.ConfigPath, config.DefaultManifestKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	actor := opts.Actor
	if actor == "" {
		actor = "github-actions[bot]"
	}

	gc := newGuardConfig(options...)

	return &Promoter{
		configPath: opts.ConfigPath,
		cicdFile:   cicdFile,
		dryRun:     opts.DryRun,
		actor:      actor,
		force:      opts.Force,
		ancestor:   gc.ancestor,
	}, nil
}

// Promote executes a promotion and returns the result
// mode: "default" for sequential single-step, "cascade" for atomic multi-step
// target: for cascade mode, the target (e.g., "dev-to-prod")
func (p *Promoter) Promote(mode PromotionMode, target string) (*PromotionResult, error) {
	switch mode {
	case ModeDefault:
		return p.defaultPromotion()
	case ModeCascade:
		if target == "" {
			return &PromotionResult{
				Success: false,
				Mode:    mode,
				Error:   "cascade mode requires a target (e.g., dev-to-prod)",
			}, nil
		}
		return p.cascadePromotion(target)
	default:
		return &PromotionResult{
			Success: false,
			Mode:    mode,
			Error:   fmt.Sprintf("unknown promotion mode: %s", mode),
		}, nil
	}
}

// defaultPromotion implements sequential single-step promotion.
// Each environment promotes to its immediate next only, based on current state snapshot.
// If force=true, continues to next env even if current step fails.
// State is committed for successful steps (partial commit on force+failure).
func (p *Promoter) defaultPromotion() (*PromotionResult, error) {
	envs := p.cicdFile.Config.Environments

	// Handle no-environment mode (library/CLI projects)
	// Use implicit environments: prerelease → release
	if len(envs) == 0 {
		return p.noEnvironmentPromotion()
	}

	// Snapshot current state; include "release" since it's a virtual env
	// in the chain but isn't necessarily in the env list.
	preState := make(map[string]*config.EnvState)
	for _, env := range envs {
		if state, ok := p.cicdFile.State[env]; ok {
			stateCopy := *state
			preState[env] = &stateCopy
		}
	}
	if state, ok := p.cicdFile.State["release"]; ok {
		stateCopy := *state
		preState["release"] = &stateCopy
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)
	var promotions []EnvPromotion
	var skippedEnvs []string

	// Determine prerelease and prod positions. The implicit chain is
	// [envs..., "release", prodEnv]. "release" is a virtual env between
	// the prerelease env and prod where the publish action lands. Each
	// `mode: default` invocation advances state through the chain by one
	// logical step, stopping at the publish boundary.
	prodEnv := envs[len(envs)-1]
	releaseIdx := indexOf(envs, "release")
	var prereleaseEnv string
	if releaseIdx > 0 {
		prereleaseEnv = envs[releaseIdx-1]
	} else if len(envs) >= 2 {
		prereleaseEnv = envs[len(envs)-2]
	}

	result := &PromotionResult{
		Success:   true,
		Mode:      ModeDefault,
		Force:     p.force,
		IsCascade: false,
	}

	// Sequential walk through env pairs. Stops at the publish boundary
	// (sourceEnv == prereleaseEnv && targetEnv == prodEnv): that crossing
	// produces a "release" promotion that advances state[release] only;
	// state[prodEnv] is advanced by a subsequent default-mode invocation.
	for i := 0; i < len(envs)-1; i++ {
		sourceEnv := envs[i]
		targetEnv := envs[i+1]

		// Skip "release" as a target if it's literally in the env list. The
		// release marker is advanced via the publish path, not as a normal env.
		if targetEnv == "release" {
			continue
		}

		sourceState := preState[sourceEnv]

		// Skip if source has no state
		if sourceState == nil || sourceState.SHA == "" {
			skippedEnvs = append(skippedEnvs, targetEnv)
			continue
		}

		// Guard: never promote FROM a diverged env. Its SHA is not on trunk and
		// must not propagate upward. Only fires when divergence fields are set,
		// so non-diverged manifests never reach this branch.
		if sourceState.IsDiverged() {
			result.Success = false
			result.Error = divergedSourceError(sourceEnv, sourceState).Error()
			return result, nil
		}

		targetState := preState[targetEnv]

		// Skip if already in sync (no-op)
		if targetState != nil && targetState.SHA == sourceState.SHA {
			skippedEnvs = append(skippedEnvs, targetEnv)
			continue
		}

		// Publish boundary: crossing from prereleaseEnv to prodEnv. Instead of
		// advancing state[prodEnv] here, advance the virtual "release" env
		// (state marker + publish action). State[prodEnv] is left untouched;
		// a subsequent default-mode invocation handles release→prod.
		if sourceEnv == prereleaseEnv && targetEnv == prodEnv {
			// Skip if state[release] is already at sourceState.SHA. The
			// publish was completed by a prior invocation. Without this, a
			// repeat default-mode call would re-emit a no-op publish promo
			// instead of falling through to the release→prod fallback below.
			if rs := preState["release"]; rs != nil && rs.SHA == sourceState.SHA {
				break
			}
			semVersion := p.stripPreRelease(sourceState.Version)
			promo := EnvPromotion{
				Environment: "release",
				SourceEnv:   sourceEnv,
				SHA:         sourceState.SHA,
				Version:     semVersion,
				NeedsDeploy: false, // release marker advance, no deploy
			}
			promotions = append(promotions, promo)

			if !p.dryRun {
				if p.cicdFile.State == nil {
					p.cicdFile.State = make(map[string]*config.EnvState)
				}
				if p.cicdFile.State["release"] == nil {
					p.cicdFile.State["release"] = &config.EnvState{}
				}
				p.cicdFile.State["release"].SHA = sourceState.SHA
				p.cicdFile.State["release"].Version = semVersion
				p.cicdFile.State["release"].CommittedAt = timestamp
				p.cicdFile.State["release"].CommittedBy = p.actor
			}

			result.ReleaseAction = "publish"
			result.ReleaseData = &ReleaseData{
				SHA:        sourceState.SHA,
				RCVersion:  sourceState.Version,
				SemVersion: semVersion,
			}
			break
		}

		// Normal env-pair advance
		promo := EnvPromotion{
			Environment: targetEnv,
			SourceEnv:   sourceEnv,
			SHA:         sourceState.SHA,
			Version:     sourceState.Version,
			NeedsDeploy: true,
		}
		promotions = append(promotions, promo)

		if !p.dryRun {
			if p.cicdFile.State == nil {
				p.cicdFile.State = make(map[string]*config.EnvState)
			}
			if p.cicdFile.State[targetEnv] == nil {
				p.cicdFile.State[targetEnv] = &config.EnvState{}
			}
			p.cicdFile.State[targetEnv].SHA = sourceState.SHA
			p.cicdFile.State[targetEnv].Version = sourceState.Version
			p.cicdFile.State[targetEnv].CommittedAt = timestamp
			p.cicdFile.State[targetEnv].CommittedBy = p.actor
		}

		if targetEnv == prereleaseEnv {
			result.ReleaseAction = "prerelease"
			result.ReleaseData = &ReleaseData{
				SHA:        sourceState.SHA,
				RCVersion:  sourceState.Version,
				SemVersion: p.stripPreRelease(sourceState.Version),
			}
		}
	}

	// Handle "release" state marker when "release" is literally in the env list.
	// This path matches the implicit-chain semantics for env lists that
	// explicitly include the marker.
	if releaseIdx > 0 && prereleaseEnv != "" {
		sourceState := preState[prereleaseEnv]
		if sourceState != nil && sourceState.SHA != "" {
			// Publish-path assertion: the SHA reaching the release marker (and
			// thus publish) must come from a non-diverged source. Inert unless
			// the prerelease env carries divergence fields.
			if sourceState.IsDiverged() {
				result.Success = false
				result.Error = divergedSourceError(prereleaseEnv, sourceState).Error()
				return result, nil
			}
			releaseState := preState["release"]
			if releaseState == nil || releaseState.SHA != sourceState.SHA {
				if !p.dryRun {
					if p.cicdFile.State == nil {
						p.cicdFile.State = make(map[string]*config.EnvState)
					}
					if p.cicdFile.State["release"] == nil {
						p.cicdFile.State["release"] = &config.EnvState{}
					}
					p.cicdFile.State["release"].SHA = sourceState.SHA
					p.cicdFile.State["release"].Version = sourceState.Version
					p.cicdFile.State["release"].CommittedAt = timestamp
					p.cicdFile.State["release"].CommittedBy = p.actor
				}

				if result.ReleaseAction == "" {
					result.ReleaseAction = "publish"
					result.ReleaseData = &ReleaseData{
						SHA:        sourceState.SHA,
						RCVersion:  sourceState.Version,
						SemVersion: p.stripPreRelease(sourceState.Version),
					}
				}
			}
		}
	}

	// Fallback: if the env walk produced nothing AND state[release] is ahead
	// of state[prodEnv], deploy release → prod. This is the second step of
	// the two-step "publish then deploy" flow. Runs only when no upstream
	// advance is pending. Order matters: if uat is ahead of release, we
	// publish first; if release is ahead of prod and there's no upstream
	// work, we deploy. (When "release" is in the env list literally, the
	// env walk handles release→prod naturally so this fallback is skipped.)
	if len(promotions) == 0 && releaseIdx == -1 {
		releaseState := preState["release"]
		prodState := preState[prodEnv]
		if releaseState != nil && releaseState.SHA != "" {
			needsRelDeploy := prodState == nil || prodState.SHA != releaseState.SHA
			if needsRelDeploy {
				promo := EnvPromotion{
					Environment: prodEnv,
					SourceEnv:   "release",
					SHA:         releaseState.SHA,
					Version:     releaseState.Version,
					NeedsDeploy: true,
				}
				promotions = append(promotions, promo)
				if !p.dryRun {
					if p.cicdFile.State == nil {
						p.cicdFile.State = make(map[string]*config.EnvState)
					}
					if p.cicdFile.State[prodEnv] == nil {
						p.cicdFile.State[prodEnv] = &config.EnvState{}
					}
					p.cicdFile.State[prodEnv].SHA = releaseState.SHA
					p.cicdFile.State[prodEnv].Version = releaseState.Version
					p.cicdFile.State[prodEnv].CommittedAt = timestamp
					p.cicdFile.State[prodEnv].CommittedBy = p.actor
				}
			}
		}
	}

	result.SkippedEnvs = skippedEnvs
	result.Promotions = promotions

	if len(promotions) > 0 {
		result.FinalEnv = promotions[len(promotions)-1].Environment
	}

	// Check if anything happened
	if len(promotions) == 0 {
		result.Success = false
		result.Error = "all environments are up to date - nothing to promote"
		return result, nil
	}

	// Save state if not dry run
	if !p.dryRun {
		if err := p.saveConfig(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return result, nil
}

// noEnvironmentPromotion handles promotion for library/CLI projects with no environments.
// Uses implicit environments: prerelease → release
// This publishes a draft prerelease as a final release.
func (p *Promoter) noEnvironmentPromotion() (*PromotionResult, error) {
	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Get prerelease state
	sourceState := p.cicdFile.State["prerelease"]
	if sourceState == nil || sourceState.SHA == "" {
		return &PromotionResult{
			Success: false,
			Mode:    ModeDefault,
			Error:   "no prerelease state found - nothing to promote",
		}, nil
	}

	// Publish-path assertion: a diverged prerelease holds a non-trunk SHA and
	// must never be published. Inert unless divergence fields are set.
	if sourceState.IsDiverged() {
		return &PromotionResult{
			Success: false,
			Mode:    ModeDefault,
			Error:   divergedSourceError("prerelease", sourceState).Error(),
		}, nil
	}

	// Check if already released (release state matches prerelease)
	releaseState := p.cicdFile.State["release"]
	if releaseState != nil && releaseState.SHA == sourceState.SHA {
		return &PromotionResult{
			Success: false,
			Mode:    ModeDefault,
			Error:   "already released - nothing to promote",
		}, nil
	}

	// Create promotion from prerelease to release
	promo := EnvPromotion{
		Environment: "release",
		SourceEnv:   "prerelease",
		SHA:         sourceState.SHA,
		Version:     p.stripPreRelease(sourceState.Version), // Use semver for release
		NeedsDeploy: false,                              // No deployment for library/CLI projects
	}

	result := &PromotionResult{
		Success:       true,
		Mode:          ModeDefault,
		Force:         p.force,
		IsCascade:     false,
		Promotions:    []EnvPromotion{promo},
		FinalEnv:      "release",
		ReleaseAction: "publish",
		ReleaseData: &ReleaseData{
			SHA:        sourceState.SHA,
			RCVersion:  sourceState.Version,
			SemVersion: p.stripPreRelease(sourceState.Version),
		},
	}

	// Update state (if not dry run)
	if !p.dryRun {
		if p.cicdFile.State == nil {
			p.cicdFile.State = make(map[string]*config.EnvState)
		}
		if p.cicdFile.State["release"] == nil {
			p.cicdFile.State["release"] = &config.EnvState{}
		}
		p.cicdFile.State["release"].SHA = sourceState.SHA
		p.cicdFile.State["release"].Version = p.stripPreRelease(sourceState.Version)
		p.cicdFile.State["release"].CommittedAt = timestamp
		p.cicdFile.State["release"].CommittedBy = p.actor

		// Clear prerelease state after publishing
		delete(p.cicdFile.State, "prerelease")

		if err := p.saveConfig(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return result, nil
}

// cascadePromotion implements atomic cascade promotion from source to target.
// It pushes the source state through all intermediate environments to the target.
// This is atomic - if any step would fail, the entire operation fails with no state changes.
func (p *Promoter) cascadePromotion(target string) (*PromotionResult, error) {
	// Parse target format: "source-to-target" (e.g., "dev-to-prod")
	parts := strings.Split(target, "-to-")
	if len(parts) != 2 {
		return &PromotionResult{
			Success:   false,
			Mode:      ModeCascade,
			Target:    target,
			IsCascade: true,
			Error:     fmt.Sprintf("invalid cascade target format: %s (expected 'source-to-target')", target),
		}, nil
	}

	sourceEnv := parts[0]
	targetEnv := parts[1]

	// Get source state
	sourceState := p.cicdFile.State[sourceEnv]
	if sourceState == nil || sourceState.SHA == "" {
		return &PromotionResult{
			Success:   false,
			Mode:      ModeCascade,
			Target:    target,
			IsCascade: true,
			Error:     fmt.Sprintf("source environment '%s' has no deployments", sourceEnv),
		}, nil
	}

	// Guard: never cascade FROM a diverged env. Inert unless divergence fields
	// are set on the source state.
	if sourceState.IsDiverged() {
		return &PromotionResult{
			Success:   false,
			Mode:      ModeCascade,
			Target:    target,
			IsCascade: true,
			Error:     divergedSourceError(sourceEnv, sourceState).Error(),
		}, nil
	}

	envs := p.cicdFile.Config.Environments
	sourceIdx := indexOf(envs, sourceEnv)
	targetIdx := indexOf(envs, targetEnv)

	// Validate path
	if sourceIdx == -1 {
		return &PromotionResult{
			Success:   false,
			Mode:      ModeCascade,
			Target:    target,
			IsCascade: true,
			Error:     fmt.Sprintf("source environment '%s' not found in config", sourceEnv),
		}, nil
	}

	if targetIdx == -1 {
		return &PromotionResult{
			Success:   false,
			Mode:      ModeCascade,
			Target:    target,
			IsCascade: true,
			Error:     fmt.Sprintf("target environment '%s' not found in config", targetEnv),
		}, nil
	}

	if sourceIdx >= targetIdx {
		return &PromotionResult{
			Success:   false,
			Mode:      ModeCascade,
			Target:    target,
			IsCascade: true,
			Error:     fmt.Sprintf("invalid promotion path: source '%s' must be before target '%s'", sourceEnv, targetEnv),
		}, nil
	}

	// Find prerelease/publish positions. The implicit chain is
	// [envs..., "release", prodEnv]. Cascade walks this chain atomically;
	// when it crosses into the prod env, a "release" promotion is inserted
	// before the prod promotion (release marker advance + then deploy).
	releaseIdx := indexOf(envs, "release")
	var prereleaseEnv, publishEnv string
	if releaseIdx > 0 {
		prereleaseEnv = envs[releaseIdx-1]
		publishEnv = "release"
	} else if len(envs) >= 2 {
		prereleaseEnv = envs[len(envs)-2]
		publishEnv = envs[len(envs)-1]
	}
	prodEnv := envs[len(envs)-1]
	semVersion := p.stripPreRelease(sourceState.Version)

	// Build promotions for envs[sourceIdx+1..targetIdx]. Materialize "release"
	// as its own promotion either when it's in the env list or when crossing
	// into prodEnv from prereleaseEnv (publish boundary). The release
	// promotion advances state[release] only; no deploy.
	var promotions []EnvPromotion
	for i := sourceIdx + 1; i <= targetIdx; i++ {
		env := envs[i]

		if env == "release" {
			promotions = append(promotions, EnvPromotion{
				Environment: "release",
				SourceEnv:   sourceEnv,
				SHA:         sourceState.SHA,
				Version:     semVersion,
				NeedsDeploy: false,
			})
			continue
		}

		// Crossing into prodEnv when "release" isn't already in the env list:
		// insert the publish step (release marker) before prod.
		if env == prodEnv && releaseIdx == -1 && envs[i-1] == prereleaseEnv {
			promotions = append(promotions, EnvPromotion{
				Environment: "release",
				SourceEnv:   sourceEnv,
				SHA:         sourceState.SHA,
				Version:     semVersion,
				NeedsDeploy: false,
			})
		}

		// Version: stripped on the publish env (where the final semver lands),
		// otherwise the source's RC version is carried forward.
		v := sourceState.Version
		if env == publishEnv || env == prodEnv {
			v = semVersion
		}
		promotions = append(promotions, EnvPromotion{
			Environment: env,
			SourceEnv:   sourceEnv,
			SHA:         sourceState.SHA,
			Version:     v,
			NeedsDeploy: true,
		})
	}

	if len(promotions) == 0 {
		return &PromotionResult{
			Success:   false,
			Mode:      ModeCascade,
			Target:    target,
			IsCascade: true,
			Error:     "no environments to promote",
		}, nil
	}

	// Determine release action based on final target
	result := &PromotionResult{
		Success:    true,
		Mode:       ModeCascade,
		Target:     target,
		IsCascade:  true,
		Promotions: promotions,
		FinalEnv:   promotions[len(promotions)-1].Environment,
	}

	// Set release action based on what environments are being promoted
	finalEnv := promotions[len(promotions)-1].Environment
	if finalEnv == publishEnv || finalEnv == prodEnv || (parts[1] == "release" && releaseIdx > 0) {
		result.ReleaseAction = "publish"
		result.ReleaseData = &ReleaseData{
			SHA:        sourceState.SHA,
			RCVersion:  sourceState.Version,
			SemVersion: semVersion,
		}
	} else if finalEnv == prereleaseEnv {
		result.ReleaseAction = "prerelease"
		result.ReleaseData = &ReleaseData{
			SHA:        sourceState.SHA,
			RCVersion:  sourceState.Version,
			SemVersion: semVersion,
		}
	}

	// Apply state changes atomically (all or nothing)
	if !p.dryRun {
		timestamp := time.Now().UTC().Format(time.RFC3339)
		if p.cicdFile.State == nil {
			p.cicdFile.State = make(map[string]*config.EnvState)
		}

		for _, promo := range promotions {
			if p.cicdFile.State[promo.Environment] == nil {
				p.cicdFile.State[promo.Environment] = &config.EnvState{}
			}
			p.cicdFile.State[promo.Environment].SHA = promo.SHA
			p.cicdFile.State[promo.Environment].Version = promo.Version
			p.cicdFile.State[promo.Environment].CommittedAt = timestamp
			p.cicdFile.State[promo.Environment].CommittedBy = p.actor
		}

		if err := p.saveConfig(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return result, nil
}

// CommitAndPush commits the state change and pushes to remote. It delegates to
// the shared git rebase-retry helper so promote and hotfix finalize write
// manifest state identically.
func (p *Promoter) CommitAndPush(message string) error {
	return git.CommitAndPushWithRetry(p.configPath, message)
}

// saveConfig writes the in-memory manifest back to disk. It rewrites only the
// mutable state subtree of the on-disk manifest, so any configuration this binary
// does not model is preserved rather than dropped on the round-trip. saveConfig
// only ever mutates State (including the prerelease delete), so handing the parsed
// LatestRelease through unchanged keeps it intact. This path is dry-run in
// production promote (preflight) but is reached non-dry-run by cascade simulate.
func (p *Promoter) saveConfig() error {
	// Get the manifest key from config (defaults to "ci")
	key := config.DefaultManifestKey
	if p.cicdFile.Config != nil && p.cicdFile.Config.ManifestKey != "" {
		key = p.cicdFile.Config.ManifestKey
	}

	current, err := os.ReadFile(p.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}
	data, err := config.WriteManifestState(current, key, p.cicdFile.State, p.cicdFile.LatestRelease)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	return os.WriteFile(p.configPath, data, 0644)
}

// ToJSON returns the result as JSON
func (r *PromotionResult) ToJSON() string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}

// Helper functions

// resolveTagGrammar folds a manifest into its tag grammar, falling back to the
// historical default when the manifest or its config is absent so callers never
// dereference a nil config.
func resolveTagGrammar(f *config.CICDFile) taggrammar.Spec {
	if f == nil || f.Config == nil {
		return taggrammar.Default()
	}
	return f.Config.ResolveTagGrammar()
}

// stripPreRelease reduces a version to its base under this promoter's configured
// tag grammar, so a custom pre-release token (for example "beta") is stripped
// just as the historical "rc" is, instead of publishing the pre-release shape.
func (p *Promoter) stripPreRelease(version string) string {
	return resolveTagGrammar(p.cicdFile).StripPreRelease(version)
}

// stripRCSuffix strips the default-grammar pre-release segment. It is retained
// for callers with no manifest in scope; grammar-aware promotion uses
// stripPreRelease so a custom token is honored.
func stripRCSuffix(version string) string {
	return taggrammar.Default().StripPreRelease(version)
}

func indexOf(slice []string, item string) int {
	for i, s := range slice {
		if s == item {
			return i
		}
	}
	return -1
}

func truncateSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

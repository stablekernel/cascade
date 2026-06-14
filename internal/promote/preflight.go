package promote

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stablekernel/cascade/internal/changelog"
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
	"github.com/stablekernel/cascade/internal/version"
)

// PreflightResult contains all outputs needed by the workflow
type PreflightResult struct {
	// Mode and configuration
	Mode              PromotionMode `json:"mode"`
	Target            string        `json:"target,omitempty"`              // For cascade mode
	Force             bool          `json:"force,omitempty"`               // For default mode
	RollbackOnFailure bool          `json:"rollback_on_failure,omitempty"` // Revert successful deploys if any fails

	// Source/target info
	SourceEnv     string `json:"source_env"`
	TargetEnv     string `json:"target_env"`
	SourceSHA     string `json:"source_sha"`
	SourceVersion string `json:"source_version"`
	// SourceImageDigest is an immutable artifact identifier (e.g. a Docker image
	// content digest) for the source env, threaded alongside the mutable
	// SourceVersion so operators can pin deploys to immutable content. It is
	// sourced from the source env's build state: cascade treats artifact_id as an
	// opaque immutable id, so this is only a real content digest if the operator's
	// build populates artifact_id with one. When the source env has multiple
	// builds, the first build (by sorted build name) with a non-empty artifact_id
	// is used; when none has one, this stays empty and the deploy input is omitted.
	SourceImageDigest string `json:"source_image_digest,omitempty"`

	// Rollback SHA (target env's current SHA before promotion - what we revert to on failure)
	RollbackSHA string `json:"rollback_sha,omitempty"`

	// Changelog comparison point (SHA of first target env before promotion)
	ChangelogBaseSHA string `json:"changelog_base_sha"`

	// Environments to update
	EnvsToUpdate []string `json:"envs_to_update"`
	SkippedEnvs  []string `json:"skipped_envs"`

	// Deploy decisions
	DeploysToRun         []string `json:"deploys_to_run"`          // Local deploys to run
	ExternalDeploysToRun []string `json:"external_deploys_to_run"` // External deploys to run

	// Release info
	IsPrereleaseEnv bool   `json:"is_prerelease_env"`
	IsFinalEnv      bool   `json:"is_final_env"`
	IsCascade       bool   `json:"is_cascade"`
	ReleaseAction   string `json:"release_action"`

	// Full-pass prod deployment (separate from cascade)
	HasProdDeployment bool   `json:"has_prod_deployment"`
	ProdSHA           string `json:"prod_sha"`
	ProdVersion       string `json:"prod_version"`

	// Breaking changes
	HasBreaking       bool   `json:"has_breaking"`
	CanProceed        bool   `json:"can_proceed"`
	BreakingBlockedAt string `json:"breaking_blocked_at,omitempty"` // The transition that was blocked

	// Warnings carries non-fatal advisories surfaced during preflight, for
	// example a forced override of the diverged-env patch-containment guard.
	Warnings []string `json:"warnings,omitempty"`

	// Full promotion result for downstream use
	PromotionResult *PromotionResult `json:"promotion_result"`
}

// Preflighter handles preflight validation and planning
type Preflighter struct {
	cicdFile          *config.CICDFile
	mode              PromotionMode
	target            string // For cascade mode (e.g., "dev-to-prod")
	force             bool   // For default mode
	baseDir           string
	deployChecks      map[string]bool // deploy name -> include in run
	deploysFilter     []string        // Specific deploys to include (empty = all)
	rollbackOnFailure bool            // Revert successful deploys if any fails
	allowDowngrade    bool            // Permit promoting an older version onto an env
	ancestor          AncestorFunc    // git-ancestry checker for divergence guards
}

// PreflighterOptions configures the Preflighter
type PreflighterOptions struct {
	Config            *config.CICDFile
	Mode              PromotionMode
	Target            string // For cascade mode (e.g., "dev-to-prod")
	Force             bool   // For default mode
	BaseDir           string
	DeploysFilter     []string // Specific deploys to include (empty = all)
	RollbackOnFailure bool     // Revert successful deploys if any fails
	AllowDowngrade    bool     // Permit promoting an older version onto an env
}

// NewPreflighter creates a new Preflighter instance. Optional behavior (such as
// the git-ancestry checker used by the divergence guards) is supplied through
// functional options so the required inputs stay positional.
func NewPreflighter(opts PreflighterOptions, options ...Option) *Preflighter {
	baseDir := opts.BaseDir
	if baseDir == "" {
		baseDir = "."
	}
	gc := newGuardConfig(options...)
	return &Preflighter{
		cicdFile:          opts.Config,
		mode:              opts.Mode,
		target:            opts.Target,
		force:             opts.Force,
		baseDir:           baseDir,
		deployChecks:      make(map[string]bool),
		deploysFilter:     opts.DeploysFilter,
		rollbackOnFailure: opts.RollbackOnFailure,
		allowDowngrade:    opts.AllowDowngrade,
		ancestor:          gc.ancestor,
	}
}

// SetDeployCheck sets whether a deploy should be included
func (p *Preflighter) SetDeployCheck(name string, include bool) {
	p.deployChecks[name] = include
}

// Run executes the preflight checks and returns the result
func (p *Preflighter) Run() (*PreflightResult, error) {
	// 1. Calculate promotion plan using Promoter in dry-run mode. The promoter
	// enforces the "never promote FROM a diverged env" and publish-path guards;
	// it shares this preflighter's ancestry checker so behavior is consistent.
	promoter := &Promoter{
		cicdFile: p.cicdFile,
		dryRun:   true, // Always dry run for preflight
		actor:    "preflight",
		force:    p.force,
		ancestor: p.ancestor,
	}

	promoResult, err := promoter.Promote(p.mode, p.target)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate promotion: %w", err)
	}
	if !promoResult.Success {
		return nil, fmt.Errorf("promotion validation failed: %s", promoResult.Error)
	}

	result := &PreflightResult{
		Mode:              p.mode,
		Target:            p.target,
		Force:             p.force,
		RollbackOnFailure: p.rollbackOnFailure,
		PromotionResult:   promoResult,
		IsCascade:         promoResult.IsCascade,
		ReleaseAction:     promoResult.ReleaseAction,
	}

	// 2. Extract source/target from promotion result
	if len(promoResult.Promotions) > 0 {
		first := promoResult.Promotions[0]
		result.SourceEnv = first.SourceEnv
		result.TargetEnv = promoResult.FinalEnv

		// Get source state
		if state := p.cicdFile.State[first.SourceEnv]; state != nil {
			result.SourceSHA = state.SHA
			result.SourceVersion = state.Version
			result.SourceImageDigest = firstNonEmptyArtifactID(state.Builds)
		}

		// Build envs to update list
		for _, promo := range promoResult.Promotions {
			result.EnvsToUpdate = append(result.EnvsToUpdate, promo.Environment)
		}

		// Calculate changelog base SHA (what the first target env currently has)
		// This ensures changelog shows what's new compared to the immediate next env
		firstTargetEnv := first.Environment
		if state := p.cicdFile.State[firstTargetEnv]; state != nil && state.SHA != "" {
			result.ChangelogBaseSHA = state.SHA
		}
		// If empty, workflow will fall back to initial commit (first deployment ever)

		// Get rollback SHA (target env's current SHA before promotion)
		// This is what we revert to if rollback_on_failure is enabled and a deploy fails
		if state := p.cicdFile.State[firstTargetEnv]; state != nil && state.SHA != "" {
			result.RollbackSHA = state.SHA
		}
	}

	// 3. Check for prod deployment
	if promoResult.ProdDeployment != nil {
		result.HasProdDeployment = true
		result.ProdSHA = promoResult.ProdDeployment.SHA
		result.ProdVersion = promoResult.ProdDeployment.Version
	}

	// 4. Determine prerelease/final env and release marker positions
	envs := p.cicdFile.Config.Environments
	var prereleaseEnv, prodEnv string
	hasReleaseMarker := false

	if len(envs) == 0 {
		// No-environment mode (library/CLI projects)
		// Implicit environments: prerelease → release
		prereleaseEnv = "prerelease"
		prodEnv = "release"
		hasReleaseMarker = true // Treat as having release marker for breaking change checks
	} else {
		releaseIdx := indexOf(envs, "release")
		hasReleaseMarker = releaseIdx > 0
		if releaseIdx > 0 {
			prereleaseEnv = envs[releaseIdx-1]
			if releaseIdx < len(envs)-1 {
				prodEnv = envs[len(envs)-1]
			}
		} else if len(envs) >= 2 {
			prereleaseEnv = envs[len(envs)-2]
			prodEnv = envs[len(envs)-1]
		}
	}

	result.IsPrereleaseEnv = result.TargetEnv == prereleaseEnv
	// is_final_env fires when the publish action lands. Under the implicit
	// chain [envs..., "release", prodEnv] this is either:
	//   - target_env == "release" (default-mode advance to publish marker), or
	//   - target_env == prodEnv with ReleaseAction == "publish" (cascade
	//     atomically through the publish boundary into prod).
	result.IsFinalEnv = result.TargetEnv == "release" ||
		(result.TargetEnv == prodEnv && result.ReleaseAction == "publish")

	// 5. Detect which deploys need to run. The release marker advance is a
	// metadata step (no deploy jobs), so emit no deploys when target is "release".
	if result.TargetEnv == "release" {
		result.DeploysToRun = nil
		result.ExternalDeploysToRun = nil
	} else {
		result.DeploysToRun, result.ExternalDeploysToRun = p.detectDeployChanges(result.SourceSHA, result.TargetEnv)
	}

	// 6. Check for breaking changes
	// Breaking changes block at: pre-release → release AND release → prod
	result.HasBreaking, result.BreakingBlockedAt = p.checkBreakingChangesForMode(
		promoResult.Promotions, prereleaseEnv, prodEnv, hasReleaseMarker,
	)

	result.CanProceed = !result.HasBreaking

	// 7. Promotion INTO a diverged env: the incoming SHA must contain every
	// recorded patch, otherwise the promotion would silently regress a fix that
	// was deployed into that env. The force flag overrides with a loud warning.
	// Inert for non-diverged targets (no manifest without divergence fields can
	// enter this loop).
	if err := p.checkPatchContainment(promoResult.Promotions, result); err != nil {
		return nil, err
	}

	// 8. Monotonicity / downgrade gate: promoting an older version onto an env
	// regresses it. Block unless --allow-downgrade is set; prod always requires
	// the flag even if a lower env permitted the same downgrade.
	if err := p.checkDowngrade(promoResult.Promotions, result, prodEnv); err != nil {
		return nil, err
	}

	return result, nil
}

// checkDowngrade enforces version monotonicity across the planned promotions.
// For each target env, the incoming SourceVersion must be greater than or equal
// to the env's current version under semver precedence. A strictly older
// incoming version is a downgrade: it is blocked unless allowDowngrade is set,
// in which case a loud warning is recorded and the promotion proceeds. The prod
// env always requires the flag explicitly, even when a lower env in the same
// cascade already permitted the downgrade.
//
// The gate fails open on non-semver inputs: when either the incoming or the
// current version cannot be parsed, it records a warning naming both versions
// and the env, then continues. Envs with an empty incoming or current version
// are skipped silently (first promotion, or version-less manifests).
func (p *Preflighter) checkDowngrade(promotions []EnvPromotion, result *PreflightResult, prodEnv string) error {
	for _, promo := range promotions {
		state := p.cicdFile.State[promo.Environment]
		var currentStr string
		if state != nil {
			currentStr = state.Version
		}

		// The incoming version is the one that actually LANDS on this env, which
		// is promo.Version. In a cascade across the publish boundary these differ
		// from the source env's raw version: the prerelease envs receive the RC
		// (e.g. v1.0.0-rc.0) while the release/prod envs receive the stripped
		// semver (v1.0.0). Comparing every env against result.SourceVersion (the
		// single source env's raw version) wrongly flags the rc-to-release
		// finalization as a downgrade, because an rc sorts below its release under
		// semver precedence. Use promo.Version per env; fall back to SourceVersion
		// only when a promotion carries no version (version-less manifests).
		incomingStr := promo.Version
		if incomingStr == "" {
			incomingStr = result.SourceVersion
		}

		// Skip if either version is empty (first promotion / version-less).
		if incomingStr == "" || currentStr == "" {
			continue
		}

		incoming, errIn := version.Parse(incomingStr)
		current, errCur := version.Parse(currentStr)
		if errIn != nil || errCur != nil {
			// FAIL-OPEN: non-semver version -> warn and continue.
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"downgrade check skipped on env %q: could not compare current version %s to incoming %s; ensure both are semver to enforce monotonicity",
				promo.Environment, currentStr, incomingStr,
			))
			continue
		}

		if incoming.Compare(current) >= 0 {
			// Newer or equal: a forward promotion, allow.
			continue
		}

		// DOWNGRADE: incoming is strictly older than current.
		isProd := promo.Environment == prodEnv
		if p.allowDowngrade {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"downgrade on %s from %s to %s permitted via --allow-downgrade",
				promo.Environment, currentStr, incomingStr,
			))
			continue
		}

		if isProd {
			return fmt.Errorf(
				"downgrade blocked on prod env %q: current version %s is newer than incoming %s; prod downgrades always require --allow-downgrade",
				promo.Environment, currentStr, incomingStr,
			)
		}
		return fmt.Errorf(
			"downgrade blocked on env %q: current version %s is newer than incoming %s; pass --allow-downgrade to permit",
			promo.Environment, currentStr, incomingStr,
		)
	}
	return nil
}

// checkPatchContainment enforces the "promote INTO a diverged env requires the
// incoming SHA to contain every recorded patch" rule across the planned
// promotions. For each target env that is diverged, every entry of its Patches
// must be an ancestor of the incoming SHA. A missing patch fails the preflight
// unless force is set, in which case it records a loud warning and proceeds.
//
// The check is fully additive: environments without divergence fields are
// skipped, so a manifest that never diverges never invokes the ancestry checker.
func (p *Preflighter) checkPatchContainment(promotions []EnvPromotion, result *PreflightResult) error {
	for _, promo := range promotions {
		target := p.cicdFile.State[promo.Environment]
		if !target.IsDiverged() {
			continue
		}

		incoming := promo.SHA
		for _, patch := range target.Patches {
			contained, err := p.ancestor(patch, incoming)
			if err != nil {
				return fmt.Errorf(
					"failed to verify patch %s is contained in %s for diverged environment %q: %w",
					patch, incoming, promo.Environment, err,
				)
			}
			if contained {
				continue
			}

			if !p.force {
				return fmt.Errorf(
					"cannot promote into diverged environment %q: incoming SHA %s does not contain patch %s. "+
						"Promoting would regress a fix deployed in %q. "+
						"Promote a trunk SHA at or after the patch, or pass --force to override",
					promo.Environment, incoming, patch, promo.Environment,
				)
			}

			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"FORCE OVERRIDE: promoting into diverged environment %q with incoming SHA %s that does NOT contain patch %s; "+
					"this regresses a fix deployed in %q",
				promo.Environment, incoming, patch, promo.Environment,
			))
		}
	}
	return nil
}

// detectDeployChanges determines which deploys need to run based on changes and deploy checks
// Returns (localDeploys, externalDeploys)
func (p *Preflighter) detectDeployChanges(sourceSHA, targetEnv string) ([]string, []string) {
	var localDeploys []string
	var externalDeploys []string

	// Get source environment name for external deploy comparison
	var sourceEnv string
	if len(p.cicdFile.Config.Environments) > 0 {
		sourceEnv = p.cicdFile.Config.Environments[0] // Default to first env
		// Find the env before targetEnv for source
		for i, env := range p.cicdFile.Config.Environments {
			if env == targetEnv && i > 0 {
				sourceEnv = p.cicdFile.Config.Environments[i-1]
				break
			}
		}
	}

	// Process local deploys
	for _, d := range p.cicdFile.Config.Deploys {
		// Check if deploy is in the filter list (if filter is specified)
		if len(p.deploysFilter) > 0 && !stringSliceContains(p.deploysFilter, d.Name) {
			continue
		}

		// Check if deploy is included (from checkbox - legacy support)
		if include, ok := p.deployChecks[d.Name]; ok && !include {
			continue
		}

		// Get target deploy state. Per-deploy state is the most precise
		// comparison point ("when was this specific deploy last run for this
		// env"). When unavailable (typical for first-promotion scenarios
		// where the env has been promoted to some SHA but no deploy has run
		// yet), fall back to the env-level SHA. That captures the semantic
		// "if the env is at SHA X, treat the deploy as having seen X" so
		// trigger filters still apply rather than unconditionally including.
		var targetSHA string
		if state := p.cicdFile.State[targetEnv]; state != nil {
			if ds := state.Deploys[d.Name]; ds != nil {
				targetSHA = ds.SHA
			}
			if targetSHA == "" {
				targetSHA = state.SHA
			}
		}

		// If neither per-deploy nor env-level state exists, include
		// unconditionally: this is a never-deployed env.
		if targetSHA == "" {
			localDeploys = append(localDeploys, d.Name)
			continue
		}

		// Check for changes in triggers
		if p.hasChanges(targetSHA, sourceSHA, d.Triggers) {
			localDeploys = append(localDeploys, d.Name)
		}
	}

	// Process external deploys (from primary repo coordinating satellites)
	for _, ext := range p.cicdFile.Config.External {
		for _, d := range ext.Deploys {
			// Check if deploy is in the filter list (if filter is specified)
			if len(p.deploysFilter) > 0 && !stringSliceContains(p.deploysFilter, d.Name) {
				continue
			}

			// Check if deploy is included (from checkbox - legacy support)
			if include, ok := p.deployChecks[d.Name]; ok && !include {
				continue
			}

			// Get source and target external deploy states
			var sourceSHAExt, targetSHAExt string
			if state := p.cicdFile.State[sourceEnv]; state != nil {
				if es := state.External[d.Name]; es != nil {
					sourceSHAExt = es.SHA
				}
			}
			if state := p.cicdFile.State[targetEnv]; state != nil {
				if es := state.External[d.Name]; es != nil {
					targetSHAExt = es.SHA
				}
			}

			// Include if never deployed to target or source is different from target
			if targetSHAExt == "" || sourceSHAExt != targetSHAExt {
				externalDeploys = append(externalDeploys, d.Name)
			}
		}
	}

	return localDeploys, externalDeploys
}

// hasChanges checks if there are changes between two SHAs matching any of the triggers
func (p *Preflighter) hasChanges(baseSHA, headSHA string, triggers []string) bool {
	if len(triggers) == 0 {
		return true // No triggers = always deploy
	}

	// Use git diff to get changed files
	cmd := exec.Command("git", "diff", "--name-only", baseSHA, headSHA)
	cmd.Dir = p.baseDir
	out, err := cmd.Output()
	if err != nil {
		return true // On error, assume changes
	}

	changedFiles := strings.Split(string(out), "\n")

	// Check if any changed file matches any trigger pattern
	for _, file := range changedFiles {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		for _, pattern := range triggers {
			if matchGlob(pattern, file) {
				return true
			}
		}
	}

	return false
}

// matchGlob matches a file path against a glob pattern
// Supports: * (single segment), ** (any segments)
func matchGlob(pattern, path string) bool {
	// Handle ** patterns
	if strings.Contains(pattern, "**") {
		return matchDoublestar(pattern, path)
	}

	// For simple patterns, use filepath.Match
	matched, _ := filepath.Match(pattern, path)
	return matched
}

// matchDoublestar handles ** glob patterns
func matchDoublestar(pattern, path string) bool {
	// Split pattern and path into segments
	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")

	return matchParts(patternParts, pathParts)
}

// matchParts recursively matches pattern parts against path parts
func matchParts(pattern, path []string) bool {
	pi, ppi := 0, 0

	for pi < len(pattern) && ppi < len(path) {
		if pattern[pi] == "**" {
			// ** matches zero or more path segments
			if pi == len(pattern)-1 {
				// ** at end matches everything
				return true
			}

			// Try matching remaining pattern at each position
			for i := ppi; i <= len(path); i++ {
				if matchParts(pattern[pi+1:], path[i:]) {
					return true
				}
			}
			return false
		}

		// Match single segment
		matched, _ := filepath.Match(pattern[pi], path[ppi])
		if !matched {
			return false
		}

		pi++
		ppi++
	}

	// Check if pattern is exhausted
	for pi < len(pattern) {
		if pattern[pi] != "**" {
			return false
		}
		pi++
	}

	return ppi == len(path)
}

// checkBreakingChangesForMode checks if there are breaking changes based on mode and transitions.
// Breaking changes block at: pre-release → release AND release → prod
// For cascade mode: blocks entire operation if target includes release or prod
// Returns (hasBreaking, blockedTransition)
func (p *Preflighter) checkBreakingChangesForMode(
	promotions []EnvPromotion,
	prereleaseEnv, prodEnv string,
	hasReleaseMarker bool,
) (bool, string) {
	if len(promotions) == 0 {
		return false, ""
	}

	// Get source SHA for breaking change detection
	sourceSHA := promotions[0].SHA
	if sourceSHA == "" {
		return false, ""
	}

	// Determine the transitions that should be blocked by breaking changes.
	// The implicit chain is [envs..., "release", prodEnv]; we block at the two
	// crossings around the virtual "release" env: prereleaseEnv → release and
	// release → prod. Each promotion's target_env is matched against this set.
	blockedTransitions := make(map[string]string) // target env -> transition description

	if prereleaseEnv != "" {
		blockedTransitions["release"] = fmt.Sprintf("%s → release", prereleaseEnv)
	}
	if prodEnv != "" {
		blockedTransitions[prodEnv] = fmt.Sprintf("release → %s", prodEnv)
	}
	// Pre-#45 callers may pass empty hasReleaseMarker for env lists that
	// don't include "release" literally; the chain is still implicit so we
	// keep both entries above. The flag is preserved for API compatibility.
	_ = hasReleaseMarker

	// Check if any promotion targets a blocked transition
	for _, promo := range promotions {
		if transition, blocked := blockedTransitions[promo.Environment]; blocked {
			// Check for actual breaking changes in the diff
			targetSHA := p.getEnvCurrentSHA(promo.Environment)
			if targetSHA != "" && p.hasBreakingChangesBetween(targetSHA, sourceSHA) {
				return true, transition
			}
		}
	}

	// For cascade mode targeting release or prod, check the final target
	if p.mode == ModeCascade && len(promotions) > 0 {
		finalEnv := promotions[len(promotions)-1].Environment
		if transition, blocked := blockedTransitions[finalEnv]; blocked {
			targetSHA := p.getEnvCurrentSHA(finalEnv)
			if targetSHA != "" && p.hasBreakingChangesBetween(targetSHA, sourceSHA) {
				return true, transition
			}
		}
	}

	return false, ""
}

// getEnvCurrentSHA returns the current SHA for an environment
func (p *Preflighter) getEnvCurrentSHA(env string) string {
	if state := p.cicdFile.State[env]; state != nil {
		return state.SHA
	}
	return ""
}

// hasBreakingChangesBetween reports whether any commit in the range
// (baseSHA, headSHA] is a breaking change per the conventional-commit spec
// (`feat!:`, `fix!:`, or any commit body containing `BREAKING CHANGE:`).
//
// On any failure to read git history, returns false (fail-open). The gate
// is a guardrail, not a hard correctness check, and surfacing parse errors
// at preflight time would make the CLI brittle in environments with shallow
// or partially fetched histories.
func (p *Preflighter) hasBreakingChangesBetween(baseSHA, headSHA string) bool {
	if baseSHA == "" || headSHA == "" {
		return false
	}
	commits, err := git.GetCommits(baseSHA, headSHA, nil)
	if err != nil {
		return false
	}
	return containsBreakingCommit(commits)
}

// containsBreakingCommit reports whether any commit in the slice is breaking
// per the conventional-commit spec. Pure function for unit-testability.
func containsBreakingCommit(commits []git.Commit) bool {
	for _, gc := range commits {
		if cc := changelog.ParseCommit(gc); cc != nil && cc.Breaking {
			return true
		}
	}
	return false
}

// stringSliceContains checks if a string is in a slice
func stringSliceContains(slice []string, item string) bool {
	return slices.Contains(slice, item)
}

// firstNonEmptyArtifactID picks a single deterministic artifact identifier from
// an env's per-build state. artifact_id is per-build, but the deploy threads one
// env-level value, so build names are iterated in sorted order and the first
// build with a non-empty artifact_id wins. Returns "" when no build has one, so
// callers can omit the deploy input gracefully. Operators must populate a
// build's artifact_id output with the content digest to use this; cascade treats
// artifact_id as an opaque immutable id.
func firstNonEmptyArtifactID(builds map[string]*config.BuildState) string {
	names := make([]string, 0, len(builds))
	for name := range builds {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if b := builds[name]; b != nil && b.ArtifactID != "" {
			return b.ArtifactID
		}
	}
	return ""
}

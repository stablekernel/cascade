package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/stablekernel/cascade/internal/config"
)

// PlannedFile is a single workflow or action file the manifest would generate,
// paired with its rendered content. Path mirrors the exact location the generate
// command writes to: relative paths for the workflow files (resolved against the
// current working directory) and an absolute path under baseDir for the composite
// action.
type PlannedFile struct {
	Path    string
	Content string
}

// PlanOptions configures a Plan run. The fields mirror the generate-workflow
// flags that affect which files are emitted and where, so a Plan reproduces the
// generate command's full-set output without touching the filesystem.
type PlanOptions struct {
	ConfigPath        string
	ManifestKey       string
	ActionFolder      string
	OutputPath        string
	PromoteOutputPath string
	// PinOverridesPath, when non-empty, names an on-disk action_pins.yaml whose
	// pins overlay cfg.ActionPins (via ApplyDiskPinOverrides) before any
	// generator runs. A version-pinned reconcile binary uses this to regenerate
	// against a repo's current pins instead of the binary's stale compiled-in
	// defaultActionPins copy; an explicit user action_pins override still wins.
	PinOverridesPath string
	// OwnRepo plans cascade's own-repo release-plumbing variant of the
	// orchestrate workflow and the manage-release composite action (tag-only
	// finalize step, non-triggering GITHUB_TOKEN tag-create) instead of the
	// plain output every downstream manifest produces. Only cascade's own
	// verify/generate-workflow --own-repo invocation sets this; it is not a
	// manifest field.
	OwnRepo bool
}

// Plan resolves the manifest and returns the complete set of files the generate
// command would write for it, each paired with its rendered content. The result
// is sorted by Path and is deterministic across calls. Plan never touches the
// filesystem beyond reading the manifest and the reusable-workflow stubs the
// generators inspect; it performs no writes, no directory creation, and no git
// invocation.
//
// A parse failure, a missing manifest, or a config validation failure returns a
// non-nil error. These are operational failures distinct from any drift a caller
// may compute by comparing the returned content to bytes on disk.
func Plan(opts PlanOptions) ([]PlannedFile, error) {
	// Determine config path - auto-detect if not specified.
	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}

	cfg, err := config.ParseWithKey(configPath, opts.ManifestKey)
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if opts.PinOverridesPath != "" {
		if err := ApplyDiskPinOverrides(cfg, opts.PinOverridesPath); err != nil {
			return nil, fmt.Errorf("applying pin overrides: %w", err)
		}
	}

	// Parse the full manifest (including state) so generators can resolve
	// cascade-owned ${{ state.<env>.<field> }} input references. State is
	// optional; absence is not an error.
	var manifestState map[string]*config.EnvState
	if full, ferr := config.ParseManifestFile(configPath, opts.ManifestKey); ferr == nil {
		manifestState = full.State
	}

	// Override action folder if specified on command line.
	if opts.ActionFolder != "" && opts.ActionFolder != "manage-release" {
		cfg.ActionFolder = opts.ActionFolder
	}

	if errs := config.Validate(cfg); len(errs) > 0 {
		return nil, fmt.Errorf("config validation failed: %s", errs[0])
	}

	baseDir := resolveBaseDir(configPath)

	outputPath := opts.OutputPath
	if outputPath == "" {
		outputPath = ".github/workflows/orchestrate.yaml"
	}
	promoteOutputPath := opts.PromoteOutputPath
	if promoteOutputPath == "" {
		promoteOutputPath = ".github/workflows/promote.yaml"
	}

	var planned []PlannedFile

	// 1. orchestrate -> outputPath, or one path-scoped orchestrate-<name>.yaml per
	//    component when the manifest declares components: (verify always treats the
	//    full set as enabled).
	orchTargets, err := orchestrateTargets(cfg, baseDir, outputPath, manifestState, opts.OwnRepo)
	if err != nil {
		return nil, err
	}
	var content string
	for _, t := range orchTargets {
		content, err = t.Gen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating orchestrate workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: t.Path, Content: content})
	}

	// 2. promote (multi-env) or release (single-env). A single-env manifest keeps
	//    the single release workflow. A multi-env manifest with components: fans
	//    out to one promote-<name>.yaml per component; otherwise a single
	//    promote.yaml, byte-identical to today.
	if cfg.IsSingleEnvironment() {
		content, err = NewReleaseGenerator(cfg, baseDir).Generate()
		if err != nil {
			return nil, fmt.Errorf("generating release workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: promoteOutputPath, Content: content})
	} else {
		promoteTargets, perr := promoteTargets(cfg, baseDir, promoteOutputPath, manifestState)
		if perr != nil {
			return nil, perr
		}
		for _, t := range promoteTargets {
			content, err = t.Gen.Generate()
			if err != nil {
				return nil, fmt.Errorf("generating promote workflow: %w", err)
			}
			planned = append(planned, PlannedFile{Path: t.Path, Content: content})
		}
	}

	// 3. external-update -> .github/workflows/external-update.yaml when primary.
	if cfg.IsPrimary() {
		content, err = NewExternalUpdateGenerator(cfg, baseDir).Generate()
		if err != nil {
			return nil, fmt.Errorf("generating external-update workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: ".github/workflows/external-update.yaml", Content: content})
	}

	// 4. validate-check -> .github/workflows/cascade-validate.yaml when enabled.
	if gen := NewValidateCheckGenerator(cfg, baseDir); gen.Enabled() {
		content, err = gen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating validate-check workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: ".github/workflows/cascade-validate.yaml", Content: content})
	}

	// 5. merge-queue -> .github/workflows/cascade-merge-queue.yaml when enabled.
	if gen := NewMergeQueueGenerator(cfg, baseDir); gen.Enabled() {
		content, err = gen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating merge-queue workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: ".github/workflows/cascade-merge-queue.yaml", Content: content})
	}

	// 6. hotfix -> cascade-hotfix.yaml when enabled, or one path-scoped
	//    cascade-hotfix-<name>.yaml per component when the manifest declares
	//    components:.
	hfTargets, err := hotfixTargets(cfg, baseDir)
	if err != nil {
		return nil, err
	}
	for _, t := range hfTargets {
		content, err = t.Gen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating hotfix workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: t.Path, Content: content})
	}

	// 7. rollback -> cascade-rollback.yaml when enabled, or one path-scoped
	//    cascade-rollback-<name>.yaml per component when the manifest declares
	//    components:.
	rbTargets, err := rollbackTargets(cfg, baseDir)
	if err != nil {
		return nil, err
	}
	for _, t := range rbTargets {
		content, err = t.Gen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating rollback workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: t.Path, Content: content})
	}

	// 8. pr-preview -> .github/workflows/cascade-pr-preview.yaml when enabled.
	if cfg.PRPreview != nil && cfg.PRPreview.Enabled {
		content, err = NewPRPreviewGenerator(cfg, baseDir).Generate()
		if err != nil {
			return nil, fmt.Errorf("generating pr-preview workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: ".github/workflows/cascade-pr-preview.yaml", Content: content})
	}

	// 9. drift-check -> .github/workflows/cascade-drift-check.yaml when enabled,
	//    plus the fork-safe comment companion when drift_check.comment is set.
	if gen := NewDriftCheckGenerator(cfg, baseDir); gen.Enabled() {
		content, err = gen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating drift-check workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: ".github/workflows/cascade-drift-check.yaml", Content: content})

		if gen.commentEnabled() {
			commentContent, cerr := gen.GenerateComment()
			if cerr != nil {
				return nil, fmt.Errorf("generating drift-comment workflow: %w", cerr)
			}
			planned = append(planned, PlannedFile{Path: ".github/workflows/cascade-drift-comment.yaml", Content: commentContent})
		}
	}

	// 10. composite action -> baseDir/.github/actions/<folder>/action.yaml.
	action, err := RenderLocalActions(baseDir, cfg, opts.OwnRepo)
	if err != nil {
		return nil, fmt.Errorf("rendering local actions: %w", err)
	}
	planned = append(planned, action)

	sort.Slice(planned, func(i, j int) bool {
		return planned[i].Path < planned[j].Path
	})

	return planned, nil
}

// orchestrateTarget pairs a rendered orchestrate workflow's target path with the
// generator that produces it. The generator is returned unrendered so callers can
// validate it (generate command) or render it (Plan) without duplicating the
// component fan-out decision.
type orchestrateTarget struct {
	Path string
	Gen  *Generator
}

// orchestrateTargets returns the orchestrate workflow target(s) the manifest
// emits. A manifest with no components: block yields exactly one target at
// outputPath, byte-identical to the pre-component generator. A manifest that
// declares components yields one path-scoped, concurrency-isolated,
// component-namespaced orchestrate-<name>.yaml per component (sorted by name) and
// no repo-wide orchestrate file, because the repo is no longer a single
// orchestration unit. Both the generate command and Plan (verify's enumeration)
// call this, so they can never disagree on the per-component file set.
//
// This is the structural fan-out only. Per-component version, promotion, hotfix,
// and rollback semantics are deferred to their owning stages; the CLI invocations
// inside each generated workflow are unchanged from the single-component path.
func orchestrateTargets(cfg *config.TrunkConfig, baseDir, outputPath string, state map[string]*config.EnvState, ownRepo bool) ([]orchestrateTarget, error) {
	var baseOpts []GeneratorOption
	if ownRepo {
		baseOpts = append(baseOpts, WithOwnRepoRelease())
	}

	if !cfg.HasComponents() {
		gen := NewGenerator(cfg, baseDir, baseOpts...)
		gen.SetState(state)
		return []orchestrateTarget{{Path: outputPath, Gen: gen}}, nil
	}

	names := make([]string, 0, len(cfg.Components))
	for name := range cfg.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	targets := make([]orchestrateTarget, 0, len(names))
	for _, name := range names {
		resolved, err := cfg.ResolveComponent(name)
		if err != nil {
			return nil, fmt.Errorf("resolving component %q: %w", name, err)
		}
		genOpts := make([]GeneratorOption, 0, len(baseOpts)+1)
		genOpts = append(genOpts, baseOpts...)
		genOpts = append(genOpts, WithComponentName(name))
		gen := NewGenerator(resolved.Config, baseDir, genOpts...)
		gen.SetState(state)
		targets = append(targets, orchestrateTarget{Path: componentWorkflowPath(outputPath, name), Gen: gen})
	}
	return targets, nil
}

// componentWorkflowPath derives a per-component orchestrate workflow path from the
// base output path: the base directory plus orchestrate-<name>.yaml.
func componentWorkflowPath(outputPath, name string) string {
	return filepath.Join(filepath.Dir(outputPath), fmt.Sprintf("orchestrate-%s.yaml", name))
}

// promoteTarget pairs a rendered promote workflow's target path with the
// generator that produces it, mirroring orchestrateTarget so the generate command
// and Plan share one fan-out decision for the promote surface.
type promoteTarget struct {
	Path string
	Gen  *PromoteGenerator
}

// promoteTargets returns the promote workflow target(s) the manifest emits,
// mirroring orchestrateTargets. A manifest with no components: block yields
// exactly one target at outputPath, byte-identical to the pre-component promote
// generator. A manifest that declares components yields one promote-<name>.yaml
// per component (sorted by name) and no repo-wide promote file, each generated
// from the resolved per-component config and scoped with --component so promotion
// records state under that component's subtree. The manifest-global
// concurrency.group is captured before resolution so the per-component promote
// group can compose it instead of collapsing onto it. Both the generate command
// and Plan call this, so they can never disagree on the promote file set.
//
// Callers gate on IsSingleEnvironment before invoking this: single-env manifests
// keep the release-workflow branch, so promoteTargets only ever runs on the
// multi-env promote path.
func promoteTargets(cfg *config.TrunkConfig, baseDir, outputPath string, state map[string]*config.EnvState) ([]promoteTarget, error) {
	if !cfg.HasComponents() {
		gen := NewPromoteGenerator(cfg, baseDir)
		gen.SetState(state)
		return []promoteTarget{{Path: outputPath, Gen: gen}}, nil
	}

	var globalGroup string
	if cfg.Concurrency != nil {
		globalGroup = cfg.Concurrency.Group
	}

	names := make([]string, 0, len(cfg.Components))
	for name := range cfg.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	targets := make([]promoteTarget, 0, len(names))
	for _, name := range names {
		resolved, err := cfg.ResolveComponent(name)
		if err != nil {
			return nil, fmt.Errorf("resolving component %q: %w", name, err)
		}
		gen := NewPromoteGenerator(resolved.Config, baseDir,
			WithPromoteComponentName(name),
			WithPromoteGlobalConcurrencyGroup(globalGroup))
		gen.SetState(state)
		targets = append(targets, promoteTarget{Path: promoteComponentWorkflowPath(outputPath, name), Gen: gen})
	}
	return targets, nil
}

// promoteComponentWorkflowPath derives a per-component promote workflow path from
// the base output path: the base directory plus promote-<name>.yaml.
func promoteComponentWorkflowPath(outputPath, name string) string {
	return filepath.Join(filepath.Dir(outputPath), fmt.Sprintf("promote-%s.yaml", name))
}

// hotfixWorkflowPath and rollbackWorkflowPath are the canonical single-component
// output locations for the hotfix and rollback workflows. The generate command
// and Plan both anchor their fan-out on these so the two never disagree.
const (
	hotfixWorkflowPath   = ".github/workflows/cascade-hotfix.yaml"
	rollbackWorkflowPath = ".github/workflows/cascade-rollback.yaml"
)

// hotfixTarget pairs a rendered hotfix workflow's target path with the generator
// that produces it, mirroring promoteTarget so the generate command and Plan share
// one fan-out decision for the hotfix surface.
type hotfixTarget struct {
	Path string
	Gen  *HotfixGenerator
}

// hotfixTargets returns the hotfix workflow target(s) the manifest emits,
// mirroring promoteTargets. A manifest with no components: block yields exactly
// one target at the canonical path (byte-identical to the pre-component
// generator), or no targets when the single-component hotfix is not enabled
// (fewer than two environments). A manifest that declares components yields one
// cascade-hotfix-<name>.yaml per component whose resolved config enables the
// hotfix workflow (sorted by name) and no repo-wide hotfix file, each generated
// from the resolved per-component config and scoped with --component so plan and
// finalize record state under that component's subtree. Both the generate command
// and Plan call this, so they can never disagree on the hotfix file set.
func hotfixTargets(cfg *config.TrunkConfig, baseDir string) ([]hotfixTarget, error) {
	if !cfg.HasComponents() {
		gen := NewHotfixGenerator(cfg, baseDir)
		if !gen.Enabled() {
			return nil, nil
		}
		return []hotfixTarget{{Path: hotfixWorkflowPath, Gen: gen}}, nil
	}

	names := make([]string, 0, len(cfg.Components))
	for name := range cfg.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	targets := make([]hotfixTarget, 0, len(names))
	for _, name := range names {
		resolved, err := cfg.ResolveComponent(name)
		if err != nil {
			return nil, fmt.Errorf("resolving component %q: %w", name, err)
		}
		gen := NewHotfixGenerator(resolved.Config, baseDir, WithHotfixComponentName(name))
		if !gen.Enabled() {
			continue
		}
		targets = append(targets, hotfixTarget{Path: hotfixComponentWorkflowPath(name), Gen: gen})
	}
	return targets, nil
}

// hotfixComponentWorkflowPath derives a per-component hotfix workflow path from the
// canonical output path: the base directory plus cascade-hotfix-<name>.yaml.
func hotfixComponentWorkflowPath(name string) string {
	return filepath.Join(filepath.Dir(hotfixWorkflowPath), fmt.Sprintf("cascade-hotfix-%s.yaml", name))
}

// rollbackTarget pairs a rendered rollback workflow's target path with the
// generator that produces it, mirroring promoteTarget.
type rollbackTarget struct {
	Path string
	Gen  *RollbackGenerator
}

// rollbackTargets returns the rollback workflow target(s) the manifest emits,
// mirroring promoteTargets. A manifest with no components: block yields exactly
// one target at the canonical path (byte-identical to the pre-component
// generator), or no targets when the single-component rollback is not enabled. A
// manifest that declares components yields one cascade-rollback-<name>.yaml per
// component whose resolved config enables the rollback workflow (sorted by name)
// and no repo-wide rollback file, each generated from the resolved per-component
// config and scoped with --component. The manifest-global concurrency.group is
// captured before resolution so the per-component rollback group can compose it
// instead of collapsing onto it. Both the generate command and Plan call this, so
// they can never disagree on the rollback file set.
func rollbackTargets(cfg *config.TrunkConfig, baseDir string) ([]rollbackTarget, error) {
	if !cfg.HasComponents() {
		gen := NewRollbackGenerator(cfg, baseDir)
		if !gen.Enabled() {
			return nil, nil
		}
		return []rollbackTarget{{Path: rollbackWorkflowPath, Gen: gen}}, nil
	}

	var globalGroup string
	if cfg.Concurrency != nil {
		globalGroup = cfg.Concurrency.Group
	}

	names := make([]string, 0, len(cfg.Components))
	for name := range cfg.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	targets := make([]rollbackTarget, 0, len(names))
	for _, name := range names {
		resolved, err := cfg.ResolveComponent(name)
		if err != nil {
			return nil, fmt.Errorf("resolving component %q: %w", name, err)
		}
		gen := NewRollbackGenerator(resolved.Config, baseDir,
			WithRollbackComponentName(name),
			WithRollbackGlobalConcurrencyGroup(globalGroup))
		if !gen.Enabled() {
			continue
		}
		targets = append(targets, rollbackTarget{Path: rollbackComponentWorkflowPath(name), Gen: gen})
	}
	return targets, nil
}

// rollbackComponentWorkflowPath derives a per-component rollback workflow path from
// the canonical output path: the base directory plus cascade-rollback-<name>.yaml.
func rollbackComponentWorkflowPath(name string) string {
	return filepath.Join(filepath.Dir(rollbackWorkflowPath), fmt.Sprintf("cascade-rollback-%s.yaml", name))
}

// ResolveBaseDir reports the repo root the generate command resolves workflow
// paths against for the given config path: the config's directory, promoted one
// level up when the config lives in .github/. Callers that compare planned files
// (which carry relative workflow paths) against bytes on disk use this to anchor
// those relative paths to the manifest's repo root instead of the process
// working directory.
func ResolveBaseDir(configPath string) string {
	return resolveBaseDir(configPath)
}

// resolveBaseDir reproduces the generate command's base-directory resolution:
// the config's directory, promoted one level up when the config lives in
// .github/, so workflow paths resolve against the repo root.
func resolveBaseDir(configPath string) string {
	configDir := filepath.Dir(configPath)
	if !filepath.IsAbs(configDir) {
		cwd, _ := os.Getwd()
		configDir = filepath.Join(cwd, configDir)
	}
	baseDir := configDir
	if filepath.Base(configDir) == ".github" {
		baseDir = filepath.Dir(configDir)
	}
	return baseDir
}

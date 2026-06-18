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

	// 1. orchestrate -> outputPath (verify always treats the full set as enabled).
	orchestrateGen := NewGenerator(cfg, baseDir)
	orchestrateGen.SetState(manifestState)
	content, err := orchestrateGen.Generate()
	if err != nil {
		return nil, fmt.Errorf("generating orchestrate workflow: %w", err)
	}
	planned = append(planned, PlannedFile{Path: outputPath, Content: content})

	// 2. promote (multi-env) or release (single-env) -> promoteOutputPath.
	if cfg.IsSingleEnvironment() {
		content, err = NewReleaseGenerator(cfg, baseDir).Generate()
		if err != nil {
			return nil, fmt.Errorf("generating release workflow: %w", err)
		}
	} else {
		promoteGen := NewPromoteGenerator(cfg, baseDir)
		promoteGen.SetState(manifestState)
		content, err = promoteGen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating promote workflow: %w", err)
		}
	}
	planned = append(planned, PlannedFile{Path: promoteOutputPath, Content: content})

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

	// 6. hotfix -> .github/workflows/cascade-hotfix.yaml when enabled.
	if gen := NewHotfixGenerator(cfg, baseDir); gen.Enabled() {
		content, err = gen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating hotfix workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: ".github/workflows/cascade-hotfix.yaml", Content: content})
	}

	// 7. rollback -> .github/workflows/cascade-rollback.yaml when enabled.
	if gen := NewRollbackGenerator(cfg, baseDir); gen.Enabled() {
		content, err = gen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating rollback workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: ".github/workflows/cascade-rollback.yaml", Content: content})
	}

	// 8. pr-preview -> .github/workflows/cascade-pr-preview.yaml when enabled.
	if cfg.PRPreview != nil && cfg.PRPreview.Enabled {
		content, err = NewPRPreviewGenerator(cfg, baseDir).Generate()
		if err != nil {
			return nil, fmt.Errorf("generating pr-preview workflow: %w", err)
		}
		planned = append(planned, PlannedFile{Path: ".github/workflows/cascade-pr-preview.yaml", Content: content})
	}

	// 9. composite action -> baseDir/.github/actions/<folder>/action.yaml.
	action, err := RenderLocalActions(baseDir, cfg)
	if err != nil {
		return nil, fmt.Errorf("rendering local actions: %w", err)
	}
	planned = append(planned, action)

	sort.Slice(planned, func(i, j int) bool {
		return planned[i].Path < planned[j].Path
	})

	return planned, nil
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

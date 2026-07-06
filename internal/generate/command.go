package generate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stablekernel/cascade/internal/config"
)

// NewCommand creates the generate-workflow command
func NewCommand() *cobra.Command {
	var configPath string
	var manifestKey string
	var actionFolder string
	var outputPath string
	var promoteOutputPath string
	var validateOnly bool
	var dryRun bool
	var force bool
	var commit bool
	var push bool
	var orchestrateOnly bool
	var promoteOnly bool
	var ownRepo bool

	cmd := &cobra.Command{
		Use:   "generate-workflow",
		Short: "Generate orchestration workflow from manifest.yaml",
		Long: `Generate GitHub Actions workflow files that orchestrate builds, deploys, and promotions
based on the manifest.yaml configuration. By default generates both:
  - orchestrate.yaml: Handles CI/CD on trunk merges
  - promote.yaml: Handles environment promotions

Use --orchestrate-only or --promote-only to generate just one workflow.

Configuration is read from .github/manifest.yaml (or .github/manifest.yml) under
the "ci" key by default. Use --manifest-key to change the key name.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// --push implies --commit
			if push {
				commit = true
			}
			opts := generateOptions{
				configPath:        configPath,
				manifestKey:       manifestKey,
				actionFolder:      actionFolder,
				outputPath:        outputPath,
				promoteOutputPath: promoteOutputPath,
				validateOnly:      validateOnly,
				dryRun:            dryRun,
				force:             force,
				commit:            commit,
				push:              push,
				orchestrateOnly:   orchestrateOnly,
				promoteOnly:       promoteOnly,
				ownRepo:           ownRepo,
			}
			return runGenerateWorkflow(opts)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file (default: auto-detect .github/manifest.yaml)")
	cmd.Flags().StringVar(&manifestKey, "manifest-key", config.DefaultManifestKey, "Key in manifest file containing CI config")
	cmd.Flags().StringVar(&actionFolder, "action-folder", "manage-release", "Folder name for the manage-release composite action")
	cmd.Flags().StringVarP(&outputPath, "output", "o", ".github/workflows/orchestrate.yaml", "Output path for orchestrate workflow")
	cmd.Flags().StringVar(&promoteOutputPath, "promote-output", ".github/workflows/promote.yaml", "Output path for promote workflow")
	cmd.Flags().BoolVar(&validateOnly, "validate-only", false, "Validate config without generating")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print generated workflow to stdout")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite output file without prompting")
	cmd.Flags().BoolVar(&commit, "commit", false, "Commit generated workflow files to git")
	cmd.Flags().BoolVarP(&push, "push", "p", false, "Push commit to remote (implies --commit)")
	cmd.Flags().BoolVar(&orchestrateOnly, "orchestrate-only", false, "Only generate orchestrate.yaml")
	cmd.Flags().BoolVar(&promoteOnly, "promote-only", false, "Only generate promote.yaml")
	cmd.Flags().BoolVar(&ownRepo, "own-repo", false, "Generate cascade's own-repo release-plumbing variant (tag-only manage-release, non-triggering tag-create). Maintainer-only; never used by a downstream manifest.")

	return cmd
}

type generateOptions struct {
	configPath         string
	manifestKey        string
	actionFolder       string
	outputPath         string
	promoteOutputPath  string
	externalOutputPath string
	validateOnly       bool
	dryRun             bool
	force              bool
	commit             bool
	push               bool
	orchestrateOnly    bool
	promoteOnly        bool
	ownRepo            bool
}

func runGenerateWorkflow(opts generateOptions) error {
	// Determine config path - auto-detect if not specified
	configPath := opts.configPath
	if configPath == "" {
		configPath = config.FindConfigFile("")
	}

	// Parse config with manifest key
	cfg, err := config.ParseWithKey(configPath, opts.manifestKey)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	// Parse the full manifest (including state) so generators can resolve
	// cascade-owned ${{ state.<env>.<field> }} input references at generation
	// time. State is optional; absence is not an error.
	var manifestState map[string]*config.EnvState
	if full, ferr := config.ParseManifestFile(configPath, opts.manifestKey); ferr == nil {
		manifestState = full.State
	}

	// Override action folder if specified on command line
	if opts.actionFolder != "" && opts.actionFolder != "manage-release" {
		cfg.ActionFolder = opts.actionFolder
	}

	// Validate config
	if errs := config.Validate(cfg); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "Error: %s\n", e)
		}
		return fmt.Errorf("config validation failed")
	}

	// Get base directory for workflow file resolution
	// Workflow paths are relative to repo root
	configDir := filepath.Dir(configPath)
	if !filepath.IsAbs(configDir) {
		cwd, _ := os.Getwd()
		configDir = filepath.Join(cwd, configDir)
	}
	// If config is in .github/, go up one level to get repo root
	baseDir := configDir
	if filepath.Base(configDir) == ".github" {
		baseDir = filepath.Dir(configDir)
	}

	// Determine which workflows to generate
	generateOrchestrate := !opts.promoteOnly
	generatePromote := !opts.orchestrateOnly

	// Create orchestrate generator and validate
	var orchestrateGen *Generator
	if generateOrchestrate {
		var orchestrateOpts []GeneratorOption
		if opts.ownRepo {
			orchestrateOpts = append(orchestrateOpts, WithOwnRepoRelease())
		}
		orchestrateGen = NewGenerator(cfg, baseDir, orchestrateOpts...)
		orchestrateGen.SetState(manifestState)
		warnings := orchestrateGen.Validate()
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "%s\n", w)
		}
	}

	if opts.validateOnly {
		fmt.Println("Config is valid")
		return nil
	}

	var generatedFiles []string

	// Generate orchestrate workflow
	if generateOrchestrate {
		content, err := orchestrateGen.Generate()
		if err != nil {
			return fmt.Errorf("generating orchestrate workflow: %w", err)
		}

		if opts.dryRun {
			fmt.Println("=== orchestrate.yaml ===")
			fmt.Print(content)
		} else {
			if err := writeWorkflow(opts.outputPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, opts.outputPath)
			fmt.Printf("Generated workflow: %s\n", opts.outputPath)
		}
	}

	// Generate promote/release workflow based on number of environments
	if generatePromote {
		var content string
		var err error
		var workflowName string

		if cfg.IsSingleEnvironment() {
			// Single-environment projects get a simpler Release workflow
			releaseGen := NewReleaseGenerator(cfg, baseDir)
			content, err = releaseGen.Generate()
			workflowName = "release"
		} else {
			// Multi-environment projects get the full Promote workflow
			promoteGen := NewPromoteGenerator(cfg, baseDir)
			promoteGen.SetState(manifestState)
			content, err = promoteGen.Generate()
			workflowName = "promote"
		}

		if err != nil {
			return fmt.Errorf("generating %s workflow: %w", workflowName, err)
		}

		if opts.dryRun {
			if generateOrchestrate {
				fmt.Printf("\n=== %s.yaml ===\n", workflowName)
			}
			fmt.Print(content)
		} else {
			if err := writeWorkflow(opts.promoteOutputPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, opts.promoteOutputPath)
			fmt.Printf("Generated workflow: %s\n", opts.promoteOutputPath)
		}
	}

	// Generate external-update workflow for primary repos
	if cfg.IsPrimary() {
		externalGen := NewExternalUpdateGenerator(cfg, baseDir)
		content, err := externalGen.Generate()
		if err != nil {
			return fmt.Errorf("generating external-update workflow: %w", err)
		}

		externalOutputPath := ".github/workflows/external-update.yaml"
		if opts.externalOutputPath != "" {
			externalOutputPath = opts.externalOutputPath
		}

		if opts.dryRun {
			fmt.Println("\n=== external-update.yaml ===")
			fmt.Print(content)
		} else {
			if err := writeWorkflow(externalOutputPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, externalOutputPath)
			fmt.Printf("Generated workflow: %s\n", externalOutputPath)
		}
	}

	// Generate the opt-in manifest-validation PR check (validate_check.enabled).
	validateCheckGen := NewValidateCheckGenerator(cfg, baseDir)
	if validateCheckGen.Enabled() {
		content, err := validateCheckGen.Generate()
		if err != nil {
			return fmt.Errorf("generating validate-check workflow: %w", err)
		}
		outPath := ".github/workflows/cascade-validate.yaml"
		if opts.dryRun {
			fmt.Println("\n=== cascade-validate.yaml ===")
			fmt.Print(content)
		} else {
			if err := writeWorkflow(outPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, outPath)
			fmt.Printf("Generated workflow: %s\n", outPath)
		}
	}

	// Generate the opt-in merge-queue validation lane (merge_queue.enabled).
	mergeQueueGen := NewMergeQueueGenerator(cfg, baseDir)
	if mergeQueueGen.Enabled() {
		content, err := mergeQueueGen.Generate()
		if err != nil {
			return fmt.Errorf("generating merge-queue workflow: %w", err)
		}
		outPath := ".github/workflows/cascade-merge-queue.yaml"
		if opts.dryRun {
			fmt.Println("\n=== cascade-merge-queue.yaml ===")
			fmt.Print(content)
		} else {
			if err := writeWorkflow(outPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, outPath)
			fmt.Printf("Generated workflow: %s\n", outPath)
		}
	}

	// Generate the hotfix workflow when 2+ environments are configured (Q1).
	hotfixGen := NewHotfixGenerator(cfg, baseDir)
	if hotfixGen.Enabled() {
		content, err := hotfixGen.Generate()
		if err != nil {
			return fmt.Errorf("generating hotfix workflow: %w", err)
		}
		outPath := ".github/workflows/cascade-hotfix.yaml"
		if opts.dryRun {
			fmt.Println("\n=== cascade-hotfix.yaml ===")
			fmt.Print(content)
		} else {
			if err := writeWorkflow(outPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, outPath)
			fmt.Printf("Generated workflow: %s\n", outPath)
		}
	}

	// Generate the rollback workflow when at least one environment is configured.
	rollbackGen := NewRollbackGenerator(cfg, baseDir)
	if rollbackGen.Enabled() {
		content, err := rollbackGen.Generate()
		if err != nil {
			return fmt.Errorf("generating rollback workflow: %w", err)
		}
		outPath := ".github/workflows/cascade-rollback.yaml"
		if opts.dryRun {
			fmt.Println("\n=== cascade-rollback.yaml ===")
			fmt.Print(content)
		} else {
			if err := writeWorkflow(outPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, outPath)
			fmt.Printf("Generated workflow: %s\n", outPath)
		}
	}

	// Generate the opt-in read-only PR plan-preview workflow (#40). Absent or
	// disabled pr_preview emits nothing, so existing manifests are unaffected.
	if cfg.PRPreview != nil && cfg.PRPreview.Enabled {
		previewGen := NewPRPreviewGenerator(cfg, baseDir)
		content, err := previewGen.Generate()
		if err != nil {
			return fmt.Errorf("generating pr-preview workflow: %w", err)
		}

		previewOutputPath := ".github/workflows/cascade-pr-preview.yaml"

		if opts.dryRun {
			fmt.Println("\n=== cascade-pr-preview.yaml ===")
			fmt.Print(content)
		} else {
			if err := writeWorkflow(previewOutputPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, previewOutputPath)
			fmt.Printf("Generated workflow: %s\n", previewOutputPath)
		}
	}

	// Generate the opt-in workflow drift-check lane (#229). Absent or disabled
	// drift_check emits nothing, so existing manifests are unaffected. The
	// comment companion is emitted only when drift_check.comment is also set.
	driftGen := NewDriftCheckGenerator(cfg, baseDir)
	if driftGen.Enabled() {
		content, err := driftGen.Generate()
		if err != nil {
			return fmt.Errorf("generating drift-check workflow: %w", err)
		}
		outPath := ".github/workflows/cascade-drift-check.yaml"
		if opts.dryRun {
			fmt.Println("\n=== cascade-drift-check.yaml ===")
			fmt.Print(content)
		} else {
			if err := writeWorkflow(outPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, outPath)
			fmt.Printf("Generated workflow: %s\n", outPath)
		}

		if driftGen.commentEnabled() {
			commentContent, err := driftGen.GenerateComment()
			if err != nil {
				return fmt.Errorf("generating drift-comment workflow: %w", err)
			}
			commentOutPath := ".github/workflows/cascade-drift-comment.yaml"
			if opts.dryRun {
				fmt.Println("\n=== cascade-drift-comment.yaml ===")
				fmt.Print(commentContent)
			} else {
				if err := writeWorkflow(commentOutPath, commentContent, opts.force); err != nil {
					return err
				}
				generatedFiles = append(generatedFiles, commentOutPath)
				fmt.Printf("Generated workflow: %s\n", commentOutPath)
			}
		}
	}

	// Generate the opt-in emitted pin-reconcile lane (reconcile.enabled):
	// the pull_request detector plus its workflow_run companion. Absent or
	// disabled reconcile emits nothing, so existing manifests are unaffected.
	reconcileGen := NewReconcileGenerator(cfg, baseDir)
	if reconcileGen.Enabled() {
		content, err := reconcileGen.Generate()
		if err != nil {
			return fmt.Errorf("generating reconcile-check workflow: %w", err)
		}
		outPath := ".github/workflows/cascade-reconcile-check.yaml"
		if opts.dryRun {
			fmt.Println("\n=== cascade-reconcile-check.yaml ===")
			fmt.Print(content)
		} else {
			if err := writeWorkflow(outPath, content, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, outPath)
			fmt.Printf("Generated workflow: %s\n", outPath)
		}

		companionContent, err := reconcileGen.GenerateCompanion()
		if err != nil {
			return fmt.Errorf("generating reconcile-companion workflow: %w", err)
		}
		companionOutPath := ".github/workflows/cascade-reconcile-companion.yaml"
		if opts.dryRun {
			fmt.Println("\n=== cascade-reconcile-companion.yaml ===")
			fmt.Print(companionContent)
		} else {
			if err := writeWorkflow(companionOutPath, companionContent, opts.force); err != nil {
				return err
			}
			generatedFiles = append(generatedFiles, companionOutPath)
			fmt.Printf("Generated workflow: %s\n", companionOutPath)
		}
	}

	if opts.dryRun {
		return nil
	}

	// Generate local actions (manage-release or custom folder name)
	if err := GenerateLocalActions(baseDir, cfg, opts.ownRepo); err != nil {
		return fmt.Errorf("generating local actions: %w", err)
	}
	actionPath := filepath.Join(baseDir, ".github", "actions", cfg.GetActionFolder(), "action.yaml")
	generatedFiles = append(generatedFiles, actionPath)
	fmt.Printf("Generated action: %s\n", actionPath)

	// Commit the generated files if requested
	if opts.commit && len(generatedFiles) > 0 {
		committed, err := gitCommitFiles(generatedFiles)
		if err != nil {
			return fmt.Errorf("committing workflows: %w", err)
		}
		if committed {
			fmt.Printf("Committed: %v\n", generatedFiles)
		}

		// Push if requested (and we actually committed something)
		if opts.push && committed {
			if err := gitPush(); err != nil {
				return fmt.Errorf("pushing to remote: %w", err)
			}
			fmt.Println("Pushed to remote")
		}
	}

	return nil
}

// writeWorkflow writes the workflow content to the specified path
func writeWorkflow(outputPath, content string, force bool) error {
	// Check if output exists
	if !force {
		if _, err := os.Stat(outputPath); err == nil {
			return fmt.Errorf("output file %s exists, use --force to overwrite", outputPath)
		}
	}

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Write output
	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing output file: %w", err)
	}

	return nil
}

// gitCommitFiles stages and commits the specified files.
// Returns true if a commit was created, false if there were no changes.
func gitCommitFiles(filePaths []string) (bool, error) {
	// Stage all files
	args := append([]string{"add"}, filePaths...)
	addCmd := exec.Command("git", args...)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git add failed: %s: %w", string(output), err)
	}

	// Check if there are staged changes
	diffCmd := exec.Command("git", "diff", "--cached", "--quiet")
	if err := diffCmd.Run(); err == nil {
		// No changes to commit
		fmt.Println("No changes to commit")
		return false, nil
	}

	// Build commit message with file names
	var fileNames []string
	for _, fp := range filePaths {
		fileNames = append(fileNames, filepath.Base(fp))
	}
	commitMsg := fmt.Sprintf("chore: regenerate %s\n\nAuto-generated by cascade generate-workflow", strings.Join(fileNames, ", "))
	commitCmd := exec.Command("git", "commit", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git commit failed: %s: %w", string(output), err)
	}

	return true, nil
}

// gitPush pushes the current branch to its upstream remote
func gitPush() error {
	pushCmd := exec.Command("git", "push")
	if output, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %s: %w", string(output), err)
	}
	return nil
}

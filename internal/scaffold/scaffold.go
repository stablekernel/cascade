// Package scaffold renders a starter cascade manifest and the matching
// reusable-workflow stubs for a project, so a new repository can adopt cascade
// with a working, self-consistent configuration on the first try.
//
// The rendered output is verified before it is returned: Scaffold runs
// SelfCheck on its own files, which writes them to a temporary directory and
// confirms the manifest parses, validates, and generates orchestration
// workflows. A scaffold that cannot survive the real generator is never handed
// back to the caller.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/generate"
)

// schemaDirective is the YAML language-server schema comment placed as the very
// first line of every generated manifest. It is a YAML comment, so it is inert
// to the parser while still giving editors schema-aware completion.
const schemaDirective = "# yaml-language-server: $schema=https://stablekernel.github.io/cascade/manifest.schema.json"

const (
	manifestPath = config.DefaultManifestFile
	buildPath    = ".github/workflows/build.yaml"
	deployPath   = ".github/workflows/deploy.yaml"
)

// scaffoldConfig holds the resolved, optional inputs for a scaffold render.
type scaffoldConfig struct {
	cliVersion string
}

// Option customizes optional scaffold behavior. Required inputs are positional
// on Scaffold; Options form the variadic tail so new capability stays additive.
type Option func(*scaffoldConfig)

// WithCLIVersion overrides the cascade CLI version pinned in the generated
// manifest. An empty value is ignored so the default version is retained.
func WithCLIVersion(v string) Option {
	return func(c *scaffoldConfig) {
		if v != "" {
			c.cliVersion = v
		}
	}
}

// Topologies returns the preset environment-name lists keyed by topology name.
// Env names are applied positionally by Scaffold, so callers may substitute
// their own ordered names for any preset.
func Topologies() map[string][]string {
	return map[string][]string{
		"no-env":    {},
		"two-env":   {"dev", "prod"},
		"three-env": {"dev", "staging", "prod"},
		"four-env":  {"dev", "test", "uat", "prod"},
	}
}

// Scaffold renders a starter manifest plus reusable-workflow stubs for project,
// trunkBranch, and the ordered envs list, returning a map of relative path to
// file content. When envs is empty the result is release-only: the manifest and
// build stub are produced with no deploys block and no deploy stub. The output
// is verified with SelfCheck before it is returned, and any SelfCheck failure
// is surfaced to the caller.
func Scaffold(project, trunkBranch string, envs []string, opts ...Option) (map[string]string, error) {
	c := scaffoldConfig{cliVersion: config.DefaultCLIVersion}
	for _, o := range opts {
		o(&c)
	}

	manifest, err := renderManifest(trunkBranch, c.cliVersion, envs)
	if err != nil {
		return nil, fmt.Errorf("rendering manifest: %w", err)
	}

	files := map[string]string{
		manifestPath: manifest,
		buildPath:    strings.ReplaceAll(buildStub, "<name>", project),
	}
	if len(envs) > 0 {
		files[deployPath] = strings.ReplaceAll(deployStub, "<name>", project)
	}

	if err := SelfCheck(files); err != nil {
		return nil, fmt.Errorf("scaffold self-check failed: %w", err)
	}
	return files, nil
}

// manifestDoc mirrors the ci: -> config: shape of a cascade manifest so the
// scaffold can marshal a minimal, ordered document without dragging in the full
// config type and all of its reserved fields.
type manifestDoc struct {
	CI struct {
		Config manifestConfig `yaml:"config"`
	} `yaml:"ci"`
}

type manifestConfig struct {
	TrunkBranch  string          `yaml:"trunk_branch"`
	CLIVersion   string          `yaml:"cli_version"`
	Environments []string        `yaml:"environments,omitempty"`
	Builds       []manifestJob   `yaml:"builds"`
	Deploys      []manifestJob   `yaml:"deploys,omitempty"`
	Changelog    manifestCLEntry `yaml:"changelog"`
}

type manifestJob struct {
	Name     string   `yaml:"name"`
	Workflow string   `yaml:"workflow"`
	Triggers []string `yaml:"triggers"`
}

type manifestCLEntry struct {
	Contributors bool `yaml:"contributors"`
}

// renderManifest marshals the starter manifest and prepends the schema
// directive as the first line.
func renderManifest(trunkBranch, cliVersion string, envs []string) (string, error) {
	var doc manifestDoc
	doc.CI.Config.TrunkBranch = trunkBranch
	doc.CI.Config.CLIVersion = cliVersion
	if len(envs) > 0 {
		doc.CI.Config.Environments = envs
	}
	doc.CI.Config.Builds = []manifestJob{
		{Name: "build", Workflow: ".github/workflows/build.yaml", Triggers: []string{}},
	}
	if len(envs) > 0 {
		doc.CI.Config.Deploys = []manifestJob{
			{Name: "deploy", Workflow: ".github/workflows/deploy.yaml", Triggers: []string{}},
		}
	}
	doc.CI.Config.Changelog = manifestCLEntry{Contributors: true}

	body, err := yaml.Marshal(&doc)
	if err != nil {
		return "", fmt.Errorf("marshaling manifest yaml: %w", err)
	}
	return schemaDirective + "\n" + string(body), nil
}

// SelfCheck writes files to a temporary directory and confirms the manifest
// parses, validates with zero problems, and drives the real workflow
// generators. The promote generator is exercised whenever the parsed config has
// deploys. All failures are wrapped with descriptive context.
func SelfCheck(files map[string]string) error {
	dir, err := os.MkdirTemp("", "cascade-scaffold-*")
	if err != nil {
		return fmt.Errorf("creating temp dir for self-check: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return fmt.Errorf("creating dir for %s: %w", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
	}

	parsed, err := config.ParseManifestFile(filepath.Join(dir, manifestPath), config.DefaultManifestKey)
	if err != nil {
		return fmt.Errorf("parsing scaffolded manifest: %w", err)
	}
	if parsed.Config == nil {
		return fmt.Errorf("parsing scaffolded manifest: nil config")
	}

	if problems := config.Validate(parsed.Config); len(problems) > 0 {
		return fmt.Errorf("validating scaffolded manifest: %s", strings.Join(problems, "; "))
	}

	if _, err := generate.NewGenerator(parsed.Config, dir).Generate(); err != nil {
		return fmt.Errorf("generating orchestration workflow: %w", err)
	}

	if len(parsed.Config.Deploys) > 0 {
		if _, err := generate.NewPromoteGenerator(parsed.Config, dir).Generate(); err != nil {
			return fmt.Errorf("generating promotion workflow: %w", err)
		}
	}

	return nil
}

// buildStub is the reusable build workflow rendered for every scaffold. The
// only templated value is <name>; GitHub Actions ${{ ... }} expressions are
// kept literal by embedding the YAML as a raw string and substituting just the
// project name.
const buildStub = `name: Build <name>
on:
  workflow_call:
    inputs:
      environment:
        type: string
        required: true
      sha:
        type: string
        required: true
      dry_run:
        type: boolean
        required: false
        default: false
    outputs:
      artifact_id:
        description: Immutable artifact identifier (image digest, checksum)
        value: ${{ jobs.build.outputs.artifact_id }}
jobs:
  build:
    runs-on: ubuntu-latest
    outputs:
      artifact_id: ${{ steps.placeholder.outputs.artifact_id }}
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ inputs.sha }}
      - id: placeholder
        name: Placeholder build
        run: |
          echo "Building <name> for ${{ inputs.environment }} at ${{ inputs.sha }}"
          # TODO: replace with your real build; emit a real artifact id
          echo "artifact_id=placeholder-${{ inputs.sha }}" >> "$GITHUB_OUTPUT"
`

// deployStub is the reusable deploy workflow rendered when at least one
// environment is requested. Like buildStub, only <name> is substituted and all
// ${{ ... }} expressions remain literal.
const deployStub = `name: Deploy <name>
on:
  workflow_call:
    inputs:
      environment:
        type: string
        required: true
      sha:
        type: string
        required: true
      dry_run:
        type: boolean
        required: false
        default: false
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: ${{ inputs.environment }}
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ inputs.sha }}
      - name: Placeholder deploy
        if: ${{ !inputs.dry_run }}
        run: |
          echo "Deploying <name> to ${{ inputs.environment }} at ${{ inputs.sha }}"
          # TODO: replace with your real deploy
      - name: Dry run preview
        if: ${{ inputs.dry_run }}
        run: echo "Would deploy <name> to ${{ inputs.environment }}"
`

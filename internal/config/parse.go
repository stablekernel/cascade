package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultManifestFile is the default manifest file path
const DefaultManifestFile = ".github/manifest.yaml"

// DefaultManifestKey is the default key used in the manifest file
const DefaultManifestKey = "ci"

// ParseManifestFile reads and parses a manifest file with config under a specific key.
// The manifest must have the specified key (default: "ci") at the top level.
// Returns an error if the key is not found.
func ParseManifestFile(path, key string) (*CICDFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest file: %w", err)
	}

	file, err := ParseManifestBytes(data, key)
	if err != nil {
		return nil, err
	}

	if file.Config != nil {
		file.Config.ManifestFile = path
	}
	return file, nil
}

// ParseManifestBytes parses manifest bytes with config under a specific key,
// mirroring ParseManifestFile without reading from disk. The manifest must have
// the specified key (default: "ci") at the top level. Returns an error if the
// key is not found. This is the entry point for reading a manifest fetched from
// a branch ref (for example, trunk) rather than the working tree.
func ParseManifestBytes(data []byte, key string) (*CICDFile, error) {
	if key == "" {
		key = DefaultManifestKey
	}

	var manifest map[string]interface{}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest YAML: %w", err)
	}

	ciData, ok := manifest[key]
	if !ok {
		return nil, fmt.Errorf("manifest file missing required '%s' key at top level", key)
	}

	// Re-marshal and unmarshal to get the CICDFile structure
	ciBytes, err := yaml.Marshal(ciData)
	if err != nil {
		return nil, fmt.Errorf("marshaling %s section: %w", key, err)
	}

	var file CICDFile
	if err := yaml.Unmarshal(ciBytes, &file); err != nil {
		return nil, fmt.Errorf("parsing %s section: %w", key, err)
	}

	// Initialize state map if nil
	if file.State == nil {
		file.State = make(map[string]*EnvState)
	}

	// Ensure all environments have state entries
	if file.Config != nil {
		file.Config.ManifestKey = key
		for _, env := range file.Config.Environments {
			if file.State[env] == nil {
				file.State[env] = &EnvState{}
			}
		}
	}

	return &file, nil
}

// FindManifestFile looks for manifest.yaml or manifest.yml in the given directory.
// Returns the path to the found file, or empty string if not found.
func FindManifestFile(baseDir string) string {
	yamlPath := filepath.Join(baseDir, ".github", "manifest.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath
	}
	ymlPath := filepath.Join(baseDir, ".github", "manifest.yml")
	if _, err := os.Stat(ymlPath); err == nil {
		return ymlPath
	}
	return ""
}

// FindConfigFile finds the manifest file path.
// If baseDir is empty, uses current working directory.
// Returns the default path if no file is found.
func FindConfigFile(baseDir string) string {
	if baseDir == "" {
		baseDir, _ = os.Getwd()
	}

	if found := FindManifestFile(baseDir); found != "" {
		return found
	}

	// Return default path even if not found (let Parse handle the error)
	return DefaultManifestFile
}

// ParseWithKey reads and parses a manifest file with config under a specific key.
// The manifest must have the specified key (default: "ci") at the top level.
func ParseWithKey(path, key string) (*TrunkConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	if key == "" {
		key = DefaultManifestKey
	}

	var manifest map[string]interface{}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest YAML: %w", err)
	}

	ciData, ok := manifest[key]
	if !ok {
		return nil, fmt.Errorf("manifest file missing required '%s' key at top level", key)
	}

	// Re-marshal and unmarshal to get the CICDFile structure
	ciBytes, err := yaml.Marshal(ciData)
	if err != nil {
		return nil, fmt.Errorf("marshaling %s section: %w", key, err)
	}

	var file CICDFile
	if err := yaml.Unmarshal(ciBytes, &file); err != nil {
		return nil, fmt.Errorf("parsing %s section: %w", key, err)
	}

	if file.Config == nil {
		return nil, fmt.Errorf("manifest file missing 'config' section under '%s'", key)
	}

	file.Config.ManifestFile = path
	file.Config.ManifestKey = key
	return file.Config, nil
}

// Parse reads and parses a manifest file with config under the default "ci" key.
func Parse(path string) (*TrunkConfig, error) {
	return ParseWithKey(path, DefaultManifestKey)
}

// Validate checks the configuration for errors
func Validate(cfg *TrunkConfig) []string {
	var errors []string

	// Schema version compatibility is a fatal-class check: an unreadable schema
	// must block generation. Warnings (omitted/older-supported version) are
	// surfaced separately via ParseResult.Warnings, not here.
	if _, fatalErr := cfg.ValidateSchemaVersion(); fatalErr != nil {
		errors = append(errors, fatalErr.Error())
	}

	// Empty environments is valid - means pre-release -> release only (no deployments)

	envSet := make(map[string]bool)
	for i, env := range cfg.Environments {
		envSet[env] = true
		// Environment names key job IDs and ${{ }} expression references, so they
		// must be job-ID-safe.
		errors = append(errors, validateJobIDSafeName(fmt.Sprintf("environments[%d]", i), env)...)
	}

	// Build name sets for each section (builds and deploys can share names)
	buildNames := make(map[string]bool)
	deployNames := make(map[string]bool)

	// Validate builds
	for i, b := range cfg.Builds {
		if b.Name == "" {
			errors = append(errors, fmt.Sprintf("builds[%d].name is required", i))
		} else if buildNames[b.Name] {
			errors = append(errors, fmt.Sprintf("duplicate build name: %s", b.Name))
		} else {
			buildNames[b.Name] = true
		}
		// The name becomes part of the job ID (build-<name>); enforce the job-ID
		// grammar so generation cannot emit invalid YAML.
		errors = append(errors, validateJobIDSafeName(fmt.Sprintf("builds[%d].name", i), b.Name)...)
		// workflow XOR run: exactly one must be set.
		errors = append(errors, validateWorkflowRunXOR(fmt.Sprintf("builds[%d]", i), b.Workflow, b.Run, b.Shell)...)
		errors = append(errors, validateLocalCallbackWorkflowPath(fmt.Sprintf("builds[%d]", i), b.Workflow)...)

		// Reusable-workflow callbacks cannot carry job-control fields that GHA
		// rejects on a jobs.<id>.uses call. matrix: is builds-only.
		isReusable := b.Workflow != ""
		errors = append(errors, validateJobControlFields(fmt.Sprintf("builds[%d]", i), isReusable, b.RunsOn, b.Concurrency)...)
		errors = append(errors, validateCallbackTimeout(fmt.Sprintf("builds[%d]", i), isReusable, b.TimeoutMinutes)...)
		errors = append(errors, validatePermissions(fmt.Sprintf("builds[%d]", i), b.Permissions)...)
		errors = append(errors, validateSecrets(fmt.Sprintf("builds[%d]", i), b.Secrets)...)

		// Validate run_policy
		if b.RunPolicy != "" && b.RunPolicy != RunPolicyDefault && b.RunPolicy != RunPolicyAlways && b.RunPolicy != RunPolicyForce {
			errors = append(errors, fmt.Sprintf("builds[%d].run_policy must be one of: default, always, force", i))
		}

		// Validate on_failure
		if b.OnFailure != "" && b.OnFailure != OnFailureAbort && b.OnFailure != OnFailureContinue {
			errors = append(errors, fmt.Sprintf("builds[%d].on_failure must be one of: abort, continue", i))
		}

		// Validate retries
		if b.Retries < 0 || b.Retries > 3 {
			errors = append(errors, fmt.Sprintf("builds[%d].retries must be between 0 and 3", i))
		}

		// Validate depends_on references using ResolveDependency
		for _, dep := range b.DependsOn {
			if _, err := cfg.ResolveDependency(dep, CallbackTypeBuild); err != nil {
				errors = append(errors, fmt.Sprintf("builds[%d].depends_on: %s", i, err.Error()))
			}
		}

		// optional_depends_on resolves exactly like depends_on (ordering-only).
		for _, dep := range b.OptionalDependsOn {
			if _, err := cfg.ResolveDependency(dep, CallbackTypeBuild); err != nil {
				errors = append(errors, fmt.Sprintf("builds[%d].optional_depends_on: %s", i, err.Error()))
			}
		}

		// Validate env_inputs keys match top-level environments
		for envKey := range b.EnvInputs {
			if !envSet[envKey] {
				errors = append(errors, fmt.Sprintf("builds[%d].env_inputs has key '%s' which is not in environments %v", i, envKey, cfg.Environments))
			}
		}
	}

	// Validate deploys
	for i, d := range cfg.Deploys {
		if d.Name == "" {
			errors = append(errors, fmt.Sprintf("deploys[%d].name is required", i))
		} else if deployNames[d.Name] {
			errors = append(errors, fmt.Sprintf("duplicate deploy name: %s", d.Name))
		} else {
			deployNames[d.Name] = true
		}
		// The name becomes part of the job ID (deploy-<name>); enforce the job-ID
		// grammar so generation cannot emit invalid YAML.
		errors = append(errors, validateJobIDSafeName(fmt.Sprintf("deploys[%d].name", i), d.Name)...)
		// workflow XOR run: exactly one must be set.
		errors = append(errors, validateWorkflowRunXOR(fmt.Sprintf("deploys[%d]", i), d.Workflow, d.Run, d.Shell)...)
		errors = append(errors, validateLocalCallbackWorkflowPath(fmt.Sprintf("deploys[%d]", i), d.Workflow)...)

		// Reusable-workflow callbacks cannot carry job-control fields that GHA
		// rejects on a jobs.<id>.uses call. rollout: is deploys-only.
		isReusable := d.Workflow != ""
		errors = append(errors, validateJobControlFields(fmt.Sprintf("deploys[%d]", i), isReusable, d.RunsOn, d.Concurrency)...)
		errors = append(errors, validateCallbackTimeout(fmt.Sprintf("deploys[%d]", i), isReusable, d.TimeoutMinutes)...)
		errors = append(errors, validatePermissions(fmt.Sprintf("deploys[%d]", i), d.Permissions)...)
		errors = append(errors, validateSecrets(fmt.Sprintf("deploys[%d]", i), d.Secrets)...)
		errors = append(errors, validateRollout(fmt.Sprintf("deploys[%d]", i), d.Rollout, cfg.Environments)...)
		errors = append(errors, validateDeployTarget(fmt.Sprintf("deploys[%d]", i), d.DeployTarget)...)

		// Validate run_policy
		if d.RunPolicy != "" && d.RunPolicy != RunPolicyDefault && d.RunPolicy != RunPolicyAlways && d.RunPolicy != RunPolicyForce {
			errors = append(errors, fmt.Sprintf("deploys[%d].run_policy must be one of: default, always, force", i))
		}

		// Validate on_failure
		if d.OnFailure != "" && d.OnFailure != OnFailureAbort && d.OnFailure != OnFailureContinue {
			errors = append(errors, fmt.Sprintf("deploys[%d].on_failure must be one of: abort, continue", i))
		}

		// Validate retries
		if d.Retries < 0 || d.Retries > 3 {
			errors = append(errors, fmt.Sprintf("deploys[%d].retries must be between 0 and 3", i))
		}

		// Validate depends_on references using ResolveDependency
		// Deploys can depend on builds (preferred) or other deploys
		for _, dep := range d.DependsOn {
			if _, err := cfg.ResolveDependency(dep, CallbackTypeDeploy); err != nil {
				errors = append(errors, fmt.Sprintf("deploys[%d].depends_on: %s", i, err.Error()))
			}
		}

		// optional_depends_on resolves exactly like depends_on (ordering-only).
		for _, dep := range d.OptionalDependsOn {
			if _, err := cfg.ResolveDependency(dep, CallbackTypeDeploy); err != nil {
				errors = append(errors, fmt.Sprintf("deploys[%d].optional_depends_on: %s", i, err.Error()))
			}
		}

		// Validate env_inputs keys match top-level environments
		for envKey := range d.EnvInputs {
			if !envSet[envKey] {
				errors = append(errors, fmt.Sprintf("deploys[%d].env_inputs has key '%s' which is not in environments %v", i, envKey, cfg.Environments))
			}
		}
	}

	// Validate the validate callback structural rules.
	if cfg.Validate != nil {
		v := cfg.Validate
		errors = append(errors, validateWorkflowRunXOR("validate", v.Workflow, v.Run, v.Shell)...)
		errors = append(errors, validateLocalCallbackWorkflowPath("validate", v.Workflow)...)
		isReusable := v.Workflow != ""
		errors = append(errors, validateJobControlFields("validate", isReusable, v.RunsOn, v.Concurrency)...)
		errors = append(errors, validateCallbackTimeout("validate", isReusable, v.TimeoutMinutes)...)
		errors = append(errors, validatePermissions("validate", v.Permissions)...)
		errors = append(errors, validateSecrets("validate", v.Secrets)...)
	}

	// Config-level structural validation for v1 reserved fields.
	errors = append(errors, validateConfigLevel(cfg)...)
	errors = append(errors, validateComponents(cfg)...)
	errors = append(errors, validateVersionOverrides(cfg.Release)...)

	// Validate release.tag reference
	if cfg.Release != nil && cfg.Release.Tag != "" {
		parts := strings.SplitN(cfg.Release.Tag, ".", 2)
		if len(parts) != 2 {
			errors = append(errors, "release.tag invalid format (expected callback.output, e.g., goreleaser.tag)")
		} else {
			callbackName := parts[0]
			// Check where callback exists
			inBuilds := buildNames[callbackName]
			inDeploys := deployNames[callbackName]
			inValidate := callbackName == "validate" && cfg.Validate != nil

			if !inBuilds && !inDeploys && !inValidate {
				errors = append(errors, fmt.Sprintf("release.tag references unknown callback: %s", callbackName))
			} else if (inBuilds && inDeploys) || (inBuilds && inValidate) || (inDeploys && inValidate) {
				// Conflict - multiple callbacks with same name
				var locations []string
				if inBuilds {
					locations = append(locations, "build:"+callbackName)
				}
				if inDeploys {
					locations = append(locations, "deploy:"+callbackName)
				}
				if inValidate {
					locations = append(locations, "validate:"+callbackName)
				}
				errors = append(errors, fmt.Sprintf("release.tag '%s' is ambiguous: exists as %s. Use explicit prefix (e.g., build:%s.output)",
					callbackName, strings.Join(locations, ", "), callbackName))
			}
		}
	}

	// Validate external repos (for primary repos)
	externalDeployNames := make(map[string]bool)
	for i, ext := range cfg.External {
		if ext.Repo == "" {
			errors = append(errors, fmt.Sprintf("external[%d].repo is required", i))
		}

		for j, d := range ext.Deploys {
			if d.Name == "" {
				errors = append(errors, fmt.Sprintf("external[%d].deploys[%d].name is required", i, j))
			} else {
				// Check for duplicate external deploy names
				if externalDeployNames[d.Name] {
					errors = append(errors, fmt.Sprintf("duplicate external deploy name: %s", d.Name))
				} else {
					externalDeployNames[d.Name] = true
				}
				// Check for conflicts with local deploy names
				if deployNames[d.Name] {
					errors = append(errors, fmt.Sprintf("external deploy name '%s' conflicts with local deploy name", d.Name))
				}
			}
			// External deploys also key job IDs (deploy-<name>); enforce the grammar.
			errors = append(errors, validateJobIDSafeName(fmt.Sprintf("external[%d].deploys[%d].name", i, j), d.Name)...)
			prefix := fmt.Sprintf("external[%d].deploys[%d]", i, j)
			errors = append(errors, validateExternalDeployWorkflowOnly(prefix, d.Workflow, d.Run, d.Shell)...)
			// on_update.deploy is opt-in and reusable-workflow only, mirroring the
			// workflow-only policy enforced above. When the block is present its
			// workflow path is required.
			errors = append(errors, validateOnUpdate(prefix, d.OnUpdate)...)
			// External deploys are always reusable-workflow callbacks.
			errors = append(errors, validateJobControlFields(prefix, true, d.RunsOn, d.Concurrency)...)
			errors = append(errors, validatePermissions(prefix, d.Permissions)...)
			errors = append(errors, validateSecrets(prefix, d.Secrets)...)
			errors = append(errors, validateRollout(prefix, d.Rollout, cfg.Environments)...)
			errors = append(errors, validateDeployTarget(prefix, d.DeployTarget)...)
			for _, dep := range d.OptionalDependsOn {
				if _, err := cfg.ResolveDependency(dep, CallbackTypeExternal); err != nil {
					errors = append(errors, fmt.Sprintf("%s.optional_depends_on: %s", prefix, err.Error()))
				}
			}
		}
	}

	// Validate notify config (for satellite repos)
	if cfg.Notify != nil {
		if cfg.Notify.Repo == "" {
			errors = append(errors, "notify.repo is required")
		}
	}

	// Check for mutually exclusive: can't be both primary and satellite
	if len(cfg.External) > 0 && cfg.Notify != nil {
		errors = append(errors, "cannot have both 'external' (primary) and 'notify' (satellite) config - a repo must be one or the other")
	}

	// Check for circular dependencies
	if cycleErr := detectCycles(cfg); cycleErr != "" {
		errors = append(errors, cycleErr)
	}

	return errors
}

// GetBuildNames returns a list of all build names
func GetBuildNames(cfg *TrunkConfig) []string {
	names := make([]string, len(cfg.Builds))
	for i, b := range cfg.Builds {
		names[i] = b.Name
	}
	return names
}

// GetDeployNames returns a list of all deploy names
func GetDeployNames(cfg *TrunkConfig) []string {
	names := make([]string, len(cfg.Deploys))
	for i, d := range cfg.Deploys {
		names[i] = d.Name
	}
	return names
}

// detectCycles checks for circular dependencies in the callback graph
// Uses prefixed job IDs (build-app, deploy-app) to handle name collisions
func detectCycles(cfg *TrunkConfig) string {
	// Build adjacency list using prefixed job IDs
	deps := make(map[string][]string)

	// resolveEdges resolves a callback's hard and optional dependencies into
	// prefixed job IDs. optional_depends_on participates in cycle detection
	// exactly like depends_on (it still adds a needs: edge for ordering).
	resolveEdges := func(hard, optional []string, fromType string) []string {
		var resolvedDeps []string
		for _, dep := range hard {
			if resolved, err := cfg.ResolveDependency(dep, fromType); err == nil {
				resolvedDeps = append(resolvedDeps, resolved)
			}
		}
		for _, dep := range optional {
			if resolved, err := cfg.ResolveDependency(dep, fromType); err == nil {
				resolvedDeps = append(resolvedDeps, resolved)
			}
		}
		return resolvedDeps
	}

	// Add builds with resolved dependencies
	for _, b := range cfg.Builds {
		jobID := JobID(CallbackTypeBuild, b.Name)
		deps[jobID] = resolveEdges(b.DependsOn, b.OptionalDependsOn, CallbackTypeBuild)
	}

	// Add deploys with resolved dependencies
	for _, d := range cfg.Deploys {
		jobID := JobID(CallbackTypeDeploy, d.Name)
		deps[jobID] = resolveEdges(d.DependsOn, d.OptionalDependsOn, CallbackTypeDeploy)
	}

	// DFS for cycle detection
	visited := make(map[string]int) // 0=unvisited, 1=visiting, 2=visited
	var path []string

	var dfs func(node string) string
	dfs = func(node string) string {
		if visited[node] == 1 {
			// Found cycle - build path string
			cycleStart := -1
			for i, n := range path {
				if n == node {
					cycleStart = i
					break
				}
			}
			cyclePath := append(path[cycleStart:], node)
			return fmt.Sprintf("circular dependency detected: %s", strings.Join(cyclePath, " -> "))
		}
		if visited[node] == 2 {
			return ""
		}

		visited[node] = 1
		path = append(path, node)

		for _, dep := range deps[node] {
			if result := dfs(dep); result != "" {
				return result
			}
		}

		path = path[:len(path)-1]
		visited[node] = 2
		return ""
	}

	for name := range deps {
		if visited[name] == 0 {
			if result := dfs(name); result != "" {
				return result
			}
		}
	}

	return ""
}

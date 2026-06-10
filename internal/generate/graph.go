package generate

import (
	"fmt"

	"github.com/stablekernel/cascade/internal/config"
)

// DependencyGraph represents the callback dependency graph
// Uses prefixed job IDs (build-app, deploy-app) to allow name reuse across sections
type DependencyGraph struct {
	Nodes map[string]CallbackInfo // job ID -> info
	Edges map[string][]string     // job ID -> dependencies (as job IDs)
}

// CallbackInfo holds information about a callback
type CallbackInfo struct {
	Name           string // Original name from config (e.g., "app")
	JobID          string // Prefixed job ID (e.g., "build-app")
	DisplayName    string // Display name (e.g., "Build (app)")
	Type           string // "build" or "deploy" or "validate"
	Workflow       string
	RunPolicy      string
	OnFailure      string
	Retries        int
	TimeoutMinutes int                  // Job-level timeout-minutes (omitted when 0)
	Matrix         *config.MatrixConfig // Build fan-out; nil for deploys and validate
}

// BuildDependencyGraph creates a dependency graph from config
// Uses prefixed job IDs to support same names in builds and deploys
func BuildDependencyGraph(cfg *config.TrunkConfig) *DependencyGraph {
	g := &DependencyGraph{
		Nodes: make(map[string]CallbackInfo),
		Edges: make(map[string][]string),
	}

	// Add validate if present
	if cfg.Validate != nil {
		jobID := "validate"
		g.Nodes[jobID] = CallbackInfo{
			Name:           "validate",
			JobID:          jobID,
			DisplayName:    "Validate (validate)",
			Type:           config.CallbackTypeValidate,
			Workflow:       cfg.Validate.Workflow,
			RunPolicy:      defaultString(cfg.Validate.RunPolicy, config.RunPolicyDefault),
			OnFailure:      defaultString(cfg.Validate.OnFailure, config.OnFailureAbort),
			Retries:        cfg.Validate.Retries,
			TimeoutMinutes: cfg.Validate.TimeoutMinutes,
		}
		g.Edges[jobID] = nil
	}

	// Add builds
	for _, b := range cfg.Builds {
		jobID := config.JobID(config.CallbackTypeBuild, b.Name)
		g.Nodes[jobID] = CallbackInfo{
			Name:           b.Name,
			JobID:          jobID,
			DisplayName:    config.DisplayName(config.CallbackTypeBuild, b.Name),
			Type:           config.CallbackTypeBuild,
			Workflow:       b.Workflow,
			RunPolicy:      defaultString(b.RunPolicy, config.RunPolicyDefault),
			OnFailure:      defaultString(b.OnFailure, config.OnFailureAbort),
			Retries:        b.Retries,
			TimeoutMinutes: b.TimeoutMinutes,
			Matrix:         b.Matrix,
		}

		// Resolve dependencies to job IDs
		var deps []string
		for _, dep := range b.DependsOn {
			if resolved, err := cfg.ResolveDependency(dep, config.CallbackTypeBuild); err == nil {
				deps = append(deps, resolved)
			}
		}
		// All builds depend on validate if it exists
		if cfg.Validate != nil {
			deps = ensureValidateDependency(deps)
		}
		g.Edges[jobID] = deps
	}

	// Add deploys
	for _, d := range cfg.Deploys {
		jobID := config.JobID(config.CallbackTypeDeploy, d.Name)
		g.Nodes[jobID] = CallbackInfo{
			Name:           d.Name,
			JobID:          jobID,
			DisplayName:    config.DisplayName(config.CallbackTypeDeploy, d.Name),
			Type:           config.CallbackTypeDeploy,
			Workflow:       d.Workflow,
			RunPolicy:      defaultString(d.RunPolicy, config.RunPolicyDefault),
			OnFailure:      defaultString(d.OnFailure, config.OnFailureAbort),
			Retries:        d.Retries,
			TimeoutMinutes: d.TimeoutMinutes,
		}

		// Resolve dependencies to job IDs
		var deps []string
		for _, dep := range d.DependsOn {
			if resolved, err := cfg.ResolveDependency(dep, config.CallbackTypeDeploy); err == nil {
				deps = append(deps, resolved)
			}
		}
		// All deploys depend on validate if it exists
		if cfg.Validate != nil {
			deps = ensureValidateDependency(deps)
		}
		g.Edges[jobID] = deps
	}

	return g
}

// TopologicalSort returns nodes in dependency order
func (g *DependencyGraph) TopologicalSort() ([]string, error) {
	visited := make(map[string]bool)
	inStack := make(map[string]bool)
	var result []string

	var visit func(node string) error
	visit = func(node string) error {
		if inStack[node] {
			return fmt.Errorf("cycle detected at node: %s", node)
		}
		if visited[node] {
			return nil
		}

		inStack[node] = true
		for _, dep := range g.Edges[node] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		inStack[node] = false
		visited[node] = true
		result = append(result, node)
		return nil
	}

	for node := range g.Nodes {
		if err := visit(node); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// GetAllDependencies returns all transitive dependencies for a node
func (g *DependencyGraph) GetAllDependencies(node string) []string {
	visited := make(map[string]bool)
	var deps []string

	var collect func(n string)
	collect = func(n string) {
		for _, dep := range g.Edges[n] {
			if !visited[dep] {
				visited[dep] = true
				deps = append(deps, dep)
				collect(dep)
			}
		}
	}

	collect(node)
	return deps
}

// GetDirectDependencies returns only direct dependencies for a node
func (g *DependencyGraph) GetDirectDependencies(node string) []string {
	return g.Edges[node]
}

func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ensureValidateDependency adds "validate" to deps if not already present
func ensureValidateDependency(deps []string) []string {
	for _, d := range deps {
		if d == "validate" {
			return deps
		}
	}
	// Prepend validate so it runs first
	return append([]string{"validate"}, deps...)
}

package generate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// DependencyGraph represents the callback dependency graph
// Uses prefixed job IDs (build-app, deploy-app) to allow name reuse across sections
type DependencyGraph struct {
	Nodes map[string]CallbackInfo // job ID -> info
	Edges map[string][]string     // job ID -> hard dependencies (as job IDs)

	// OptionalEdges holds optional_depends_on edges (job ID -> dependencies as
	// job IDs). Optional deps add to a job's needs: for ordering but do NOT
	// contribute a skip-gate to its if: condition. The job still runs when an
	// optional dep was skipped because its triggers didn't match (#18).
	OptionalEdges map[string][]string

	// Order lists every job ID in manifest declaration order (validate, then
	// builds, then deploys, each in the order they appear in config). It is the
	// deterministic seed for TopologicalSort: iterating Nodes (a map) directly
	// would randomize emitted job order, needs: lists, and if: conditions across
	// runs because Go randomizes map range order per process.
	Order []string
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
	Matrix         *config.MatrixConfig // Build fan-out; nil for deploys and validate
	SupportsDryRun bool                 // When true, dry-run promotes invoke the callback with dry_run: true instead of skipping it

	// Per-callback job attributes carried from config. GHA forbids
	// runs-on/concurrency/timeout-minutes on a reusable-workflow uses: caller
	// job, and schema validation rejects runs_on/concurrency/timeout_minutes on
	// reusable callbacks, so those fields are populated from config but never
	// emitted as job-level keys. permissions: is the exception: GitHub allows it
	// on a uses: caller job, so Permissions IS rendered as a job-level
	// permissions: block (see writeCallbackPermissions).
	TimeoutMinutes int                       // Per-callback timeout-minutes; validated-against, never emitted (belongs inside the called workflow)
	RunsOn      *config.RunsOn            // Per-callback runner selection (#12); validated-against, never emitted
	Permissions map[string]string         // Per-callback job permissions, incl. id-token: write OIDC (#35, #15); emitted as job-level permissions:
	Concurrency *config.ConcurrencyConfig // Per-callback concurrency override (#17); validated-against, never emitted

	// PassthroughArtifact declares GHA artifact upload/download steps to inject
	// around this job's callback invocation, enabling inter-job artifact passing
	// within a single orchestrate run (#16).
	PassthroughArtifact *config.PassthroughArtifact

	// Secrets holds the per-callback secrets passing config. Nil means no secrets
	// block is emitted at all (the default; secrets: inherit is opt-in). When
	// non-nil and Inherit is true, secrets: inherit is emitted. When non-nil with
	// an explicit Map, a secrets: block with per-entry ${{ secrets.CALLER_NAME }}
	// expressions is emitted instead.
	Secrets *config.SecretsConfig
}

// BuildDependencyGraph creates a dependency graph from config
// Uses prefixed job IDs to support same names in builds and deploys
func BuildDependencyGraph(cfg *config.TrunkConfig) *DependencyGraph {
	g := &DependencyGraph{
		Nodes:         make(map[string]CallbackInfo),
		Edges:         make(map[string][]string),
		OptionalEdges: make(map[string][]string),
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
			RunsOn:         cfg.Validate.RunsOn,
			Permissions:    cfg.Validate.Permissions,
			Concurrency:    cfg.Validate.Concurrency,
			SupportsDryRun: cfg.Validate.SupportsDryRun,
			Secrets:        cfg.Validate.Secrets,
		}
		g.Edges[jobID] = nil
		g.Order = append(g.Order, jobID)
	}

	// Add builds
	for _, b := range cfg.Builds {
		jobID := config.JobID(config.CallbackTypeBuild, b.Name)
		g.Nodes[jobID] = CallbackInfo{
			Name:                b.Name,
			JobID:               jobID,
			DisplayName:         config.DisplayName(config.CallbackTypeBuild, b.Name),
			Type:                config.CallbackTypeBuild,
			Workflow:            b.Workflow,
			RunPolicy:           defaultString(b.RunPolicy, config.RunPolicyDefault),
			OnFailure:           defaultString(b.OnFailure, config.OnFailureAbort),
			Retries:             b.Retries,
			TimeoutMinutes:      b.TimeoutMinutes,
			Matrix:              b.Matrix,
			RunsOn:              b.RunsOn,
			Permissions:         b.Permissions,
			Concurrency:         b.Concurrency,
			PassthroughArtifact: b.PassthroughArtifact,
			Secrets:             b.Secrets,
		}
		g.Order = append(g.Order, jobID)

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

		// Optional dependencies: ordering-only edges (sequence after, no skip-gate).
		var optDeps []string
		for _, dep := range b.OptionalDependsOn {
			if resolved, err := cfg.ResolveDependency(dep, config.CallbackTypeBuild); err == nil {
				optDeps = append(optDeps, resolved)
			}
		}
		g.OptionalEdges[jobID] = optDeps
	}

	// Add deploys
	for _, d := range cfg.Deploys {
		jobID := config.JobID(config.CallbackTypeDeploy, d.Name)
		g.Nodes[jobID] = CallbackInfo{
			Name:                d.Name,
			JobID:               jobID,
			DisplayName:         config.DisplayName(config.CallbackTypeDeploy, d.Name),
			Type:                config.CallbackTypeDeploy,
			Workflow:            d.Workflow,
			RunPolicy:           defaultString(d.RunPolicy, config.RunPolicyDefault),
			OnFailure:           defaultString(d.OnFailure, config.OnFailureAbort),
			Retries:             d.Retries,
			TimeoutMinutes:      d.TimeoutMinutes,
			RunsOn:              d.RunsOn,
			Permissions:         d.Permissions,
			Concurrency:         d.Concurrency,
			PassthroughArtifact: d.PassthroughArtifact,
			SupportsDryRun:      d.SupportsDryRun,
			Secrets:             d.Secrets,
		}
		g.Order = append(g.Order, jobID)

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

		// Optional dependencies: ordering-only edges (sequence after, no skip-gate).
		var optDeps []string
		for _, dep := range d.OptionalDependsOn {
			if resolved, err := cfg.ResolveDependency(dep, config.CallbackTypeDeploy); err == nil {
				optDeps = append(optDeps, resolved)
			}
		}
		g.OptionalEdges[jobID] = optDeps
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

	// Seed the walk in manifest declaration order (Order), not by ranging
	// g.Nodes: a map range is randomized per process, which would shuffle the
	// emitted job order, needs: lists, and if: conditions run to run. With a
	// stable seed the result is a deterministic topological order that follows
	// declaration order wherever the dependency DAG leaves it free.
	for _, node := range g.Order {
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

// GetDirectDependencies returns only direct (hard) dependencies for a node
func (g *DependencyGraph) GetDirectDependencies(node string) []string {
	return g.Edges[node]
}

// GetOptionalDependencies returns the optional_depends_on dependencies for a
// node. These add to needs: for ordering only; they do not skip-gate the job.
func (g *DependencyGraph) GetOptionalDependencies(node string) []string {
	return g.OptionalEdges[node]
}

func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// collectCallbackPermissions unions the per-callback permissions of every
// callback in the manifest (validate, builds, deploys). A reusable-workflow
// caller job cannot carry job-level permissions, so any scope a callback needs
// (notably id-token: write for OIDC) must be granted by the calling workflow's
// top-level permissions block instead. The result is that union keyed by scope.
//
// Precedence when two callbacks set the same scope to different values: the
// lexicographically greatest value wins. This keeps "write" ahead of "read"
// ahead of "none", matching the monotonic GHA permission order (write subsumes
// read), and is independent of map iteration order so the union is fully
// deterministic. In practice callbacks agree on a scope's value, so this only
// matters for the read/write distinction.
func collectCallbackPermissions(cfg *config.TrunkConfig) map[string]string {
	graph := BuildDependencyGraph(cfg)
	union := make(map[string]string)
	for _, node := range graph.Nodes {
		for scope, value := range node.Permissions {
			if existing, ok := union[scope]; ok && existing >= value {
				continue
			}
			union[scope] = value
		}
	}
	return union
}

// writeTopLevelPermissions emits a top-level permissions: block. The base scopes
// are written first in their given order (preserving each generator's historical
// output), then any callback-union scope not already present in base is appended
// in alphabetical order for deterministic output. When a scope appears in both
// base and the callback union, the more-permissive value wins ("write" over a
// non-write value), so base scopes can be promoted to write by a callback that
// requires it. A trailing blank line is emitted to match the prior blocks.
//
// When the callback union is empty and contributes nothing, the output is
// byte-identical to the historical hardcoded base block.
func writeTopLevelPermissions(sb *strings.Builder, base [][2]string, callbackUnion map[string]string) {
	sb.WriteString("permissions:\n")

	inBase := make(map[string]bool, len(base))
	for _, kv := range base {
		scope, value := kv[0], kv[1]
		inBase[scope] = true
		// Promote the base scope if a callback requires a more-permissive value
		// (e.g. write over read), using the same lexicographic order as the union.
		if cb, ok := callbackUnion[scope]; ok && cb > value {
			value = cb
		}
		fmt.Fprintf(sb, "  %s: %s\n", scope, value)
	}

	// Append callback-only scopes (not in base) in deterministic sorted order.
	extra := make([]string, 0, len(callbackUnion))
	for scope := range callbackUnion {
		if !inBase[scope] {
			extra = append(extra, scope)
		}
	}
	sort.Strings(extra)
	for _, scope := range extra {
		fmt.Fprintf(sb, "  %s: %s\n", scope, callbackUnion[scope])
	}

	sb.WriteString("\n")
}

// writeCallbackPermissions emits a job-level permissions: block for a
// reusable-workflow caller job (jobs.<id>.uses) from the callback's configured
// permissions map. GitHub Actions allows permissions: on a uses: caller job, so
// rendering it scopes the GITHUB_TOKEN to least privilege per callback and
// enables OIDC (id-token: write) and build provenance (attestations: write) for
// exactly the callbacks that need them. When perms is empty nothing is emitted,
// so callbacks without an explicit permissions: stanza are byte-identical to the
// prior output. Scopes are emitted in sorted order for deterministic output
// (cascade gates generation on determinism).
//
// indent is the two-space job-body indent for the job whose block this is (e.g.
// "    " for orchestrate/promote jobs).
func writeCallbackPermissions(sb *strings.Builder, indent string, perms map[string]string) {
	if len(perms) == 0 {
		return
	}
	scopes := make([]string, 0, len(perms))
	for scope := range perms {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	fmt.Fprintf(sb, "%spermissions:\n", indent)
	for _, scope := range scopes {
		fmt.Fprintf(sb, "%s  %s: %s\n", indent, scope, perms[scope])
	}
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

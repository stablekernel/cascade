package simulate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stablekernel/cascade/internal/config"
)

// boundaryNote states the orchestration-not-deploys boundary in the simulation
// output. The simulator validates the run, skip, and gate decisions, not the
// user's real build and deploy scripts, which never execute in a what-if.
const boundaryNote = "Note: build and deploy results are simulated, not executed. cascade validates orchestration, not your build and deploy scripts."

// Result is the outcome of one simulation: the action identity, the before and
// after state diff, and the ordered effect sequence.
type Result struct {
	// ActionName is the short identifier of the simulated action.
	ActionName string `json:"action"`

	// ActionDescribe is the one-line human-readable summary of the action.
	ActionDescribe string `json:"describe"`

	// Diff is the before and after state difference.
	Diff StateDiff `json:"diff"`

	// Effects is the ordered list of orchestration steps.
	Effects []Effect `json:"effects"`

	// Chain is the ordered multi-environment cherry-pick preview: one step per
	// environment the fix elevates through, bottom-up from the second environment
	// up to and including the target. It is populated only for a hotfix; other
	// actions leave it empty.
	Chain []Effect `json:"chain,omitempty"`

	// Note states the orchestration-not-deploys boundary so a green simulation
	// is never read as a passing deploy.
	Note string `json:"note,omitempty"`
}

// Engine runs hypothetical actions against a clone of the user's manifest and
// reports the resulting state diff and effect sequence. It never mutates the
// user's real manifest and never touches git or the network.
type Engine struct {
	manifestPath   string
	actor          string
	component      string
	deployOutcomes map[string]DeployOutcome
}

// Option configures an Engine.
type Option func(*Engine)

// WithActor sets the actor identity used when replaying an action.
func WithActor(actor string) Option {
	return func(e *Engine) {
		if actor != "" {
			e.actor = actor
		}
	}
}

// WithComponent scopes the simulation to a declared component, reading and
// writing state under state.components.<name>.<env> exactly as the real
// component-scoped commands do. An empty name (the default) keeps the flat
// single-component path byte-identical.
func WithComponent(name string) Option {
	return func(e *Engine) {
		if name != "" {
			e.component = name
		}
	}
}

// WithDeployResults injects per-callback simulated outcomes keyed by build or
// deploy name. Callbacks not named here default to success. Use it to preview
// the orchestration's gating behavior, for example a deploy failure holding back
// finalize. Real build and deploy scripts never run regardless of the outcome.
func WithDeployResults(outcomes map[string]DeployOutcome) Option {
	return func(e *Engine) {
		if len(outcomes) == 0 {
			return
		}
		if e.deployOutcomes == nil {
			e.deployOutcomes = make(map[string]DeployOutcome, len(outcomes))
		}
		for name, outcome := range outcomes {
			e.deployOutcomes[name] = outcome
		}
	}
}

// NewEngine builds an Engine bound to the manifest at manifestPath. The path is
// validated by reading it; the file is never modified.
func NewEngine(manifestPath string, opts ...Option) (*Engine, error) {
	if manifestPath == "" {
		return nil, fmt.Errorf("manifest path is required")
	}
	if _, err := os.Stat(manifestPath); err != nil {
		return nil, fmt.Errorf("manifest not readable: %w", err)
	}

	e := &Engine{manifestPath: manifestPath}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// Simulate replays the action against a temp clone of the manifest and returns
// the before and after diff plus the ordered effects. The clone is created in a
// temp directory and removed when Simulate returns.
func (e *Engine) Simulate(a Action) (*Result, error) {
	beforeState, err := parseState(e.manifestPath, e.component)
	if err != nil {
		return nil, fmt.Errorf("parse before-state: %w", err)
	}
	// Snapshot the before-state by value so the action cannot alias it through
	// a shared pointer when it rewrites the clone.
	beforeState = cloneStateMap(beforeState)

	clonePath, cleanup, err := e.cloneManifest()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	builds, deploys, err := parseCallbackNames(e.manifestPath)
	if err != nil {
		return nil, fmt.Errorf("parse callbacks: %w", err)
	}
	stub := newDeployStub(builds, deploys, e.deployOutcomes)

	outcome, err := a.Apply(ActionContext{ClonePath: clonePath, Actor: e.actor, Component: e.component, Deploys: stub})
	if err != nil {
		return nil, fmt.Errorf("apply action: %w", err)
	}

	afterPath := outcome.AfterStatePath
	if afterPath == "" {
		afterPath = clonePath
	}
	afterState, err := parseState(afterPath, e.component)
	if err != nil {
		return nil, fmt.Errorf("parse after-state: %w", err)
	}

	return &Result{
		ActionName:     a.Name(),
		ActionDescribe: a.Describe(),
		Diff:           DiffState(beforeState, afterState),
		Effects:        outcome.Effects,
		Chain:          outcome.Chain,
		Note:           boundaryNote,
	}, nil
}

// cloneManifest copies the manifest bytes into a fresh temp file and returns its
// path plus a cleanup func that removes the temp directory.
func (e *Engine) cloneManifest() (string, func(), error) {
	data, err := os.ReadFile(e.manifestPath)
	if err != nil {
		return "", nil, fmt.Errorf("read manifest: %w", err)
	}

	dir, err := os.MkdirTemp("", "cascade-simulate-")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	clonePath := filepath.Join(dir, filepath.Base(e.manifestPath))
	if err := os.WriteFile(clonePath, data, 0o644); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write clone: %w", err)
	}
	return clonePath, cleanup, nil
}

// parseState reads a manifest file and returns its environment state map. When
// component is non-empty the state is read component-scoped from
// state.components.<component>.<env> via config.ReadComponentState, the same
// helper the real component-scoped commands (promote, rollback, hotfix) use, so
// the simulator replays those commands' state rather than the empty flat map a
// monorepo manifest carries at the top level. An empty component keeps the flat
// state.<env> path unchanged.
func parseState(path, component string) (map[string]*config.EnvState, error) {
	if component != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read manifest for component state: %w", err)
		}
		compState, err := config.ReadComponentState(raw, config.DefaultManifestKey, component)
		if err != nil {
			return nil, err
		}
		if compState == nil {
			return map[string]*config.EnvState{}, nil
		}
		return compState, nil
	}

	cicd, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		return nil, err
	}
	if cicd.State == nil {
		return map[string]*config.EnvState{}, nil
	}
	return cicd.State, nil
}

// parseCallbackNames reads a manifest file and returns the configured build and
// deploy callback names in declaration order. A manifest with no config or no
// callbacks yields empty slices, which leaves the deploy-stub model recording
// nothing.
func parseCallbackNames(path string) (builds, deploys []string, err error) {
	cicd, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		return nil, nil, err
	}
	if cicd.Config == nil {
		return nil, nil, nil
	}
	for _, b := range cicd.Config.Builds {
		builds = append(builds, b.Name)
	}
	for _, d := range cicd.Config.Deploys {
		deploys = append(deploys, d.Name)
	}
	return builds, deploys, nil
}

// cloneStateMap returns a deep value copy of the environment state map so a
// later action cannot mutate the captured before-state through a shared
// pointer.
func cloneStateMap(in map[string]*config.EnvState) map[string]*config.EnvState {
	out := make(map[string]*config.EnvState, len(in))
	for name, s := range in {
		out[name] = cloneEnvState(s)
	}
	return out
}

func cloneEnvState(s *config.EnvState) *config.EnvState {
	if s == nil {
		return nil
	}
	c := *s

	if s.Deploys != nil {
		c.Deploys = make(map[string]*config.DeployState, len(s.Deploys))
		for k, d := range s.Deploys {
			if d == nil {
				c.Deploys[k] = nil
				continue
			}
			dc := *d
			c.Deploys[k] = &dc
		}
	}
	if s.Patches != nil {
		c.Patches = append([]string(nil), s.Patches...)
	}
	if s.Previous != nil {
		c.Previous = append([]config.EnvStateSnapshot(nil), s.Previous...)
	}
	return &c
}

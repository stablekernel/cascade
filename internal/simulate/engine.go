package simulate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stablekernel/cascade/internal/config"
)

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
}

// Engine runs hypothetical actions against a clone of the user's manifest and
// reports the resulting state diff and effect sequence. It never mutates the
// user's real manifest and never touches git or the network.
type Engine struct {
	manifestPath string
	actor        string
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
	beforeState, err := parseState(e.manifestPath)
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

	outcome, err := a.Apply(ActionContext{ClonePath: clonePath, Actor: e.actor})
	if err != nil {
		return nil, fmt.Errorf("apply action: %w", err)
	}

	afterPath := outcome.AfterStatePath
	if afterPath == "" {
		afterPath = clonePath
	}
	afterState, err := parseState(afterPath)
	if err != nil {
		return nil, fmt.Errorf("parse after-state: %w", err)
	}

	return &Result{
		ActionName:     a.Name(),
		ActionDescribe: a.Describe(),
		Diff:           DiffState(beforeState, afterState),
		Effects:        outcome.Effects,
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

// parseState reads a manifest file and returns its environment state map.
func parseState(path string) (map[string]*config.EnvState, error) {
	cicd, err := config.ParseManifestFile(path, config.DefaultManifestKey)
	if err != nil {
		return nil, err
	}
	if cicd.State == nil {
		return map[string]*config.EnvState{}, nil
	}
	return cicd.State, nil
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

package rollback

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"gopkg.in/yaml.v3"
)

// gitHistoryReader resolves prior environment states from the git history of
// the manifest file. It is the default HistoryReader and underpins the locked
// git/state-history rollback approach: when the live manifest has advanced
// past a deployment, the prior SHA/version is still recoverable from the
// commits that recorded it.
type gitHistoryReader struct {
	configPath  string
	manifestKey string
}

func newGitHistoryReader(configPath, manifestKey string) *gitHistoryReader {
	return &gitHistoryReader{configPath: configPath, manifestKey: manifestKey}
}

// PriorStates returns historical EnvState snapshots for env from the manifest's
// git history, newest first, excluding the current working-tree state. Commits
// that don't parse or don't record the env are skipped. A missing git history
// (or a non-repo) yields an empty slice, not an error, so callers degrade to
// state-only resolution gracefully.
func (g *gitHistoryReader) PriorStates(env string) ([]*config.EnvState, error) {
	repoDir := filepath.Dir(g.configPath)
	relPath := filepath.Base(g.configPath)

	// List commits that touched the manifest, newest first.
	logCmd := exec.Command("git", "-C", repoDir, "log", "--format=%H", "--", relPath)
	out, err := logCmd.Output()
	if err != nil {
		// Not a git repo, or git unavailable; degrade to no history.
		return nil, nil
	}

	commits := strings.Fields(strings.TrimSpace(string(out)))
	var states []*config.EnvState
	seen := make(map[string]bool) // dedupe identical sha|version snapshots

	for _, sha := range commits {
		showCmd := exec.Command("git", "-C", repoDir, "show", sha+":"+relPath)
		blob, err := showCmd.Output()
		if err != nil {
			continue // file may not exist at that revision
		}
		state := extractEnvState(blob, g.manifestKey, env)
		if state == nil {
			continue
		}
		dedupeKey := state.SHA + "|" + state.Version
		if seen[dedupeKey] {
			continue
		}
		seen[dedupeKey] = true
		states = append(states, state)
	}

	return states, nil
}

// extractEnvState parses a manifest blob and returns the EnvState for env, or
// nil when the manifest can't be parsed or the env isn't recorded. The manifest
// key is honoured so wrapped (ci:) manifests parse correctly.
func extractEnvState(blob []byte, manifestKey, env string) *config.EnvState {
	key := manifestKey
	if key == "" {
		key = config.DefaultManifestKey
	}

	var wrapper map[string]struct {
		State map[string]*config.EnvState `yaml:"state"`
	}
	if err := yaml.Unmarshal(blob, &wrapper); err != nil {
		return nil
	}
	inner, ok := wrapper[key]
	if !ok {
		return nil
	}
	state, ok := inner.State[env]
	if !ok || state == nil {
		return nil
	}
	if state.SHA == "" && state.Version == "" && len(state.Deploys) == 0 {
		return nil
	}
	return state
}

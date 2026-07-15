// Package changes detects which files changed between two commits and,
// from the path rules in the manifest, which component builds and
// deploys those changes trigger.
package changes

import (
	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
)

// Detect determines which builds and deploys are triggered by changes
func Detect(cfg *config.TrunkConfig, baseSHA, headSHA string) (*DetectResult, error) {
	changedFiles, err := git.GetChangedFiles(baseSHA, headSHA)
	if err != nil {
		return nil, err
	}

	result := &DetectResult{
		TriggeredBuilds:  []string{},
		TriggeredDeploys: []string{},
		HasChanges:       len(changedFiles) > 0,
		ChangedFiles:     changedFiles,
	}

	if !result.HasChanges {
		return result, nil
	}

	// Check which builds are triggered
	for _, build := range cfg.Builds {
		if isTriggered(build.Triggers, changedFiles) {
			result.TriggeredBuilds = append(result.TriggeredBuilds, build.Name)
		}
	}

	// Check which deploys are triggered
	for _, deploy := range cfg.Deploys {
		if isTriggered(deploy.Triggers, changedFiles) {
			result.TriggeredDeploys = append(result.TriggeredDeploys, deploy.Name)
		}
	}

	return result, nil
}

// isTriggered reports whether any changed file is a trigger under the supplied
// pattern list. It delegates to config.MatchAnyTrigger so the CLI-side change
// detection honours negation ("!"-prefixed) patterns identically to the emitted
// GitHub Actions paths filter, which is generated verbatim from the same list.
// An empty pattern list means "always triggered".
func isTriggered(triggers []string, changedFiles []string) bool {
	return config.MatchAnyTrigger(triggers, changedFiles)
}

// matchGlob reports whether a single glob pattern matches a path. Negation is
// handled at the pattern-list level by config.MatchTrigger; this helper matches
// the bare glob and is retained for the existing unit coverage. A leading "!"
// is stripped before matching.
func matchGlob(pattern, path string) bool {
	return config.MatchGlobPattern(pattern, path)
}

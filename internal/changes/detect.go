package changes

import (
	"path/filepath"
	"strings"

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

// isTriggered checks if any changed file matches any trigger pattern
func isTriggered(triggers []string, changedFiles []string) bool {
	// No triggers means always triggered
	if len(triggers) == 0 {
		return true
	}

	for _, pattern := range triggers {
		for _, file := range changedFiles {
			if matchGlob(pattern, file) {
				return true
			}
		}
	}

	return false
}

// matchGlob matches a file path against a glob pattern
// Supports: * (single segment), ** (any segments), ? (single char)
func matchGlob(pattern, path string) bool {
	// Handle ** patterns by converting to a regex-like approach
	if strings.Contains(pattern, "**") {
		return matchDoublestar(pattern, path)
	}

	// For simple patterns, use filepath.Match
	matched, _ := filepath.Match(pattern, path)
	return matched
}

// matchDoublestar handles ** glob patterns
func matchDoublestar(pattern, path string) bool {
	// Split pattern and path into segments
	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")

	return matchParts(patternParts, pathParts)
}

func matchParts(pattern, path []string) bool {
	pi, ppi := 0, 0

	for pi < len(pattern) && ppi < len(path) {
		if pattern[pi] == "**" {
			// ** matches zero or more path segments
			if pi == len(pattern)-1 {
				// ** at end matches everything
				return true
			}

			// Try matching remaining pattern at each position
			for i := ppi; i <= len(path); i++ {
				if matchParts(pattern[pi+1:], path[i:]) {
					return true
				}
			}
			return false
		}

		// Match single segment
		matched, _ := filepath.Match(pattern[pi], path[ppi])
		if !matched {
			return false
		}

		pi++
		ppi++
	}

	// Check if pattern is exhausted
	for pi < len(pattern) {
		if pattern[pi] != "**" {
			return false
		}
		pi++
	}

	return ppi == len(path)
}

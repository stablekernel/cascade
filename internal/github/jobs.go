package github

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// JobsResponse represents the response from the GitHub Actions jobs API.
type JobsResponse struct {
	Jobs []Job `json:"jobs"`
}

// Job represents a single GitHub Actions job.
type Job struct {
	Name       string  `json:"name"`
	Conclusion *string `json:"conclusion"` // Nullable - null when job is in_progress/queued
}

// QueryJobResults queries GitHub Actions for job results from a specific workflow run.
// It uses the gh CLI to query the GitHub API and returns a map of deploy names to their conclusions.
//
// Parameters:
//   - repo: GitHub repository in the format "owner/name"
//   - runID: The GitHub Actions workflow run ID
//   - deployNames: List of deploy names to look for (e.g., ["infra", "app"])
//
// Returns a map of deploy name to conclusion ("success", "failure", "skipped", etc.)
func QueryJobResults(repo, runID string, deployNames []string) (map[string]string, error) {
	cmd := exec.Command("gh", jobsAPIArgs(repo, runID)...)

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh api failed: %w (stderr: %s)", err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("gh api failed: %w", err)
	}

	return ParseSlurpedJobResults(out, deployNames)
}

// jobsAPIArgs builds the gh argv for the jobs API query. The jobs endpoint
// returns an OBJECT per page, and --paginate alone concatenates those objects
// into invalid JSON once a run exceeds one page (100 jobs); --slurp wraps the
// pages in a JSON array so the output stays parseable at any job count.
func jobsAPIArgs(repo, runID string) []string {
	return []string{
		"api",
		fmt.Sprintf("repos/%s/actions/runs/%s/jobs", repo, runID),
		"--paginate",
		"--slurp",
	}
}

// ParseSlurpedJobResults parses `gh api --paginate --slurp` output for the
// jobs endpoint: a JSON array with one page object per element. Jobs from all
// pages are merged before matching, so deploys whose jobs land beyond the
// first page are still found.
func ParseSlurpedJobResults(data []byte, deployNames []string) (map[string]string, error) {
	var pages []JobsResponse
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("failed to parse slurped jobs response: %w", err)
	}

	var jobs []Job
	for _, page := range pages {
		jobs = append(jobs, page.Jobs...)
	}
	return matchJobResults(jobs, deployNames, "")
}

// ParseJobResults parses the GitHub Actions jobs API response and extracts deploy job conclusions.
// It handles both simple job names ("Deploy infra") and matrix job names ("Deploy infra (test)").
//
// For matrix jobs, it returns the first matching result. To get results for a specific environment
// in a matrix job, use ParseJobResultsForEnv instead.
//
// Jobs that don't exist in the response default to "skipped".
// Jobs with null conclusions (in_progress/queued) also default to "skipped".
func ParseJobResults(data []byte, deployNames []string) (map[string]string, error) {
	return ParseJobResultsForEnv(data, deployNames, "")
}

// ParseJobResultsForEnv parses job results with optional environment filtering for matrix jobs.
// When targetEnv is provided, it only matches jobs with that environment in parentheses.
// For example, targetEnv="uat" will match "Deploy infra (uat)" but not "Deploy infra (test)".
//
// When targetEnv is empty, it matches the first occurrence of each deploy name.
func ParseJobResultsForEnv(data []byte, deployNames []string, targetEnv string) (map[string]string, error) {
	var resp JobsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse jobs response: %w", err)
	}
	return matchJobResults(resp.Jobs, deployNames, targetEnv)
}

// matchJobResults maps each deploy name to the conclusion of its matching
// job. A deploy's job is named either "Deploy <name>" (simple) or
// "Deploy <name> (<env>)" (matrix); the boundary is exact so
// prefix-overlapping deploy names (app / app-canary) never cross-match.
func matchJobResults(jobs []Job, deployNames []string, targetEnv string) (map[string]string, error) {
	// Initialize all results to "skipped" (default for not found)
	results := make(map[string]string)
	for _, name := range deployNames {
		results[name] = "skipped"
	}

	// Match job names to deploy names
	for _, job := range jobs {
		for _, deployName := range deployNames {
			// Build the pattern to match
			// For simple jobs: "Deploy <name>"
			// For matrix jobs: "Deploy <name> (<env>)"
			pattern := fmt.Sprintf("Deploy %s", deployName)

			// Check if job name matches
			var matches bool
			if targetEnv != "" {
				// For cascade with specific environment, match exact pattern
				envPattern := fmt.Sprintf("%s (%s)", pattern, targetEnv)
				matches = job.Name == envPattern
			} else {
				// Exact simple name, or the name at a matrix boundary
				// "Deploy <name> (". A bare prefix would cross-match
				// prefix-overlapping deploys (app vs app-canary).
				matches = job.Name == pattern || strings.HasPrefix(job.Name, pattern+" (")
			}

			if matches {
				// Extract conclusion (handle null for in_progress jobs)
				conclusion := "skipped"
				if job.Conclusion != nil && *job.Conclusion != "" {
					conclusion = *job.Conclusion
				}

				// Only update if we haven't found a result yet (first match wins)
				if results[deployName] == "skipped" {
					results[deployName] = conclusion
				}
				break
			}
		}
	}

	return results, nil
}

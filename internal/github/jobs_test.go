package github

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseJobResults_SimpleJobs(t *testing.T) {
	// Mock response from GitHub API with simple job names
	mockResponse := `{
		"jobs": [
			{"name": "Deploy infra", "conclusion": "success"},
			{"name": "Deploy app", "conclusion": "failure"},
			{"name": "Deploy monitoring", "conclusion": "skipped"}
		]
	}`

	results, err := ParseJobResults([]byte(mockResponse), []string{"infra", "app", "monitoring"})
	require.NoError(t, err)
	require.Equal(t, "success", results["infra"])
	require.Equal(t, "failure", results["app"])
	require.Equal(t, "skipped", results["monitoring"])
}

func TestParseJobResults_MatrixJobs(t *testing.T) {
	// Mock response with matrix jobs (e.g., for cascade promotions)
	mockResponse := `{
		"jobs": [
			{"name": "Deploy infra (test)", "conclusion": "success"},
			{"name": "Deploy infra (uat)", "conclusion": "success"},
			{"name": "Deploy app (test)", "conclusion": "failure"},
			{"name": "Deploy app (uat)", "conclusion": "skipped"}
		]
	}`

	// For matrix jobs, we return the first matching result (target env is in the job name)
	// The finalize command will query for the specific target environment
	results, err := ParseJobResults([]byte(mockResponse), []string{"infra", "app"})
	require.NoError(t, err)

	// Should match the first occurrence of each deploy
	require.Equal(t, "success", results["infra"])
	require.Equal(t, "failure", results["app"])
}

func TestParseJobResults_MissingDeploys(t *testing.T) {
	mockResponse := `{
		"jobs": [
			{"name": "Deploy infra", "conclusion": "success"},
			{"name": "Build app", "conclusion": "success"}
		]
	}`

	// Request results for deploys that don't exist in the response
	results, err := ParseJobResults([]byte(mockResponse), []string{"infra", "app", "monitoring"})
	require.NoError(t, err)
	require.Equal(t, "success", results["infra"])
	require.Equal(t, "skipped", results["app"])        // Default for not found
	require.Equal(t, "skipped", results["monitoring"]) // Default for not found
}

func TestParseJobResults_EmptyResponse(t *testing.T) {
	mockResponse := `{"jobs": []}`

	results, err := ParseJobResults([]byte(mockResponse), []string{"infra", "app"})
	require.NoError(t, err)
	require.Equal(t, "skipped", results["infra"])
	require.Equal(t, "skipped", results["app"])
}

func TestParseJobResults_InvalidJSON(t *testing.T) {
	mockResponse := `{invalid json}`

	_, err := ParseJobResults([]byte(mockResponse), []string{"infra"})
	require.Error(t, err)
}

func TestParseJobResults_CascadeWithTargetEnv(t *testing.T) {
	// Test for cascade promotion where we want results for a specific environment
	mockResponse := `{
		"jobs": [
			{"name": "Deploy infra (test)", "conclusion": "success"},
			{"name": "Deploy infra (uat)", "conclusion": "failure"},
			{"name": "Deploy app (test)", "conclusion": "success"},
			{"name": "Deploy app (uat)", "conclusion": "success"}
		]
	}`

	// With target env filter, we should only get results for that env
	results, err := ParseJobResultsForEnv([]byte(mockResponse), []string{"infra", "app"}, "uat")
	require.NoError(t, err)
	require.Equal(t, "failure", results["infra"])
	require.Equal(t, "success", results["app"])
}

func TestParseSlurpedJobResults_MergesPages(t *testing.T) {
	// `gh api --paginate --slurp` wraps each page object in a top-level JSON
	// array. A run with >100 jobs spans multiple pages; a deploy whose job
	// lands on the second page must still be found.
	mockResponse := `[
		{"jobs": [{"name": "Deploy infra", "conclusion": "success"}]},
		{"jobs": [{"name": "Deploy app", "conclusion": "failure"}]}
	]`

	results, err := ParseSlurpedJobResults([]byte(mockResponse), []string{"infra", "app"})
	require.NoError(t, err)
	require.Equal(t, "success", results["infra"])
	require.Equal(t, "failure", results["app"])
}

func TestParseSlurpedJobResults_SinglePage(t *testing.T) {
	// --slurp wraps even a single page in an array.
	mockResponse := `[{"jobs": [{"name": "Deploy infra", "conclusion": "success"}]}]`

	results, err := ParseSlurpedJobResults([]byte(mockResponse), []string{"infra"})
	require.NoError(t, err)
	require.Equal(t, "success", results["infra"])
}

func TestParseSlurpedJobResults_InvalidJSON(t *testing.T) {
	// Bare concatenated page objects (what --paginate emits WITHOUT --slurp)
	// are not valid JSON and must surface as an error, not a silent default.
	mockResponse := `{"jobs": []}{"jobs": []}`

	_, err := ParseSlurpedJobResults([]byte(mockResponse), []string{"infra"})
	require.Error(t, err)
}

func TestJobsAPIArgs_PaginatesWithSlurp(t *testing.T) {
	// The jobs endpoint returns an object per page; --paginate alone
	// concatenates objects into invalid JSON. --slurp must accompany it.
	args := jobsAPIArgs("owner/repo", "123")
	require.Contains(t, args, "repos/owner/repo/actions/runs/123/jobs")
	require.Contains(t, args, "--paginate")
	require.Contains(t, args, "--slurp")
}

func TestParseJobResults_PrefixOverlappingDeployNames(t *testing.T) {
	// Deploy names where one is a prefix of another must not cross-match:
	// "Deploy app-canary (dev)" belongs to app-canary, never to app.
	mockResponse := `{
		"jobs": [
			{"name": "Deploy app-canary (dev)", "conclusion": "failure"},
			{"name": "Deploy app (dev)", "conclusion": "success"}
		]
	}`

	results, err := ParseJobResults([]byte(mockResponse), []string{"app", "app-canary"})
	require.NoError(t, err)
	require.Equal(t, "success", results["app"])
	require.Equal(t, "failure", results["app-canary"])
}

func TestParseJobResults_PrefixOverlappingSimpleNames(t *testing.T) {
	// A simple (non-matrix) job "Deploy app-canary" must not be recorded
	// under deploy "app"; app is genuinely absent here.
	mockResponse := `{
		"jobs": [
			{"name": "Deploy app-canary", "conclusion": "failure"}
		]
	}`

	results, err := ParseJobResults([]byte(mockResponse), []string{"app", "app-canary"})
	require.NoError(t, err)
	require.Equal(t, "skipped", results["app"])
	require.Equal(t, "failure", results["app-canary"])
}

func TestParseJobResults_NullConclusion(t *testing.T) {
	// Jobs that are in_progress or queued have null conclusion
	mockResponse := `{
		"jobs": [
			{"name": "Deploy infra", "conclusion": "success"},
			{"name": "Deploy app", "conclusion": null}
		]
	}`

	results, err := ParseJobResults([]byte(mockResponse), []string{"infra", "app"})
	require.NoError(t, err)
	require.Equal(t, "success", results["infra"])
	require.Equal(t, "skipped", results["app"]) // Null conclusion defaults to skipped
}

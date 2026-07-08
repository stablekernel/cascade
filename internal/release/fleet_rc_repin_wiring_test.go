package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fleetRepoRoot walks up from the test's working directory to the repo's go.mod
// so the workflow-wiring assertions below are independent of the directory
// go test happens to run them from.
func fleetRepoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "reached filesystem root without finding go.mod")
		dir = parent
	}
}

type fleetJob struct {
	Uses  string    `yaml:"uses"`
	Needs yaml.Node `yaml:"needs"`
}

// fleetWorkflow decodes only the jobs graph. The top-level `on:` key is left out
// deliberately: YAML 1.1 treats the bare word `on` as a boolean, so decoding it
// into a struct is a foot-gun. The trigger shape is asserted by string match
// instead (see TestSuiteRCRepin_IsReusableAndCoversRoster).
type fleetWorkflow struct {
	Jobs map[string]fleetJob `yaml:"jobs"`
}

// needsList normalizes a job's `needs:` (a scalar or a sequence) to a slice.
func needsList(t *testing.T, n yaml.Node) []string {
	t.Helper()

	switch n.Kind {
	case 0:
		return nil
	case yaml.ScalarNode:
		return []string{n.Value}
	default:
		var out []string
		require.NoError(t, n.Decode(&out))
		return out
	}
}

// TestFleetE2E_SuiteLanesGateOnRepin is the regression guard for the just-in-time
// repin. A suite cannot repin its own committed setup-cli `uses:` ref because
// GitHub resolves a job's `uses:` refs from the workflow file as it existed at
// trigger time, so the fleet must re-stamp every example repo onto the rc BEFORE
// any lane fans out. This asserts the repin job exists, delegates to the reusable
// suite-rc-repin workflow, and that every suite lane gates on it. It fails if the
// pre-fan-out repin is ever removed again (as it was when the fleet moved to a
// broken self-repin model).
func TestFleetE2E_SuiteLanesGateOnRepin(t *testing.T) {
	root := fleetRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "fleet-e2e.yaml"))
	require.NoError(t, err)

	var wf fleetWorkflow
	require.NoError(t, yaml.Unmarshal(data, &wf))

	repin, ok := wf.Jobs["repin"]
	require.True(t, ok, "fleet-e2e.yaml must define a repin job")
	require.Equal(t, "./.github/workflows/suite-rc-repin.yaml", repin.Uses,
		"the repin job must delegate to the reusable suite-rc-repin workflow")
	require.Contains(t, needsList(t, repin.Needs), "resolve",
		"repin must run after resolve so it has the version under test")

	// Every suite fan-out lane, plus the aggregate gate, must gate on repin so
	// none can start (or conclude green) against a stale committed ref.
	for _, lane := range []string{"primary", "dependents", "heavy", "remainder", "aggregate"} {
		job, ok := wf.Jobs[lane]
		require.True(t, ok, "fleet-e2e.yaml must define the %s job", lane)
		require.Contains(t, needsList(t, job.Needs), "repin",
			"the %s job must list repin in needs so it never runs against a stale committed ref", lane)
	}
}

// TestSuiteRCRepin_IsReusableAndCoversRoster asserts the reusable repin workflow
// is a workflow_call with the required cascade_version input, covers the full
// canonical fleet roster (kept in sync with fleet-e2e's plan job), and takes its
// own concurrency group rather than colliding with suite-bootstrap-pin's.
func TestSuiteRCRepin_IsReusableAndCoversRoster(t *testing.T) {
	root := fleetRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "suite-rc-repin.yaml"))
	require.NoError(t, err)
	content := string(data)

	require.Contains(t, content, "workflow_call:",
		"suite-rc-repin.yaml must be a reusable workflow_call workflow")
	require.Contains(t, content, "cascade_version:",
		"suite-rc-repin.yaml must accept a cascade_version input")

	// Own concurrency group, never suite-bootstrap-pin's (the final-release
	// bumper), so the rc repin does not queue behind or fight that workflow.
	require.Contains(t, content, "group: suite-rc-repin-",
		"suite-rc-repin.yaml must use its own concurrency group")
	require.NotContains(t, content, "group: suite-bootstrap-pin",
		"suite-rc-repin.yaml must not share suite-bootstrap-pin's concurrency group")

	// The repin roster must cover every example repo the fleet fans out to.
	roster := []string{
		"primary", "artifact-a", "artifact-b", "4env", "3env", "2env",
		"single-env", "release-only", "no-env", "callbacks", "rollback-dispatch",
		"monorepo",
	}
	// Assert the roster appears as one whitespace-separated REPOS list so a
	// dropped repo is caught, not merely mentioned somewhere in a comment.
	rosterLine := strings.Join(roster, " ")
	require.Contains(t, content, rosterLine,
		"suite-rc-repin.yaml REPOS roster must match the fleet-e2e plan roster exactly")
}

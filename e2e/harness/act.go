package harness

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ActRunner wraps an act container instance
type ActRunner struct {
	container   testcontainers.Container
	giteaURL    string // External Gitea URL (http://host.docker.internal:<port>)
	giteaToken  string // Basic auth credentials
	networkName string // Docker network name for job containers
}

// RunOpts configures a workflow run
type RunOpts struct {
	WorkflowContent string            // Inline workflow YAML
	WorkflowPath    string            // Or path to workflow file
	Event           string            // push, workflow_dispatch, etc.
	EventJSON       string            // Event payload JSON
	Inputs          map[string]string // workflow_dispatch inputs
	Env             map[string]string // Environment variables
	RepoPath        string            // Path to cloned repo in container
}

// NewActRunner starts a new act container
func NewActRunner(ctx context.Context, giteaURL, giteaToken, networkName string, net *testcontainers.DockerNetwork) (*ActRunner, error) {
	var networks []string
	if net != nil && networkName != "" {
		networks = []string{networkName}
	}

	// Use a base image with act pre-installed
	// We'll use catthehacker/ubuntu which act uses internally.
	// The act container is on `networkName`, so it resolves the `gitea`
	// network alias directly. Job containers are configured separately
	// in actrc below.
	req := testcontainers.ContainerRequest{
		Image:      "ghcr.io/catthehacker/ubuntu:act-latest",
		Cmd:        []string{"sleep", "infinity"}, // Keep container running
		Networks:   networks,
		WaitingFor: wait.ForExec([]string{"echo", "ready"}),
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Mounts = append(hc.Mounts, mount.Mount{
				Type:   mount.TypeBind,
				Source: "/var/run/docker.sock",
				Target: "/var/run/docker.sock",
			})
		},
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start act container: %w", err)
	}

	// Install act in the container
	_, _, err = container.Exec(ctx, []string{
		"bash", "-c",
		"curl -s https://raw.githubusercontent.com/nektos/act/master/install.sh | bash -s -- -b /usr/local/bin",
	})
	if err != nil {
		_ = container.Terminate(ctx) // Best-effort cleanup
		return nil, fmt.Errorf("failed to install act: %w", err)
	}

	// Network override is passed on the CLI as `--network=<name>`. Act's
	// dedicated flag drives ContainerNetworkMode; --container-options is
	// appended after docker create and cannot override the network mode.
	actrc := `mkdir -p /root/.config/act && cat > /root/.config/act/actrc <<'EOF'
-P ubuntu-latest=catthehacker/ubuntu:act-latest
--pull=false
EOF`
	_, _, err = container.Exec(ctx, []string{"bash", "-c", actrc})
	if err != nil {
		_ = container.Terminate(ctx) // Best-effort cleanup
		return nil, fmt.Errorf("failed to configure act: %w", err)
	}

	// Pre-pull the act job-container image into the host docker daemon (via
	// the bind-mounted docker socket). Without this, every parallel scenario
	// triggers its own `docker pull` and races to fetch from registry-1.docker.io
	// Under proxied or rate-limited connections (e.g., Docker Desktop on
	// macOS) those pulls intermittently time out and fail the workflow.
	// The pull is best-effort: if it fails (offline, image already present,
	// proxy hiccup), we proceed; act will retry on its own when needed.
	_, _, _ = container.Exec(ctx, []string{
		"bash", "-c",
		`docker pull catthehacker/ubuntu:act-latest >/dev/null 2>&1 || true`,
	})

	return &ActRunner{
		container:   container,
		giteaURL:    giteaURL,
		giteaToken:  giteaToken,
		networkName: networkName,
	}, nil
}

// Container returns the underlying testcontainers.Container
func (a *ActRunner) Container() testcontainers.Container {
	return a.container
}

// GiteaURL returns the external Gitea URL for use in git operations
func (a *ActRunner) GiteaURL() string {
	return a.giteaURL
}

// Terminate stops and removes the container
func (a *ActRunner) Terminate(ctx context.Context) error {
	return a.container.Terminate(ctx)
}

// RunWorkflow executes a GitHub Actions workflow using act
func (a *ActRunner) RunWorkflow(ctx context.Context, opts RunOpts) (*ExtendedWorkflowResult, error) {
	// Create temp directory for workflow
	_, _, err := a.container.Exec(ctx, []string{"mkdir", "-p", "/tmp/workflow/.github/workflows"})
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow directory: %w", err)
	}

	// Write workflow content
	if opts.WorkflowContent != "" {
		_, _, err = a.container.Exec(ctx, []string{
			"bash", "-c",
			fmt.Sprintf("cat > /tmp/workflow/.github/workflows/test.yaml << 'EOFWORKFLOW'\n%s\nEOFWORKFLOW", opts.WorkflowContent),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to write workflow: %w", err)
		}
	}

	// --network sets ContainerNetworkMode (default "host"); --container-options
	// is appended after and cannot override it. Job containers must be on the
	// harness network to resolve the `gitea` alias.
	network := "bridge"
	if a.networkName != "" {
		network = a.networkName
	}
	cmd := []string{
		"act", opts.Event,
		"-W", "/tmp/workflow/.github/workflows",
		"--detect-event",
		"--json",
		"--pull=false",
		"--network=" + network,
	}

	// Add Gitea environment variables
	if a.giteaURL != "" {
		cmd = append(cmd, "--env", fmt.Sprintf("GITHUB_SERVER_URL=%s", a.giteaURL))
		cmd = append(cmd, "--env", fmt.Sprintf("GITHUB_API_URL=%s/api/v1", a.giteaURL))
	}
	if a.giteaToken != "" {
		cmd = append(cmd, "--secret", fmt.Sprintf("GITHUB_TOKEN=%s", a.giteaToken))
	}

	// Add inputs
	for k, v := range opts.Inputs {
		cmd = append(cmd, "--input", fmt.Sprintf("%s=%s", k, v))
	}

	// Add env vars
	for k, v := range opts.Env {
		cmd = append(cmd, "--env", fmt.Sprintf("%s=%s", k, v))
	}

	// Run act and capture output
	exitCode, reader, err := a.container.Exec(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to run act: %w", err)
	}

	// Read logs from the reader
	var logs bytes.Buffer
	if reader != nil {
		_, err = io.Copy(&logs, reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read act output: %w", err)
		}
	}

	// Parse JSON output
	result, err := ParseActOutput(logs.String())
	if err != nil {
		// Fall back to basic result if parsing fails
		result = &ExtendedWorkflowResult{
			Conclusion: "success",
			Jobs:       make(map[string]*JobResultExtended),
			Logs:       logs.String(),
		}
	}

	// Override conclusion based on exit code
	if exitCode != 0 {
		result.Conclusion = "failure"
	}

	return result, nil
}

// RunWorkflowFromRepo runs a workflow from the cloned repo at /tmp/repo
func (a *ActRunner) RunWorkflowFromRepo(ctx context.Context, opts RunOpts) (*ExtendedWorkflowResult, error) {
	// Build act command - run from within the repo directory
	// We configure Gitea as the Git server for checkout operations

	// Determine workflows path - use specific file if provided, otherwise directory
	workflowsPath := ".github/workflows"
	if opts.WorkflowPath != "" {
		workflowsPath = opts.WorkflowPath
	}

	// --network sets ContainerNetworkMode (default "host"); --container-options
	// is appended after and cannot override it. Job containers must be on the
	// harness network to resolve the `gitea` alias for checkout.
	network := "bridge"
	if a.networkName != "" {
		network = a.networkName
	}
	actCmd := "cd /tmp/repo && act " + opts.Event +
		" -W " + workflowsPath +
		" --detect-event" +
		" --json" +
		" --pull=false" +
		" --network=" + network +
		// Set Gitea as the Git server for checkout operations
		" --env GITHUB_SERVER_URL=" + a.giteaURL +
		" --env GITHUB_API_URL=" + a.giteaURL + "/api/v1" +
		// Provide token for Gitea authentication
		" --secret GITHUB_TOKEN=" + a.giteaToken

	actCmd += a.buildActArgs(opts)

	cmd := []string{
		"bash", "-c",
		actCmd,
	}

	// Run act
	exitCode, reader, err := a.container.Exec(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to run act: %w", err)
	}

	var logs bytes.Buffer
	if reader != nil {
		_, _ = io.Copy(&logs, reader)
	}

	// Clean the output: strip Docker's multiplexed stream headers
	// Docker exec output contains 8-byte headers for each chunk
	cleanedLogs := stripDockerStreamHeaders(logs.String())

	result, err := ParseActOutput(cleanedLogs)
	if err != nil {
		result = &ExtendedWorkflowResult{
			Conclusion: "success",
			Jobs:       make(map[string]*JobResultExtended),
			Logs:       logs.String(),
		}
	}

	normalizeWorkflowResult(result, opts.WorkflowPath, exitCode)

	return result, nil
}

// normalizeWorkflowResult reconciles a parsed act result with the exec exit
// code and the run's expectations. A non-zero exit is always a failure. A run
// that targeted a specific workflow file but produced zero parsed jobs is also
// a failure: act emitted no job events because it could not find or load the
// workflow (e.g. a missing orchestrate.yaml). Without this, such a run
// masqueraded as Conclusion="success" with 0 jobs. A missing workflow showing
// up as a green-but-empty scenario (#25).
func normalizeWorkflowResult(result *ExtendedWorkflowResult, workflowPath string, exitCode int) {
	if exitCode != 0 {
		result.Conclusion = "failure"
		result.Error = "workflow execution failed"
	}

	if workflowPath != "" && len(result.Jobs) == 0 && result.Conclusion != "failure" {
		result.Conclusion = "failure"
		result.Error = fmt.Sprintf("act produced no jobs for workflow %q (workflow missing or failed to load)", workflowPath)
	}
}

// buildActArgs builds additional act command arguments
func (a *ActRunner) buildActArgs(opts RunOpts) string {
	var args string

	// Add provided env vars
	for k, v := range opts.Env {
		args += fmt.Sprintf(" --env %s=%s", k, v)
	}

	// Add inputs for workflow_dispatch
	for k, v := range opts.Inputs {
		args += fmt.Sprintf(" --input %s=%s", k, v)
	}

	return args
}

// stripDockerStreamHeaders removes Docker's multiplexed stream headers from output.
// Docker exec output contains 8-byte headers for each chunk:
// - 1 byte: stream type (0=stdin, 1=stdout, 2=stderr)
// - 3 bytes: padding
// - 4 bytes: payload size (big endian)
func stripDockerStreamHeaders(input string) string {
	var result bytes.Buffer
	data := []byte(input)

	for i := 0; i < len(data); {
		// Look for the start of a JSON object
		if data[i] == '{' {
			// Find the end of this JSON line (newline or end of data)
			end := i
			braceCount := 0
			for end < len(data) {
				if data[end] == '{' {
					braceCount++
				} else if data[end] == '}' {
					braceCount--
					if braceCount == 0 {
						end++
						break
					}
				}
				end++
			}
			result.Write(data[i:end])
			result.WriteByte('\n')
			i = end
		} else {
			// Skip non-JSON bytes (headers, control chars, etc.)
			i++
		}
	}

	return result.String()
}

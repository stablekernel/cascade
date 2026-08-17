package harness

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
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

// actStartupTimeout bounds how long the container readiness probe waits for the
// act container to become runnable. testcontainers' default is 60s, which is
// ample for the probe itself but offers no slack when a cold first-run image
// pull has already consumed wall-clock and daemon bandwidth. A generous-but-
// finite budget tolerates the one-time cold pull while still failing closed if
// the container never becomes ready.
const actStartupTimeout = 5 * time.Minute

// actStartupPollInterval is how often the readiness probe re-checks while
// waiting. A brisk interval detects a healthy container promptly once the pull
// completes, without hammering the daemon.
const actStartupPollInterval = 2 * time.Second

// actRunnerImage pins the catthehacker/ubuntu image used BOTH as the act
// orchestrator container and as the job container act spawns for each generated
// workflow. It is a DIGEST pin, not the rolling :act-latest tag, so the e2e
// suite runs on a deterministic runtime instead of whatever the tag happens to
// point at on a given day. The digest must carry Node >= actRunnerNodeMajorMin
// because generated workflows run checkout (a Node 24 action) and act executes
// every JavaScript action with the image's single node binary;
// assertNodeMajorAtLeast enforces this at startup. catthehacker stopped
// publishing dated act-latest-YYYYMMDD tags in 2023 (all predate Node 24), so a
// digest is the only deterministic reference that reaches a Node 24 runtime.
//
// The scheduled Act Runner Image Repin workflow
// (.github/workflows/act-image-repin.yml) re-resolves act-latest to its current
// digest and opens a repin pull request whenever it moves, so this pin keeps
// tracking a live, tag-referenced digest ahead of any upstream
// garbage-collection window. The manual recipe below stays the fallback.
//
// To repin by hand: pull ghcr.io/catthehacker/ubuntu:act-latest, confirm
// `docker run --rm <image> node --version` reports >= actRunnerNodeMajorMin,
// then record the resolved digest here
// (`docker inspect --format '{{index .RepoDigests 0}}' <image>`).
const actRunnerImage = "ghcr.io/catthehacker/ubuntu@sha256:62d572b92f9f32d3427b6d220ad1f9dca9c7b6ffad37d295425037dbff78abaf"

// actRunnerNodeMajorMin is the minimum Node major version the pinned image must
// provide. checkout v7, github-script v9, and download-artifact v8 are Node 24
// actions, and act runs every JavaScript action with the image's single node
// binary, so the image's node must be at least this major or those live
// scenarios crash with an obscure runtime error instead of a clear signal.
const actRunnerNodeMajorMin = 24

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
		Image:    actRunnerImage,
		Cmd:      []string{"sleep", "infinity"}, // Keep container running
		Networks: networks,
		// The readiness exec ("echo ready") only proves the container is up; it
		// is near-instant once the container is running. The slow, variable part
		// is the FIRST-RUN image pull, which testcontainers performs as part of
		// container creation, before this wait strategy's clock starts. The
		// strategy's own startup budget defaults to 60s, which is fine for the
		// exec itself but leaves no slack when a cold pull has already eaten into
		// the daemon's bandwidth. Give the readiness probe an explicit,
		// generous budget (still a hard cap: a container that never becomes
		// runnable fails after actStartupTimeout) and poll briskly so a healthy
		// container is detected promptly.
		WaitingFor: wait.ForExec([]string{"echo", "ready"}).
			WithStartupTimeout(actStartupTimeout).
			WithPollInterval(actStartupPollInterval),
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

	// Install act in the container, verifying the binary actually lands and
	// retrying the transient network hiccups that the install script is prone
	// to. The real *DockerContainer satisfies containerExecer directly.
	if err := installActWithVerify(ctx, container, actInstallMaxAttempts); err != nil {
		_ = container.Terminate(ctx) // Best-effort cleanup
		return nil, err
	}

	// Network override is passed on the CLI as `--network=<name>`. Act's
	// dedicated flag drives ContainerNetworkMode; --container-options is
	// appended after docker create and cannot override the network mode.
	actrc := fmt.Sprintf(`mkdir -p /root/.config/act && cat > /root/.config/act/actrc <<'EOF'
-P ubuntu-latest=%s
--pull=false
EOF`, actRunnerImage)
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
		fmt.Sprintf(`docker pull %s >/dev/null 2>&1 || true`, actRunnerImage),
	})

	// Fail fast if the pinned image's Node runtime regressed below the minimum
	// the generated workflows require. The orchestrator container and the job
	// containers act spawns share actRunnerImage, so one cheap exec here proves
	// the job runtime too. A stale or mis-pinned image surfaces as a clear error
	// pointing at the pin constant, not as a downstream checkout/action crash.
	if err := assertNodeMajorAtLeast(ctx, container, actRunnerNodeMajorMin); err != nil {
		_ = container.Terminate(ctx) // Best-effort cleanup
		return nil, err
	}

	return &ActRunner{
		container:   container,
		giteaURL:    giteaURL,
		giteaToken:  giteaToken,
		networkName: networkName,
	}, nil
}

// containerExecer is the slice of the container surface the act install needs:
// running a command and getting back its exit code plus an output reader. The
// real *DockerContainer satisfies it, and a fake satisfies it in tests, so the
// install/verify/retry logic is exercisable without Docker or the network.
type containerExecer interface {
	Exec(ctx context.Context, cmd []string, options ...tcexec.ProcessOption) (int, io.Reader, error)
}

// actInstallMaxAttempts bounds how many times we try the act install before
// giving up. The install failure mode is a transient network hiccup (registry
// rate-limit, a dropped connection while fetching the install script), which a
// clean retry reliably clears, so a small fixed budget is enough.
const actInstallMaxAttempts = 3

// actInstallBackoff returns the pause before the retry that follows a failed
// install attempt (attempt is 1-based). It grows 2s then 4s so a momentary
// network blip has time to clear without stalling the suite. It is a var so
// tests can zero the wait and stay fast.
var actInstallBackoff = func(attempt int) time.Duration {
	return time.Duration(attempt*2) * time.Second
}

// installActWithVerify installs the act binary into the container and confirms
// it is actually runnable, retrying transient failures up to maxAttempts.
//
// This exists because container Exec returns a non-nil error ONLY for a Docker
// API/transport failure, NOT when the executed command exits non-zero. The
// original install discarded the exit code, so a failed `curl | bash` (a
// registry rate-limit or a dropped connection) looked like success: the runner
// came back "healthy" and the first workflow run died with
// `bash: act: command not found`. Here we check the exit code, then prove the
// binary resolves with `command -v act`, and only trust an attempt that clears
// both gates. A bounded retry with backoff absorbs the transient hiccup that is
// the usual cause; the final failure surfaces the failing command output so the
// real reason is not lost.
func installActWithVerify(ctx context.Context, exec containerExecer, maxAttempts int) error {
	const installScript = "curl -sSf https://raw.githubusercontent.com/nektos/act/master/install.sh | bash -s -- -b /usr/local/bin"

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("act install cancelled after %d attempt(s): %w", attempt-1, ctx.Err())
			case <-time.After(actInstallBackoff(attempt - 1)):
			}
		}

		installCode, installReader, err := exec.Exec(ctx, []string{"bash", "-c", installScript})
		if err != nil {
			lastErr = fmt.Errorf("act install exec failed: %w", err)
			continue
		}
		if installCode != 0 {
			lastErr = fmt.Errorf("act install exited with code %d: %s", installCode, readExecOutput(installReader))
			continue
		}

		verifyCode, verifyReader, err := exec.Exec(ctx, []string{"bash", "-c", "command -v act"})
		if err != nil {
			lastErr = fmt.Errorf("act verify exec failed: %w", err)
			continue
		}
		if verifyCode != 0 {
			lastErr = fmt.Errorf("act binary not resolvable after install (command -v act exited %d): %s", verifyCode, readExecOutput(verifyReader))
			continue
		}

		return nil
	}

	return fmt.Errorf("failed to install act after %d attempt(s): %w", maxAttempts, lastErr)
}

// assertNodeMajorAtLeast execs `node --version` inside the act container and
// errors if the reported Node major is below minMajor. The act orchestrator
// container and the job containers act spawns share actRunnerImage, so checking
// the orchestrator's node proves the job runtime too with a single cheap exec
// and no extra container. The reader is requested multiplexed so Docker's
// per-frame stream headers are stripped (a TTY-less exec on CI Linux would
// otherwise prefix the version string with header bytes and break parsing).
func assertNodeMajorAtLeast(ctx context.Context, exec containerExecer, minMajor int) error {
	code, reader, err := exec.Exec(ctx, []string{"node", "--version"}, tcexec.Multiplexed())
	if err != nil {
		return fmt.Errorf("act image node version check exec failed: %w", err)
	}
	out := readExecOutput(reader)
	if code != 0 {
		return fmt.Errorf("act image node version check exited %d: %s", code, out)
	}

	major, err := parseNodeMajor(out)
	if err != nil {
		return fmt.Errorf("act image %s: %w", actRunnerImage, err)
	}
	if major < minMajor {
		return fmt.Errorf(
			"act image %s provides Node %d (%q); the e2e suite requires Node >= %d because generated workflows run Node 24 actions. Repin actRunnerImage to a newer catthehacker digest carrying Node >= %d",
			actRunnerImage, major, out, minMajor, minMajor,
		)
	}
	return nil
}

// parseNodeMajor extracts the major version from a `node --version` string such
// as "v24.17.0" (the leading "v" is optional and trailing patch/build metadata
// is ignored). It returns an error for empty or non-numeric input so a garbled
// exec result fails loudly rather than silently reading as major 0.
func parseNodeMajor(version string) (int, error) {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" {
		return 0, fmt.Errorf("empty node version output")
	}
	majorStr := v
	if i := strings.IndexByte(v, '.'); i >= 0 {
		majorStr = v[:i]
	}
	major, err := strconv.Atoi(majorStr)
	if err != nil {
		return 0, fmt.Errorf("unparseable node version %q: %w", version, err)
	}
	return major, nil
}

// readExecOutput drains an exec output reader into a trimmed string for error
// messages, tolerating a nil reader. Output here is short (a curl error line or
// a `command -v` result), so reading it whole is fine.
func readExecOutput(reader io.Reader) string {
	if reader == nil {
		return ""
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Sprintf("(failed to read command output: %v)", err)
	}
	return strings.TrimSpace(string(out))
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

// eventFilePath is the in-container path the event payload is written to when
// RunOpts.EventJSON is set. act reads it via its -e flag to seed the
// github.event context (e.g. a merged pull_request payload).
const eventFilePath = "/tmp/cascade-event.json"

// dispatchInputsEventJSON builds a minimal workflow_dispatch event payload that
// embeds the run's inputs under the top-level "inputs" key, e.g.
//
//	{"inputs":{"commit":"<sha>","dry_run":"true","target_env":"test"}}
//
// act seeds the github.event.inputs context from the -e event file, but does NOT
// reliably populate it from --input flags alone when --detect-event is set and no
// event file carries an inputs key. A job gated on
// github.event.inputs.dry_run (e.g. the hotfix apply job) therefore evaluates its
// if: against an empty value and runs when it should be skipped. Writing this
// payload as the event file makes github.event.inputs authoritative regardless of
// act's --input handling.
//
// It returns "" when inputs is empty (no payload needed) or when marshaling fails
// (caller falls back to the --input-only behavior). The map is encoded by
// encoding/json, which emits keys in sorted order, so the output is deterministic.
func dispatchInputsEventJSON(inputs map[string]string) string {
	if len(inputs) == 0 {
		return ""
	}
	payload := map[string]map[string]string{"inputs": inputs}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// resolveEventJSON picks the event payload act should be pointed at for a run. An
// explicitly supplied opts.EventJSON always wins (e.g. a merged pull_request
// payload). Otherwise, for a workflow_dispatch carrying inputs, it synthesizes a
// payload via dispatchInputsEventJSON so github.event.inputs is seeded. All other
// runs get "" (no event file).
func resolveEventJSON(opts RunOpts) string {
	if opts.EventJSON != "" {
		return opts.EventJSON
	}
	if opts.Event == "workflow_dispatch" {
		return dispatchInputsEventJSON(opts.Inputs)
	}
	return ""
}

// writeEventFile writes the run's EventJSON payload to eventFilePath inside the
// act container via CopyToContainer. It is a no-op (returning "" with no error)
// when EventJSON is empty, so callers can unconditionally invoke it and only
// append the -e flag when a path comes back. CopyToContainer transfers the raw
// bytes, so arbitrary JSON (quotes, newlines, shell metacharacters) round-trips
// intact without heredoc or shell-escaping hazards.
func (a *ActRunner) writeEventFile(ctx context.Context, eventJSON string) (string, error) {
	if eventJSON == "" {
		return "", nil
	}
	if err := a.container.CopyToContainer(ctx, []byte(eventJSON), eventFilePath, 0644); err != nil {
		return "", fmt.Errorf("failed to write event payload: %w", err)
	}
	return eventFilePath, nil
}

// eventFileArgs returns the act flag fragment that points act at the event
// payload file. eventPath is the path returned by writeEventFile (empty when no
// EventJSON was supplied), so the result is empty in that case. Kept pure so the
// command construction is unit-testable without a container.
func eventFileArgs(eventPath string) []string {
	if eventPath == "" {
		return nil
	}
	return []string{"-e", eventPath}
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

	// When an event payload is supplied, write it into the container and point
	// act at it with -e so the github.event context is seeded (e.g. a merged
	// pull_request payload for the hotfix post-merge path).
	eventPath, evErr := a.writeEventFile(ctx, opts.EventJSON)
	if evErr != nil {
		return nil, evErr
	}
	cmd = append(cmd, eventFileArgs(eventPath)...)

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

	// Read and demultiplex the container's hijacked-attach stream. Without
	// demuxing, a TTY-less exec (CI Linux) prefixes each chunk with Docker's
	// 8-byte stream header, which corrupts the JSON the parser consumes.
	logs, err := readDemuxedStream(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read act output: %w", err)
	}

	// Parse JSON output
	result, err := ParseActOutput(logs)
	if err != nil {
		// Fall back to basic result if parsing fails
		result = &ExtendedWorkflowResult{
			Conclusion: "success",
			Jobs:       make(map[string]*JobResultExtended),
			Logs:       logs,
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
	// Resolve the event payload to point act at. An explicit opts.EventJSON wins
	// (e.g. the merged pull_request payload that drives the hotfix
	// context/build/deploy/finalize jobs). For a workflow_dispatch carrying
	// inputs, we synthesize a payload embedding those inputs so
	// github.event.inputs is seeded; act does not reliably populate that context
	// from --input flags alone, so a job gated on github.event.inputs (e.g. the
	// hotfix apply job's dry_run check) would otherwise mis-evaluate its if:.
	eventPath, err := a.writeEventFile(ctx, resolveEventJSON(opts))
	if err != nil {
		return nil, err
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

	actCmd += a.buildActArgs(opts, eventPath)

	cmd := []string{
		"bash", "-c",
		actCmd,
	}

	// Run act
	exitCode, reader, err := a.container.Exec(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to run act: %w", err)
	}

	// Read and demultiplex the container's hijacked-attach stream so the JSON
	// the parser consumes is free of Docker's per-chunk stream headers. On a
	// TTY-less exec (CI Linux) those headers would otherwise corrupt the
	// conclusion, job-failure, infra-saturation, and crash detection.
	cleanedLogs, err := readDemuxedStream(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read act output: %w", err)
	}

	result, err := ParseActOutput(cleanedLogs)
	if err != nil {
		result = &ExtendedWorkflowResult{
			Conclusion: "success",
			Jobs:       make(map[string]*JobResultExtended),
			Logs:       cleanedLogs,
		}
		// The parse fallback bypasses ParseActOutput's crash detection, so run
		// it directly here against the raw logs: a crash dump is the most likely
		// reason the JSON stream failed to parse, and it must not be lost.
		if crashed, reason := detectCrashSignature(cleanedLogs); crashed {
			result.Crashed = true
			result.CrashReason = reason
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
//
// Transient classification is DELIBERATELY narrow. A non-zero act exit is only
// tagged transient (ExecError true, retryable) when the raw output carries a
// NAMED host-saturation signature (see detectInfraSaturation): docker
// address-pool exhaustion (#125), disk-full, OOM, a gitea connection
// reset/refused (#121), or "Cannot connect to the Docker daemon". Those are
// genuine, externally-imposed transients a clean-slate retry can clear. Every
// OTHER non-zero exit with no job failure and no crash signature is now a
// DETERMINISTIC failure: it fails on the first attempt and surfaces a bug
// instead of being silently retried away. This closes the masking bucket that
// let self-inflicted contention burn the whole retry budget.
func normalizeWorkflowResult(result *ExtendedWorkflowResult, workflowPath string, exitCode int) {
	if exitCode != 0 {
		// Classify BEFORE forcing the conclusion to "failure": hasJobFailure
		// keys off result.Conclusion, so overwriting it first would make every
		// non-zero exit look like a real job failure and defeat the transient
		// path.
		switch {
		case result != nil && result.Crashed:
			// A Go-runtime crash (cascade CLI or act) corrupts the --json stream
			// so no "Job failed" event parses. Left to the ExecError heuristic
			// below it would look like a job-less hiccup and be retried away as a
			// transient flake, burning the retry budget and letting the stack
			// trace expire. A crash is a definitive defect: keep ExecError false
			// so the scenario runner does NOT retry it, and surface the crash
			// reason.
			result.ExecError = false
			result.Error = fmt.Sprintf("workflow crashed: %s", result.CrashReason)
		case hasJobFailure(result):
			// act ran the workflow and a job genuinely concluded "failure". That
			// is a real, deterministic defect, not a transport hiccup, so it must
			// not be retried.
			result.ExecError = false
			result.Error = "workflow execution failed"
		default:
			// A non-zero exit with no parsed job failure and no crash signature
			// is transient ONLY when a named host-saturation signature is in the
			// raw output; otherwise it is a deterministic real failure that must
			// surface (not be masked by a retry).
			if saturated, reason := detectInfraSaturation(infraLogs(result)); saturated {
				result.ExecError = true
				result.Error = fmt.Sprintf("infra saturation: %s", reason)
			} else {
				result.ExecError = false
				result.Error = "workflow execution failed"
			}
		}
		// A non-zero exit is always a failure; set the conclusion after
		// classification so hasJobFailure above saw the pre-exit conclusion.
		result.Conclusion = "failure"
	}

	if workflowPath != "" && len(result.Jobs) == 0 && result.Conclusion != "failure" {
		result.Conclusion = "failure"
		result.Error = fmt.Sprintf("act produced no jobs for workflow %q (workflow missing or failed to load)", workflowPath)
	}
}

// hasJobFailure reports whether act parsed a genuine job-level failure: either
// the reconciled run conclusion is already "failure", or at least one parsed
// job concluded "failure". When true, a non-zero act exit reflects a real
// workflow outcome rather than an act/docker exec hiccup, so it must not be
// classified as a transient ExecError.
func hasJobFailure(result *ExtendedWorkflowResult) bool {
	if result == nil {
		return false
	}
	if result.Conclusion == "failure" {
		return true
	}
	for _, job := range result.Jobs {
		if job != nil && job.Conclusion == "failure" {
			return true
		}
	}
	return false
}

// buildActArgs builds additional act command arguments. eventPath is the
// in-container event-payload file written by writeEventFile (empty when no
// EventJSON was supplied); when set it is appended as the act -e flag.
func (a *ActRunner) buildActArgs(opts RunOpts, eventPath string) string {
	var args string

	// Add provided env vars
	for k, v := range opts.Env {
		args += fmt.Sprintf(" --env %s=%s", k, v)
	}

	// Add inputs for workflow_dispatch
	for k, v := range opts.Inputs {
		args += fmt.Sprintf(" --input %s=%s", k, v)
	}

	// Point act at the event payload file when one was written.
	if flags := eventFileArgs(eventPath); len(flags) > 0 {
		args += " " + strings.Join(flags, " ")
	}

	return args
}

// dockerStreamHeaderLen is the size, in bytes, of the header Docker prepends to
// each frame of a multiplexed (non-TTY) hijacked-attach stream:
//   - 1 byte: stream type (0=stdin, 1=stdout, 2=stderr)
//   - 3 bytes: padding (always zero)
//   - 4 bytes: payload size (big-endian uint32)
const dockerStreamHeaderLen = 8

// readDemuxedStream drains a container exec's hijacked-attach reader and returns
// clean text with Docker's per-frame stream headers removed.
//
// A container created without a TTY (every act run here, and notably CI Linux
// where Docker allocates no TTY) returns a MULTIPLEXED stream: each chunk is
// prefixed with an 8-byte header (stream type + big-endian length). A raw
// io.Copy leaves those headers interspersed in the captured logs, which lands a
// `\x02\x00...` prefix on JSON lines and corrupts conclusion, job-failure,
// infra-saturation, and crash parsing. Docker Desktop happens to demux for us,
// which is why the corruption never reproduces locally and only bites in CI.
//
// We demultiplex with stdcopy.StdCopy (the same primitive testcontainers'
// exec.Multiplexed uses), merging stdout and stderr into a single buffer; act
// writes its --json log to stderr, so both streams must be captured. If the
// stream is NOT framed (a raw TTY attach), it is returned unchanged.
func readDemuxedStream(reader io.Reader) (string, error) {
	if reader == nil {
		return "", nil
	}

	raw, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("reading container stream: %w", err)
	}

	if !looksMultiplexed(raw) {
		// Raw (TTY) attach: no framing to strip.
		return string(raw), nil
	}

	var out bytes.Buffer
	// destOut and destErr both target out so stdout and stderr merge into one
	// clean log buffer (act emits its JSON event stream on stderr).
	if _, err := stdcopy.StdCopy(&out, &out, bytes.NewReader(raw)); err != nil {
		return "", fmt.Errorf("demultiplexing container stream: %w", err)
	}
	return out.String(), nil
}

// looksMultiplexed reports whether data begins with a well-formed Docker stream
// frame header: a known stream type, zeroed padding, and a length that does not
// overrun the buffer. This distinguishes a multiplexed (non-TTY) attach from a
// raw (TTY) attach so demuxing is only applied when there is framing to strip.
func looksMultiplexed(data []byte) bool {
	if len(data) < dockerStreamHeaderLen {
		return false
	}
	switch data[0] {
	case byte(stdcopy.Stdin), byte(stdcopy.Stdout), byte(stdcopy.Stderr):
	default:
		return false
	}
	if data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return false
	}
	frameSize := binary.BigEndian.Uint32(data[4:dockerStreamHeaderLen])
	return uint64(dockerStreamHeaderLen)+uint64(frameSize) <= uint64(len(data))
}

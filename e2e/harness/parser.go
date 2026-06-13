package harness

import (
	"bufio"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// ActEvent represents a single event from act's JSON output
// Act outputs JSON lines with fields like:
// {"job":"Workflow/JobName","jobID":"job-id","level":"info","msg":"⭐ Run Set up job",...}
type ActEvent struct {
	Job        string      `json:"job"`
	JobID      string      `json:"jobID"`
	Step       string      `json:"step"`
	StepID     interface{} `json:"stepid"` // Can be string or array
	Msg        string      `json:"msg"`
	Level      string      `json:"level"`
	JobResult  string      `json:"jobResult"`
	StepResult string      `json:"stepResult"`
	DryRun     bool        `json:"dryrun"`
	Matrix     interface{} `json:"matrix"` // Can be empty object or map
}

// ExtendedWorkflowResult contains detailed workflow execution results
type ExtendedWorkflowResult struct {
	Conclusion string
	Jobs       map[string]*JobResultExtended
	Logs       string
	Error      string
	// ExecError is true when act itself could not run the workflow to a real
	// conclusion: the act invocation exited non-zero (a docker-exec or act
	// transport hiccup) rather than a workflow job genuinely concluding
	// "failure". It distinguishes a transient infrastructure flake (safe to
	// retry from a clean slate) from a real job-level failure or an assertion
	// mismatch (which must fail deterministically). A run that parsed real job
	// events and concluded "failure" leaves ExecError false.
	ExecError bool
	// Crashed is true when act's raw output carries a Go-runtime crash
	// signature (a panic, a goroutine dump, a SIGSEGV/SIGABRT, or a fatal
	// error) from the cascade CLI or act itself. A crash corrupts act's --json
	// stream so no "Job failed" event is parseable, which would otherwise let a
	// genuine crash masquerade as a transient flake and be retried away. When
	// Crashed is true the run is a definitive failure, NOT a transient one.
	Crashed bool
	// CrashReason is the first matched crash-signature line, captured so the
	// failure surfaces the stack-trace origin rather than a generic message.
	CrashReason string
}

// goroutineDumpRE matches the header of a Go goroutine dump, e.g.
// "goroutine 1 [running]:". This is the anchored runtime signature emitted on a
// panic or fatal error; it does not match prose that merely contains the word
// "goroutine".
var goroutineDumpRE = regexp.MustCompile(`(?m)^goroutine \d+ \[`)

// signalCrashRE matches a Go runtime fatal-signal line, e.g.
// "[signal SIGSEGV: segmentation violation ...]" or a SIGABRT report.
var signalCrashRE = regexp.MustCompile(`signal SIG(SEGV|ABRT|BUS|FPE|ILL)`)

// detectCrashSignature reports whether raw act stdout/stderr carries a Go
// runtime crash signature, returning the first matched signature line as the
// reason. It anchors on real runtime signatures (a goroutine dump header, a
// "panic:" or "fatal error:" at the start of a line, a fatal signal report) so
// it does not misfire on benign occurrences of the words "panic" or
// "goroutine" inside an ordinary log line (e.g. a deploy script's prose).
func detectCrashSignature(logs string) (bool, string) {
	if logs == "" {
		return false, ""
	}

	scanner := bufio.NewScanner(strings.NewReader(logs))
	// A crash dump line can be long (a deeply nested stack frame); raise the
	// scanner's token limit so a long frame cannot truncate detection.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "panic:"):
			return true, trimmed
		case strings.HasPrefix(trimmed, "fatal error:"):
			return true, trimmed
		case goroutineDumpRE.MatchString(line):
			return true, trimmed
		case signalCrashRE.MatchString(line):
			return true, trimmed
		}
	}
	return false, ""
}

// JobResultExtended contains detailed result of a single job
type JobResultExtended struct {
	Name       string
	Conclusion string
	Steps      []*StepResult
	Outputs    map[string]string
}

// StepResult contains the result of a single step
type StepResult struct {
	Name       string
	Conclusion string
	Outputs    map[string]string
	Duration   time.Duration
}

// ParseActOutput parses act's JSON output into structured results
func ParseActOutput(output string) (*ExtendedWorkflowResult, error) {
	result := &ExtendedWorkflowResult{
		Conclusion: "success",
		Jobs:       make(map[string]*JobResultExtended),
		Logs:       output,
	}

	scanner := bufio.NewScanner(strings.NewReader(output))
	currentSteps := make(map[string][]*StepResult) // jobID -> steps

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Find the start of JSON object (skip any prefix garbage)
		jsonStart := strings.Index(line, "{")
		if jsonStart == -1 {
			continue
		}
		line = line[jsonStart:]

		var event ActEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Not valid JSON, skip
			continue
		}

		// Use jobID as the key since it matches the job names in workflow YAML
		jobID := event.JobID
		if jobID == "" {
			continue
		}

		// Handle job lifecycle events based on msg patterns
		// Use Unicode-safe string matching
		msg := event.Msg
		switch {
		case strings.Contains(msg, "Run Set up job") || strings.Contains(msg, "Run "):
			// Job starting (⭐ Run Set up job or similar)
			if _, exists := result.Jobs[jobID]; !exists {
				result.Jobs[jobID] = &JobResultExtended{
					Name:    jobID,
					Outputs: make(map[string]string),
				}
				currentSteps[jobID] = []*StepResult{}
			}

		case strings.Contains(msg, "Job failed") || strings.Contains(msg, "Job succeeded"):
			// Job ended (🏁 Job failed/succeeded) - capture result
			if job, exists := result.Jobs[jobID]; exists {
				job.Conclusion = event.JobResult
				job.Steps = currentSteps[jobID]
				if event.JobResult == "failure" {
					result.Conclusion = "failure"
				}
			}

		case event.StepResult == "failure":
			// Step failed (❌ Failure)
			steps := currentSteps[jobID]
			if len(steps) > 0 {
				steps[len(steps)-1].Conclusion = "failure"
			}

		case event.StepResult == "success":
			// Step succeeded (✅ Success)
			steps := currentSteps[jobID]
			if len(steps) > 0 {
				steps[len(steps)-1].Conclusion = "success"
			}

		case event.Step != "" && !strings.HasPrefix(msg, "  "):
			// New step starting (not an indented sub-message)
			if _, exists := result.Jobs[jobID]; exists {
				step := &StepResult{
					Name:    event.Step,
					Outputs: make(map[string]string),
				}
				currentSteps[jobID] = append(currentSteps[jobID], step)
			}
		}
	}

	// A Go-runtime crash (cascade CLI or act) can corrupt the --json stream so
	// no "Job failed" event parses; flag it from the raw logs so the classifier
	// treats it as a definitive failure rather than a transient flake.
	if crashed, reason := detectCrashSignature(output); crashed {
		result.Crashed = true
		result.CrashReason = reason
	}

	return result, scanner.Err()
}

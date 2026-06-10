package harness

import (
	"bufio"
	"encoding/json"
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

	return result, scanner.Err()
}

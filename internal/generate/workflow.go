package generate

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// WorkflowFile represents a GitHub Actions workflow file structure
type WorkflowFile struct {
	Name string                 `yaml:"name"`
	On   map[string]interface{} `yaml:"on"`
}

// WorkflowCallConfig represents the workflow_call trigger configuration
type WorkflowCallConfig struct {
	Inputs  map[string]WorkflowInput  `yaml:"inputs"`
	Outputs map[string]WorkflowOutput `yaml:"outputs"`
}

// WorkflowInput represents an input parameter
type WorkflowInput struct {
	Type        string      `yaml:"type"`
	Description string      `yaml:"description"`
	Required    bool        `yaml:"required"`
	Default     interface{} `yaml:"default"`
}

// WorkflowOutput represents an output value
type WorkflowOutput struct {
	Description string `yaml:"description"`
	Value       string `yaml:"value"`
}

// ParseWorkflowOutputs extracts output names from a workflow file
func ParseWorkflowOutputs(data []byte) ([]string, error) {
	callConfig, err := parseWorkflowCall(data)
	if err != nil {
		return nil, err
	}

	var outputs []string
	for name := range callConfig.Outputs {
		outputs = append(outputs, name)
	}
	sort.Strings(outputs)
	return outputs, nil
}

// ParseWorkflowInputs extracts input names from a workflow file
func ParseWorkflowInputs(data []byte) ([]string, error) {
	callConfig, err := parseWorkflowCall(data)
	if err != nil {
		return nil, err
	}

	var inputs []string
	for name := range callConfig.Inputs {
		inputs = append(inputs, name)
	}
	sort.Strings(inputs)
	return inputs, nil
}

// ParseWorkflowRequiredInputs extracts required input names from a workflow file
// An input is required if it has required: true and no default value
func ParseWorkflowRequiredInputs(data []byte) ([]string, error) {
	callConfig, err := parseWorkflowCall(data)
	if err != nil {
		return nil, err
	}

	var required []string
	for name, input := range callConfig.Inputs {
		// Required if explicitly marked and has no default
		if input.Required && input.Default == nil {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	return required, nil
}

func parseWorkflowCall(data []byte) (*WorkflowCallConfig, error) {
	var wf WorkflowFile
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parsing workflow file: %w", err)
	}

	// Check if workflow_call trigger exists
	onData, ok := wf.On["workflow_call"]
	if !ok {
		return nil, fmt.Errorf("not a reusable workflow: missing workflow_call trigger")
	}

	// Handle case where workflow_call is empty (just the trigger, no config)
	if onData == nil {
		return &WorkflowCallConfig{}, nil
	}

	// Re-marshal and unmarshal to get typed config
	callBytes, err := yaml.Marshal(onData)
	if err != nil {
		return nil, fmt.Errorf("marshaling workflow_call: %w", err)
	}

	var callConfig WorkflowCallConfig
	if err := yaml.Unmarshal(callBytes, &callConfig); err != nil {
		return nil, fmt.Errorf("parsing workflow_call config: %w", err)
	}

	return &callConfig, nil
}

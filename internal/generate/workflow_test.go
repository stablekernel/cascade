package generate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWorkflowOutputs(t *testing.T) {
	yaml := `
name: Build App
on:
  workflow_call:
    inputs:
      sha:
        type: string
    outputs:
      image_tag:
        description: The image tag
        value: ${{ jobs.build.outputs.image_tag }}
      digest:
        description: The image digest
        value: ${{ jobs.build.outputs.digest }}
jobs:
  build:
    runs-on: ubuntu-latest
`

	outputs, err := ParseWorkflowOutputs([]byte(yaml))
	require.NoError(t, err)

	assert.Equal(t, []string{"digest", "image_tag"}, outputs)
}

func TestParseWorkflowOutputs_NoOutputs(t *testing.T) {
	yaml := `
name: Simple Workflow
on:
  workflow_call:
jobs:
  run:
    runs-on: ubuntu-latest
`

	outputs, err := ParseWorkflowOutputs([]byte(yaml))
	require.NoError(t, err)
	assert.Empty(t, outputs)
}

func TestParseWorkflowOutputs_NotReusable(t *testing.T) {
	yaml := `
name: Not Reusable
on:
  push:
    branches: [main]
jobs:
  run:
    runs-on: ubuntu-latest
`

	_, err := ParseWorkflowOutputs([]byte(yaml))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a reusable workflow")
}

func TestParseWorkflowInputs(t *testing.T) {
	yaml := `
name: Deploy
on:
  workflow_call:
    inputs:
      environment:
        type: string
        required: true
      image_tag:
        type: string
      replicas:
        type: number
        default: 1
`

	inputs, err := ParseWorkflowInputs([]byte(yaml))
	require.NoError(t, err)

	assert.Contains(t, inputs, "environment")
	assert.Contains(t, inputs, "image_tag")
	assert.Contains(t, inputs, "replicas")
}

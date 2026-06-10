package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInlineRunRunsOnScalar asserts a per-callback scalar runs_on is emitted on
// an inline-run job's runs-on line, overriding the ubuntu-latest default.
func TestInlineRunRunsOnScalar(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "smoke",
				Run:      "go test ./...",
				Triggers: []string{"src/**"},
				RunsOn:   &config.RunsOn{Label: "self-hosted"},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-smoke:")
	require.NotEmpty(t, job)
	assert.Contains(t, job, "runs-on: self-hosted")
	assert.NotContains(t, job, "runs-on: ubuntu-latest")
}

// TestInlineRunRunsOnList asserts the list (label-set) union form renders as a
// flow sequence on the runs-on line.
func TestInlineRunRunsOnList(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "arm",
				Run:      "make build",
				Triggers: []string{"src/**"},
				RunsOn:   &config.RunsOn{Labels: []string{"self-hosted", "linux", "arm64"}},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-arm:")
	assert.Contains(t, job, "runs-on: [self-hosted, linux, arm64]")
}

// TestInlineRunRunsOnGroup asserts the {group, labels} union form renders as a
// flow mapping on the runs-on line.
func TestInlineRunRunsOnGroup(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "gpu",
				Run:      "make train",
				Triggers: []string{"src/**"},
				RunsOn:   &config.RunsOn{Group: "ml-runners", Labels: []string{"gpu"}},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-gpu:")
	assert.Contains(t, job, "runs-on: {group: ml-runners, labels: [gpu]}")
}

// TestInlineRunRunsOnConfigDefault asserts the config-level runs_on default is
// applied to a cascade-owned inline job that sets no per-callback runner.
func TestInlineRunRunsOnConfigDefault(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		RunsOn:       &config.RunsOn{Label: "self-hosted"},
		Builds: []config.BuildConfig{
			{
				Name:     "smoke",
				Run:      "go test ./...",
				Triggers: []string{"src/**"},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-smoke:")
	assert.Contains(t, job, "runs-on: self-hosted")
}

// TestInlineRunRunsOnPerCallbackOverridesConfigDefault asserts the per-callback
// runs_on wins over the config-level default.
func TestInlineRunRunsOnPerCallbackOverridesConfigDefault(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		RunsOn:       &config.RunsOn{Label: "default-runner"},
		Builds: []config.BuildConfig{
			{
				Name:     "smoke",
				Run:      "go test ./...",
				Triggers: []string{"src/**"},
				RunsOn:   &config.RunsOn{Label: "override-runner"},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-smoke:")
	assert.Contains(t, job, "runs-on: override-runner")
	assert.NotContains(t, job, "default-runner")
}

// TestInlineRunRunsOnFallbackDefault asserts ubuntu-latest is still emitted when
// nothing sets a runner (unchanged behavior, non-breaking).
func TestInlineRunRunsOnFallbackDefault(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "smoke",
				Run:      "go test ./...",
				Triggers: []string{"src/**"},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-smoke:")
	assert.Contains(t, job, "runs-on: ubuntu-latest")
}

// TestInlineRunPermissionsEmit asserts a per-callback permissions map is emitted
// on the inline job, with keys in deterministic (sorted) order.
func TestInlineRunPermissionsEmit(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:        "image",
				Run:         "docker build .",
				Triggers:    []string{"src/**"},
				Permissions: map[string]string{"packages": "write", "contents": "read"},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-image:")
	assert.Contains(t, job, "permissions:")
	assert.Contains(t, job, "contents: read")
	assert.Contains(t, job, "packages: write")
	// deterministic order: contents (sorted) before packages
	assert.Less(t, strings.Index(job, "contents: read"), strings.Index(job, "packages: write"))
}

// TestInlineRunOIDCIdTokenPermission asserts the OIDC use case (#15) is satisfied
// by the general permissions map: id-token: write is emitted as a plain entry,
// no dedicated field required.
func TestInlineRunOIDCIdTokenPermission(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:        "deploy",
				Run:         "aws sts get-caller-identity",
				Triggers:    []string{"src/**"},
				Permissions: map[string]string{"id-token": "write", "contents": "read"},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-deploy:")
	assert.Contains(t, job, "id-token: write")
}

// TestInlineRunConcurrencyEmit asserts a per-callback concurrency block is
// emitted on the inline job, including cancel-in-progress: false.
func TestInlineRunConcurrencyEmit(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:     "smoke",
				Run:      "go test ./...",
				Triggers: []string{"src/**"},
				Concurrency: &config.ConcurrencyConfig{
					Group:            "smoke-${{ github.ref }}",
					CancelInProgress: false,
				},
			},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-smoke:")
	assert.Contains(t, job, "concurrency:")
	assert.Contains(t, job, "group: smoke-${{ github.ref }}")
	assert.Contains(t, job, "cancel-in-progress: false")
}

// TestInlineRunOmittedAttributesUnchanged asserts that omitting all three fields
// changes nothing: no job-level permissions/concurrency, runs-on defaults to
// ubuntu-latest (non-breaking).
func TestInlineRunOmittedAttributesUnchanged(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{Name: "smoke", Run: "go test ./...", Triggers: []string{"src/**"}},
		},
	}

	gen := NewGenerator(cfg, t.TempDir())
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-smoke:")
	assert.Contains(t, job, "runs-on: ubuntu-latest")
	assert.NotContains(t, job, "permissions:")
	assert.NotContains(t, job, "concurrency:")
}

// TestReusableWorkflowCallbackHasNoJobAttributes asserts that a reusable
// workflow: callback (jobs.<id>.uses) never carries runs-on / permissions /
// concurrency on the job — GHA forbids them there and schema validation rejects
// runs_on/concurrency on reusable callbacks. Only permissions is structurally
// accepted on the config, but the generator must not emit any of the three on a
// uses: job.
func TestReusableWorkflowCallbackHasNoJobAttributes(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".github/workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".github/workflows/build.yaml"), []byte("on:\n  workflow_call:\n"), 0644))

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev"},
		Builds: []config.BuildConfig{
			{
				Name:        "app",
				Workflow:    ".github/workflows/build.yaml",
				Triggers:    []string{"src/**"},
				Permissions: map[string]string{"contents": "read"},
			},
		},
	}

	gen := NewGenerator(cfg, tmpDir)
	result, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(result, "build-app:")
	assert.Contains(t, job, "uses: ./.github/workflows/build.yaml")
	// None of the cascade-owned job attributes appear on a reusable uses: job.
	assert.NotContains(t, job, "runs-on:")
	assert.NotContains(t, job, "permissions:")
	assert.NotContains(t, job, "concurrency:")
}

// TestValidationRejectsJobAttributesOnReusableCallback confirms schema validation
// still rejects runs_on / concurrency on a reusable-workflow callback, so the
// generator never sees them there.
func TestValidationRejectsJobAttributesOnReusableCallback(t *testing.T) {
	runsOnCfg := &config.TrunkConfig{
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: "b.yaml", Triggers: []string{"src/**"}, RunsOn: &config.RunsOn{Label: "ubuntu-latest"}},
		},
	}
	assert.True(t, hasErr(config.Validate(runsOnCfg), "runs_on is not valid on a reusable-workflow callback"),
		"expected runs_on rejection on reusable callback")

	concCfg := &config.TrunkConfig{
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: "d.yaml", Triggers: []string{"src/**"}, Concurrency: &config.ConcurrencyConfig{Group: "g"}},
		},
	}
	assert.True(t, hasErr(config.Validate(concCfg), "concurrency is not valid on a reusable-workflow callback"),
		"expected concurrency rejection on reusable callback")
}

// TestPromoteInlineDeployCarriesAllJobAttributes is the e2e scenario: an inline
// run: deploy carrying runs_on + permissions (incl. OIDC id-token) + concurrency
// emits all three on the cascade-owned deploy job in the promote workflow.
func TestPromoteInlineDeployCarriesAllJobAttributes(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"dev", "prod"},
		Deploys: []config.DeployConfig{
			{
				Name:        "k8s",
				Run:         "kubectl apply -f .",
				Triggers:    []string{"deploy/**"},
				RunsOn:      &config.RunsOn{Labels: []string{"self-hosted", "cdk"}},
				Permissions: map[string]string{"id-token": "write", "contents": "read"},
				Concurrency: &config.ConcurrencyConfig{Group: "k8s-${{ inputs.target_env }}", CancelInProgress: false},
			},
		},
	}

	gen := NewPromoteGenerator(cfg, "")
	content, err := gen.Generate()
	require.NoError(t, err)

	job := jobSection(content, "deploy-k8s:")
	require.NotEmpty(t, job)
	assert.Contains(t, job, "runs-on: [self-hosted, cdk]")
	assert.Contains(t, job, "permissions:")
	assert.Contains(t, job, "id-token: write")
	assert.Contains(t, job, "contents: read")
	assert.Contains(t, job, "concurrency:")
	assert.Contains(t, job, "group: k8s-${{ inputs.target_env }}")
	assert.Contains(t, job, "cancel-in-progress: false")

	// The prod variant of the same inline deploy also carries the attributes.
	prod := jobSection(content, "deploy-k8s-prod:")
	require.NotEmpty(t, prod)
	assert.Contains(t, prod, "runs-on: [self-hosted, cdk]")
	assert.Contains(t, prod, "id-token: write")
}

// hasErr reports whether any error string contains substr.
func hasErr(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

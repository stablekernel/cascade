package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

func TestNormalizeWorkflowPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bare filename normalizes to github workflows dir",
			input: "build.yaml",
			want:  "./.github/workflows/build.yaml",
		},
		{
			name:  "github workflows path gets dot-slash prefix",
			input: ".github/workflows/x.yaml",
			want:  "./.github/workflows/x.yaml",
		},
		{
			name:  "already normalized path unchanged",
			input: "./.github/workflows/x.yaml",
			want:  "./.github/workflows/x.yaml",
		},
		{
			name:  "cross-repo external ref unchanged",
			input: "owner/repo/.github/workflows/x.yml@ref",
			want:  "owner/repo/.github/workflows/x.yml@ref",
		},
		{
			name:  "org satellite cross-repo ref unchanged",
			input: "org/satellite/.github/workflows/deploy.yaml@v1",
			want:  "org/satellite/.github/workflows/deploy.yaml@v1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeWorkflowPath(tc.input)
			if got != tc.want {
				t.Errorf("normalizeWorkflowPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNormalizeWorkflowPath_ActionlintClean(t *testing.T) {
	// Verify that a bare-path build callback generates an actionlint-clean workflow.
	actionlint, err := exec.LookPath("actionlint")
	if err != nil {
		// Try the known homebrew path.
		actionlint = "/opt/homebrew/bin/actionlint"
		if _, statErr := os.Stat(actionlint); statErr != nil {
			t.Skip("actionlint not found; skipping actionlint integration test")
		}
	}

	// Stage bare-filename stub callback workflows at the normalized location
	// (.github/workflows/<name>), which is where the generator discovers
	// inputs/outputs from and emits the uses: reference to.
	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if mkErr := os.MkdirAll(wfDir, 0o755); mkErr != nil {
		t.Fatalf("MkdirAll: %v", mkErr)
	}
	stub := []byte("on:\n  workflow_call:\n")
	if writeErr := os.WriteFile(filepath.Join(wfDir, "build.yaml"), stub, 0o644); writeErr != nil {
		t.Fatalf("WriteFile build stub: %v", writeErr)
	}
	if writeErr := os.WriteFile(filepath.Join(wfDir, "deploy.yaml"), stub, 0o644); writeErr != nil {
		t.Fatalf("WriteFile deploy stub: %v", writeErr)
	}

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: []string{"staging", "production"},
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: "build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: "deploy.yaml", DependsOn: []string{"app"}},
		},
	}

	g := NewGenerator(cfg, dir)
	wf, err := g.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	path := filepath.Join(wfDir, "orchestrate.yaml")
	if writeErr := os.WriteFile(path, []byte(wf), 0o644); writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}

	// Disable the optional external-linter integrations (shellcheck and
	// pyflakes): this test governs workflow structure and uses: reference
	// validity, not the style of cascade-owned run: scripts (which carry their
	// own pre-existing SC2129-style notes). Leaving these integrations on the
	// default-enabled setting makes the result depend on whether (and which
	// version of) shellcheck/pyflakes happens to be installed in the runner,
	// which is the source of the cross-environment flakiness. -no-color keeps
	// the captured output stable for the failure message.
	out, runErr := exec.Command(actionlint, "-shellcheck=", "-pyflakes=", "-no-color", path).CombinedOutput()
	if runErr != nil {
		t.Errorf("actionlint found errors in generated workflow:\n%s", out)
	}
}

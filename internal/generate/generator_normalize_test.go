package generate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// locateActionlint returns the actionlint binary path, or skips the test when
// the linter is not installed in the runner.
func locateActionlint(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("actionlint"); err == nil {
		return p
	}
	// Fall back to the known homebrew path.
	const brew = "/opt/homebrew/bin/actionlint"
	if _, err := os.Stat(brew); err == nil {
		return brew
	}
	t.Skip("actionlint not found; skipping actionlint integration test")
	return ""
}

// stageActionlintProject builds a self-contained project tree rooted at a fresh
// temp dir and returns (projectDir, workflowsDir).
//
// It stages the bare-filename callback stubs at the normalized location
// (.github/workflows/<name>), which is where the generator discovers
// inputs/outputs from and emits the uses: reference to. Each stub declares the
// environment workflow_call input that the generator wires into every reusable
// call, so a resolved uses: reference validates cleanly against its callback.
func stageActionlintProject(t *testing.T) (projectDir, workflowsDir string) {
	t.Helper()
	dir := t.TempDir()
	// Anchor actionlint's project-root detection to this temp tree. actionlint
	// resolves ./-prefixed reusable-workflow references relative to the nearest
	// ancestor .git directory; without a marker here it would walk up to an
	// unrelated repository (or find none) and silently skip local
	// reusable-workflow resolution, leaving the staged sibling stubs ignored and
	// the uses: references unvalidated. A bare .git directory is enough for the
	// project-root heuristic and keeps the tree fully self-contained.
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll .git anchor: %v", err)
	}
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("MkdirAll workflows: %v", err)
	}
	stub := []byte("on:\n" +
		"  workflow_call:\n" +
		"    inputs:\n" +
		"      environment:\n" +
		"        type: string\n" +
		"        required: true\n")
	for _, name := range []string{"build.yaml", "deploy.yaml"} {
		if err := os.WriteFile(filepath.Join(wfDir, name), stub, 0o644); err != nil {
			t.Fatalf("WriteFile %s stub: %v", name, err)
		}
	}
	return dir, wfDir
}

// runActionlint runs actionlint over path with the optional external-linter
// integrations (shellcheck and pyflakes) disabled: these tests govern workflow
// structure and uses: reference validity, not the style of cascade-owned run:
// scripts (which carry their own pre-existing SC2129-style notes). Leaving those
// integrations on the default-enabled setting makes the result depend on whether
// (and which version of) shellcheck/pyflakes happens to be installed in the
// runner, which is a source of cross-environment flakiness. -no-color keeps the
// captured output stable for failure messages.
func runActionlint(t *testing.T, bin, path string) (string, error) {
	t.Helper()
	out, err := exec.Command(bin, "-shellcheck=", "-pyflakes=", "-no-color", path).CombinedOutput()
	return string(out), err
}

func TestNormalizeWorkflowPath_ActionlintClean(t *testing.T) {
	// Verify that a bare-path build callback generates an actionlint-clean workflow.
	actionlint := locateActionlint(t)
	dir, wfDir := stageActionlintProject(t)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("staging", "production"),
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

	out, runErr := runActionlint(t, actionlint, path)
	if runErr != nil {
		t.Errorf("actionlint found errors in generated workflow:\n%s", out)
	}
}

// TestNormalizeWorkflowPath_ActionlintHermetic is a negative control that guards
// the hermeticity of the actionlint harness: it proves actionlint actually
// resolves and validates the local reusable-workflow references staged in the
// temp project tree, rather than silently skipping them. actionlint anchors its
// project root by the nearest ancestor .git directory; when the temp tree is not
// anchored it walks up to an unrelated repository (or finds none) and skips local
// reusable-workflow resolution entirely, so the staged sibling stubs are ignored
// and the uses: references go unvalidated. A clean actionlint run over a workflow
// that passes an input the callback does not declare therefore means the harness
// is not resolving siblings, which is the non-hermetic false-signal defect this
// test exists to catch.
func TestNormalizeWorkflowPath_ActionlintHermetic(t *testing.T) {
	actionlint := locateActionlint(t)
	_, wfDir := stageActionlintProject(t)

	broken := "name: orchestrate\n" +
		"on:\n" +
		"  push:\n" +
		"    branches: [main]\n" +
		"jobs:\n" +
		"  app:\n" +
		"    uses: ./.github/workflows/build.yaml\n" +
		"    with:\n" +
		"      environment: staging\n" +
		"      undefined_input: x\n"
	path := filepath.Join(wfDir, "orchestrate.yaml")
	if writeErr := os.WriteFile(path, []byte(broken), 0o644); writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}

	out, runErr := runActionlint(t, actionlint, path)
	if runErr == nil {
		t.Fatalf("expected actionlint to reject an undefined reusable-workflow input, but it "+
			"passed: the harness is not resolving sibling workflows (non-hermetic):\n%s", out)
	}
	if !strings.Contains(out, "undefined_input") {
		t.Errorf("actionlint failed for an unexpected reason; want an undefined-input error, got:\n%s", out)
	}
}

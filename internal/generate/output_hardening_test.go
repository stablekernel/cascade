package generate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The changelog written to $GITHUB_OUTPUT carries arbitrary commit-message
// text. With a fixed heredoc delimiter, a commit message containing a bare
// "EOF" line terminates the heredoc early and the remaining lines parse as
// forged step outputs (versions, refs) consumed by downstream jobs. Every
// generated heredoc must therefore mint a random delimiter at runtime.

// assertRandomizedOutputHeredoc asserts the generated step body writes its
// multiline output through a runtime-random delimiter rather than a fixed one.
func assertRandomizedOutputHeredoc(t *testing.T, content, key string) {
	t.Helper()
	assert.NotContains(t, content, key+"<<EOF",
		"fixed EOF delimiter is forgeable by a value containing a bare EOF line")
	assert.Contains(t, content, `CASCADE_DELIM="$(dd if=/dev/urandom bs=15 count=1 status=none | base64)"`,
		"heredoc delimiter must be minted at runtime from random bytes")
	assert.Contains(t, content, `echo "`+key+`<<${CASCADE_DELIM}"`,
		"heredoc must open with the random delimiter")
	assert.Contains(t, content, `echo "${CASCADE_DELIM}"`,
		"heredoc must close with the same random delimiter")
}

func TestOrchestrate_ChangelogHeredocRandomDelimiter(t *testing.T) {
	tmpDir := t.TempDir()
	writeStubWorkflow(t, tmpDir, "build.yaml")

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("dev"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: "build.yaml", Triggers: []string{"src/**"}},
		},
	}

	content, err := NewGenerator(cfg, tmpDir).Generate()
	require.NoError(t, err)
	assertRandomizedOutputHeredoc(t, content, "changelog")
}

func TestPromote_ChangelogHeredocRandomDelimiter(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("staging", "prod"),
	}

	content, err := NewPromoteGenerator(cfg, "").Generate()
	require.NoError(t, err)
	assertRandomizedOutputHeredoc(t, content, "changelog")
}

func TestRelease_ChangelogHeredocRandomDelimiter(t *testing.T) {
	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("prod"),
	}

	content, err := NewReleaseGenerator(cfg, "").Generate()
	require.NoError(t, err)
	assertRandomizedOutputHeredoc(t, content, "changelog")
}

// TestPRPreview_SummaryHeredocRandomDelimiter asserts the plan-summary body
// (which embeds manifest-derived names) is written to $GITHUB_OUTPUT through a
// runtime-random delimiter, not the fixed CASCADE_EOF marker.
func TestPRPreview_SummaryHeredocRandomDelimiter(t *testing.T) {
	content, err := NewPRPreviewGenerator(prPreviewConfig(true), "").Generate()
	require.NoError(t, err)

	assert.NotContains(t, content, "CASCADE_EOF",
		"fixed CASCADE_EOF delimiter is forgeable by a summary line")
	assertRandomizedOutputHeredoc(t, content, "body")
}

// TestPRPreview_CommentBodyRoutedThroughEnv asserts the github-script comment
// step reads the plan body from env rather than splicing the step output into
// the script source. GitHub expands ${{ }} before Node parses the script, so a
// spliced body containing a backtick or ${ breaks the step or executes
// injected script; env-routing is the pattern the sibling steps already use.
func TestPRPreview_CommentBodyRoutedThroughEnv(t *testing.T) {
	content, err := NewPRPreviewGenerator(prPreviewConfig(true), "").Generate()
	require.NoError(t, err)

	assert.Contains(t, content, "PLAN_BODY: ${{ steps.summary.outputs.body }}",
		"plan body must be bound via step env:")
	assert.Contains(t, content, "process.env.PLAN_BODY",
		"script must read the body from the environment")
	assert.NotContains(t, content, "`${{ steps.summary.outputs.body }}`",
		"step output must not be spliced into the script as a template literal")
}

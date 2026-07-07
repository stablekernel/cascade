package orchestrate

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/version"
)

func tgPtr(s string) *string { return &s }

// A manifest with a custom tag_grammar makes orchestrate calculate the next
// version in the custom shape, and that result is identical to the one the
// version command would compute from the same manifest (both resolve one spec).
func TestCalculateVersion_CustomTagGrammar(t *testing.T) {
	repoDir, head := initRepo(t)

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoDir))
	t.Cleanup(func() { require.NoError(t, os.Chdir(orig)) })

	cfg := &config.TrunkConfig{
		Environments: []string{"dev", "prod"},
		TagGrammar: &config.TagGrammarConfig{
			Prefix:          tgPtr("ver"),
			PreReleaseToken: tgPtr("beta"),
		},
	}
	o := &Orchestrator{
		environment: "dev",
		baseDir:     repoDir,
		cicdFile: &config.CICDFile{
			Config: cfg,
			State: map[string]*config.EnvState{
				"dev":  {Version: "v1.0.0-rc.0"},
				"prod": {Version: "v0.9.0", SHA: head},
			},
		},
	}

	got, err := o.calculateVersion()
	require.NoError(t, err)

	if !strings.HasPrefix(got, "ver") || !strings.Contains(got, "-beta.") {
		t.Fatalf("orchestrate did not honor tag_grammar: got %q, want ver...-beta. shape", got)
	}

	// The version command builds its calculator from the same resolved spec, so
	// it must produce byte-identical output for the same inputs.
	calc := version.NewCalculatorWithGrammar(cfg.ResolveTagGrammar())
	want, err := calc.CalculateNext("v1.0.0-rc.0", "v0.9.0", nil)
	require.NoError(t, err)
	assert.Equal(t, want.String(), got, "orchestrate and version command must resolve the same spec")
}

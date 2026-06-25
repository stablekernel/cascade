package simulate

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stablekernel/cascade/internal/promote"
)

var update = flag.Bool("update", false, "update golden files")

// knownResult builds a fixed simulation result for golden rendering.
func knownResult(t *testing.T) *Result {
	t.Helper()

	path := seedManifest(t)
	engine, err := NewEngine(path, WithActor("golden-actor"))
	require.NoError(t, err)
	result, err := engine.Simulate(NewPromoteAction(promote.ModeDefault, ""))
	require.NoError(t, err)
	return result
}

func TestRenderHuman_Golden(t *testing.T) {
	result := knownResult(t)

	var buf bytes.Buffer
	require.NoError(t, result.RenderHuman(&buf))

	goldenPath := filepath.Join("testdata", "promote_human.golden")
	checkGolden(t, goldenPath, buf.Bytes())
}

func TestRenderJSON_Golden(t *testing.T) {
	result := knownResult(t)

	var buf bytes.Buffer
	require.NoError(t, result.RenderJSON(&buf))

	goldenPath := filepath.Join("testdata", "promote_json.golden")
	checkGolden(t, goldenPath, buf.Bytes())
}

// checkGolden compares got against the golden file, rewriting it under -update.
func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()

	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}

	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden file missing; run with -update")
	require.Equal(t, string(want), string(got))
}

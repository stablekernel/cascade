package generate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanUsesForPinDrift_Exported(t *testing.T) {
	manifest := loadActionPinsManifestForTest(t)
	want := manifest[actionCheckout]
	const stale = "0000000000000000000000000000000000000000"
	fixture := "    steps:\n      - uses: actions/checkout@" + stale + " # " + want.Version + "\n"

	got := ScanUsesForPinDrift("f.yaml", fixture, manifest)
	require.Len(t, got, 1)
	require.Equal(t, actionCheckout, got[0].Action)
	require.Equal(t, stale, got[0].GotRef)
	require.Equal(t, want.SHA, got[0].WantSHA)
}

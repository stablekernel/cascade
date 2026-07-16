package git

import (
	"testing"

	"github.com/stablekernel/cascade/internal/taggrammar"
)

// TestGetLatestTagSpec_LeadingHyphenPrefixIsNotAFlag pins the argv boundary
// for tag discovery: a grammar prefix beginning with a hyphen must reach git
// as a pattern (after the "--" separator), never be parsed as a flag (which
// exits 129 with "unknown switch"). Manifest validation rejects such prefixes
// up front; this is the defense-in-depth layer beneath it.
func TestGetLatestTagSpec_LeadingHyphenPrefixIsNotAFlag(t *testing.T) {
	dir := t.TempDir()
	initRepoAt(t, dir, "v1.2.3")

	spec := taggrammar.Default()
	spec.Prefix = "-rc"

	if _, _, err := GetLatestTagSpec(dir, spec); err != nil {
		t.Fatalf("GetLatestTagSpec with a leading-hyphen prefix must treat it as a pattern, got error: %v", err)
	}
	if _, _, err := GetLatestReleaseTagSpec(dir, spec); err != nil {
		t.Fatalf("GetLatestReleaseTagSpec with a leading-hyphen prefix must treat it as a pattern, got error: %v", err)
	}
}

// TestGetLatestTagSpec_NormalPrefixStillMatches guards the separator change:
// adding "--" must not change tag discovery for ordinary prefixes.
func TestGetLatestTagSpec_NormalPrefixStillMatches(t *testing.T) {
	dir := t.TempDir()
	initRepoAt(t, dir, "v1.2.3")

	tag, _, err := GetLatestTagSpec(dir, taggrammar.Default())
	if err != nil {
		t.Fatalf("GetLatestTagSpec: %v", err)
	}
	if tag != "v1.2.3" {
		t.Fatalf("GetLatestTagSpec = %q, want v1.2.3", tag)
	}
}

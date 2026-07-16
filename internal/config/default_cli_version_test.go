package config

import (
	"regexp"
	"testing"
)

// stableTagPattern matches an immutable stable release tag (vX.Y.Z with no
// prerelease suffix). DefaultCLIVersion must always be one of these: it is
// baked into generated workflows as the setup-cli pin for every adopter that
// leaves cli_version unset, so a mutable ref or prerelease here weakens the
// supply-chain posture of all default configurations.
var stableTagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// TestDefaultCLIVersion_IsStableReleaseTag guards the shape of the default:
// a plain vX.Y.Z tag, never "latest", "beta", a branch name, or an rc/dryrun
// prerelease.
func TestDefaultCLIVersion_IsStableReleaseTag(t *testing.T) {
	if !stableTagPattern.MatchString(DefaultCLIVersion) {
		t.Fatalf("DefaultCLIVersion %q is not a stable vX.Y.Z release tag", DefaultCLIVersion)
	}
}

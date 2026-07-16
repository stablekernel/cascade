package config

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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

// TestDefaultCLIVersion_MatchesLatestReleaseTag guards against the default
// silently rotting behind the released CLI. It shipped as v0.1.0 for fifteen
// releases, pinning every default adopter to a pre-hardening CLI.
//
// The expectation is derived from the repository's own stable tags, so there
// is no network dependency: each release must bump DefaultCLIVersion in the
// same body of work that cuts the tag (see CONTRIBUTING.md). Shallow CI
// checkouts without tags skip; any full clone (every local inner loop and
// fetch-depth: 0 workflow) enforces the match.
func TestDefaultCLIVersion_MatchesLatestReleaseTag(t *testing.T) {
	out, err := exec.Command("git", "tag", "--list", "v*").Output()
	if err != nil {
		t.Skipf("git tags unavailable (%v); cannot derive the latest release tag", err)
	}

	latest := ""
	var latestParts [3]int
	for _, tag := range strings.Fields(string(out)) {
		m := stableTagPattern.FindStringSubmatch(tag)
		if m == nil {
			continue // prerelease (rc/dryrun) or foreign tag
		}
		var parts [3]int
		for i := 0; i < 3; i++ {
			parts[i], _ = strconv.Atoi(m[i+1])
		}
		if latest == "" || parts[0] > latestParts[0] ||
			(parts[0] == latestParts[0] && (parts[1] > latestParts[1] ||
				(parts[1] == latestParts[1] && parts[2] > latestParts[2]))) {
			latest, latestParts = tag, parts
		}
	}
	if latest == "" {
		t.Skip("no stable release tags visible (shallow checkout); cannot derive the latest release tag")
	}

	if DefaultCLIVersion != latest {
		t.Fatalf("DefaultCLIVersion %q lags the latest stable release tag %q; bump the const in internal/config/types.go and regenerate the byte-identical baseline goldens (go test ./internal/generate/ -run TestByteIdenticalBaseline -update)",
			DefaultCLIVersion, latest)
	}
}

// Package version provides semantic versioning utilities for release management.
package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/stablekernel/cascade/internal/changelog"
)

// Version represents a semantic version with optional pre-release suffix
type Version struct {
	Major      int
	Minor      int
	Patch      int
	PreRelease int    // -1 means no pre-release suffix, >= 0 is the RC number
	Hotfix     int    // -1 means no hotfix segment, >= 0 is the hotfix number
	Prefix     string // e.g., "v" or custom prefix
}

// BumpType represents the type of version bump
type BumpType int

const (
	BumpNone BumpType = iota
	BumpPatch
	BumpMinor
	BumpMajor
)

// semverRegex matches versions like v1.2.3, v1.2.3-rc.4, or v1.2.3-rc.4.hotfix.5.
// The hotfix segment is only valid nested after an rc segment.
var semverRegex = regexp.MustCompile(`^([a-zA-Z]*)(\d+)\.(\d+)\.(\d+)(?:-rc\.(\d+)(?:\.hotfix\.(\d+))?)?$`)

// baseVersionRegex matches a semver core (vX.Y.Z) with any optional
// pre-release suffix (for example -rc.4, -dryrun.13, or -beta.1). Only the
// numeric core and prefix are captured; the suffix is intentionally ignored.
var baseVersionRegex = regexp.MustCompile(`^([a-zA-Z]*)(\d+)\.(\d+)\.(\d+)(?:-.+)?$`)

// rcSuffixRegex captures the rc number from a version whose core is immediately
// followed by an -rc.N segment, tolerating any trailing exercise suffix (for
// example -rc.4.hotfix.5 or -rc.4.dryrun.1). It is anchored only at the start so
// a foreign suffix such as -beta.1 or -dryrun.4 simply yields no match.
var rcSuffixRegex = regexp.MustCompile(`^[a-zA-Z]*\d+\.\d+\.\d+-rc\.(\d+)`)

// extractRC returns the rc number embedded in a version string, or -1 when the
// string has no -rc.N segment directly after its numeric core. It tolerates
// trailing suffixes the strict Parse rejects so a recorded dev version can still
// advance its rc counter instead of silently resetting to rc.0.
func extractRC(s string) int {
	matches := rcSuffixRegex.FindStringSubmatch(s)
	if matches == nil {
		return -1
	}
	rc, _ := strconv.Atoi(matches[1])
	return rc
}

// ParseBase parses the numeric core (prefix and major.minor.patch) of a version
// string, tolerating and discarding any pre-release suffix such as an -rc.N,
// -dryrun.N, or other exercise tag that the strict Parse rejects. The returned
// Version always has no pre-release or hotfix segment. It errors only when the
// core itself is not a valid vX.Y.Z triple. Version calculations that derive
// their next version solely from a base can use this so a stray suffixed value
// recorded as the latest does not abort the whole calculation.
func ParseBase(s string) (*Version, error) {
	matches := baseVersionRegex.FindStringSubmatch(s)
	if matches == nil {
		return nil, fmt.Errorf("invalid version format: %s", s)
	}

	major, _ := strconv.Atoi(matches[2])
	minor, _ := strconv.Atoi(matches[3])
	patch, _ := strconv.Atoi(matches[4])

	return &Version{
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		PreRelease: -1,
		Hotfix:     -1,
		Prefix:     matches[1],
	}, nil
}

// Parse parses a version string into a Version struct
func Parse(s string) (*Version, error) {
	matches := semverRegex.FindStringSubmatch(s)
	if matches == nil {
		return nil, fmt.Errorf("invalid version format: %s", s)
	}

	major, _ := strconv.Atoi(matches[2])
	minor, _ := strconv.Atoi(matches[3])
	patch, _ := strconv.Atoi(matches[4])

	preRelease := -1
	if matches[5] != "" {
		preRelease, _ = strconv.Atoi(matches[5])
	}

	hotfix := -1
	if matches[6] != "" {
		hotfix, _ = strconv.Atoi(matches[6])
	}

	return &Version{
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		PreRelease: preRelease,
		Hotfix:     hotfix,
		Prefix:     matches[1],
	}, nil
}

// String returns the version as a string
func (v *Version) String() string {
	base := fmt.Sprintf("%s%d.%d.%d", v.Prefix, v.Major, v.Minor, v.Patch)
	if v.PreRelease >= 0 {
		rc := fmt.Sprintf("%s-rc.%d", base, v.PreRelease)
		if v.Hotfix >= 0 {
			return fmt.Sprintf("%s.hotfix.%d", rc, v.Hotfix)
		}
		return rc
	}
	return base
}

// Base returns the version without pre-release suffix
func (v *Version) Base() string {
	return fmt.Sprintf("%s%d.%d.%d", v.Prefix, v.Major, v.Minor, v.Patch)
}

// BaseVersion returns a copy without pre-release suffix
func (v *Version) BaseVersion() *Version {
	return &Version{
		Major:      v.Major,
		Minor:      v.Minor,
		Patch:      v.Patch,
		PreRelease: -1,
		Hotfix:     -1,
		Prefix:     v.Prefix,
	}
}

// WithRC returns a copy with the specified RC number
func (v *Version) WithRC(rc int) *Version {
	return &Version{
		Major:      v.Major,
		Minor:      v.Minor,
		Patch:      v.Patch,
		PreRelease: rc,
		Hotfix:     -1,
		Prefix:     v.Prefix,
	}
}

// Bump returns a new version with the specified bump applied
func (v *Version) Bump(bump BumpType) *Version {
	result := &Version{
		Major:      v.Major,
		Minor:      v.Minor,
		Patch:      v.Patch,
		PreRelease: -1,
		Hotfix:     -1,
		Prefix:     v.Prefix,
	}

	switch bump {
	case BumpMajor:
		result.Major++
		result.Minor = 0
		result.Patch = 0
	case BumpMinor:
		result.Minor++
		result.Patch = 0
	case BumpPatch:
		result.Patch++
	}

	return result
}

// Equal returns true if two versions have the same major.minor.patch (ignoring RC)
func (v *Version) Equal(other *Version) bool {
	if other == nil {
		return false
	}
	return v.Major == other.Major && v.Minor == other.Minor && v.Patch == other.Patch
}

// DetermineBumpType analyzes commits and returns the highest bump type needed
func DetermineBumpType(commits []changelog.ConventionalCommit) BumpType {
	bump := BumpNone

	for _, c := range commits {
		if c.Breaking {
			return BumpMajor // Can't get higher than this
		}

		commitType := strings.ToLower(c.Type)
		switch commitType {
		case "feat":
			if bump < BumpMinor {
				bump = BumpMinor
			}
		case "fix":
			if bump < BumpPatch {
				bump = BumpPatch
			}
		}
	}

	return bump
}

// Calculator handles version calculation for the release workflow
type Calculator struct {
	prefix string
}

// NewCalculator creates a new version calculator
func NewCalculator(prefix string) *Calculator {
	if prefix == "" {
		prefix = "v"
	}
	return &Calculator{prefix: prefix}
}

// CalculateNext determines the next version for the lowest environment
// Parameters:
//   - currentDevVersion: current version in dev (may be empty or same as nextEnvVersion after promotion)
//   - nextEnvVersion: version in the next environment (e.g., test)
//   - commits: conventional commits between nextEnv's SHA and HEAD
//
// Returns the calculated version with appropriate RC suffix
func (c *Calculator) CalculateNext(currentDevVersion, nextEnvVersion string, commits []changelog.ConventionalCommit) (*Version, error) {
	// Parse next env's version as our base
	var baseVersion *Version
	if nextEnvVersion == "" {
		// No version in next env - start at v0.0.0, bump will bring it up
		baseVersion = &Version{
			Major:      0,
			Minor:      0,
			Patch:      0,
			PreRelease: -1,
			Hotfix:     -1,
			Prefix:     c.prefix,
		}
	} else {
		// Only the numeric core of the next env's version feeds the
		// calculation (see BaseVersion below), so tolerate any pre-release
		// suffix here. A stray -dryrun.N or -rc.N value recorded as the latest
		// must not abort the calculation, matching the discovery-side filtering
		// that keeps such exercise tags out of tag lookups.
		var err error
		baseVersion, err = ParseBase(nextEnvVersion)
		if err != nil {
			return nil, fmt.Errorf("parsing next env version: %w", err)
		}
	}

	// Determine bump type from commits
	bumpType := DetermineBumpType(commits)

	// If no commits or no significant changes, default to patch for new work
	if bumpType == BumpNone && len(commits) > 0 {
		bumpType = BumpPatch
	}

	// Calculate the new version
	newVersion := baseVersion.BaseVersion().Bump(bumpType)
	newVersion.Prefix = c.prefix

	// Ensure minimum version of v0.1.0 (v0.0.x is not valid for releases)
	if newVersion.Major == 0 && newVersion.Minor == 0 {
		newVersion.Minor = 1
		newVersion.Patch = 0
	}

	// Determine RC number
	if currentDevVersion == "" || currentDevVersion == nextEnvVersion {
		// After promotion or first release - start at RC 0
		newVersion.PreRelease = 0
	} else {
		// Compare on the numeric base and extract any rc number explicitly so a
		// foreign prerelease or exercise suffix the strict Parse rejects (for
		// example -beta.1, -dryrun.4, or -rc.4.dryrun.1) does not silently reset
		// the rc counter and collide with an already-published rc tag. This
		// mirrors the tolerant handling applied to nextEnvVersion above.
		currentBase, err := ParseBase(currentDevVersion)
		switch {
		case err != nil:
			// Base itself is unparseable, so start fresh.
			newVersion.PreRelease = 0
		case newVersion.Equal(currentBase):
			// Same base version, so increment off the recorded rc. extractRC
			// returns -1 when the dev version has no rc segment, yielding a
			// fresh rc.0.
			newVersion.PreRelease = extractRC(currentDevVersion) + 1
		default:
			// Different base version, so start at rc.0.
			newVersion.PreRelease = 0
		}
	}

	return newVersion, nil
}

// GetLatestRelease returns the latest published (non-prerelease) version from a list of tags
func GetLatestRelease(tags []string) (*Version, error) {
	var latest *Version

	for _, tag := range tags {
		v, err := Parse(tag)
		if err != nil {
			continue // Skip non-semver tags
		}

		// Skip pre-releases
		if v.PreRelease >= 0 {
			continue
		}

		if latest == nil {
			latest = v
			continue
		}

		// Compare versions
		if v.Major > latest.Major ||
			(v.Major == latest.Major && v.Minor > latest.Minor) ||
			(v.Major == latest.Major && v.Minor == latest.Minor && v.Patch > latest.Patch) {
			latest = v
		}
	}

	if latest == nil {
		return nil, fmt.Errorf("no published releases found")
	}

	return latest, nil
}

// WithHotfix returns a copy of the version with the given hotfix number,
// preserving the major, minor, patch, pre-release, and prefix.
func (v *Version) WithHotfix(m int) *Version {
	return &Version{
		Major:      v.Major,
		Minor:      v.Minor,
		Patch:      v.Patch,
		PreRelease: v.PreRelease,
		Hotfix:     m,
		Prefix:     v.Prefix,
	}
}

// NextHotfix returns a copy of the version with its hotfix number incremented.
// If the version has no hotfix segment yet, the result is hotfix 1, so an
// rc.2 version becomes rc.2.hotfix.1.
func (v *Version) NextHotfix() *Version {
	next := 1
	if v.Hotfix >= 0 {
		next = v.Hotfix + 1
	}
	return v.WithHotfix(next)
}

// Compare returns -1, 0, or +1 reporting whether v sorts before, equal to, or
// after other under semver precedence. It compares major, minor, and patch
// numerically; then a version with a pre-release sorts before one without; then
// pre-release numbers compare numerically; then a version without a hotfix
// segment sorts before one with a hotfix; then hotfix numbers compare
// numerically.
func (v *Version) Compare(other *Version) int {
	if c := compareInt(v.Major, other.Major); c != 0 {
		return c
	}
	if c := compareInt(v.Minor, other.Minor); c != 0 {
		return c
	}
	if c := compareInt(v.Patch, other.Patch); c != 0 {
		return c
	}

	// A pre-release version has lower precedence than the associated release.
	vPre := v.PreRelease >= 0
	oPre := other.PreRelease >= 0
	switch {
	case vPre && !oPre:
		return -1
	case !vPre && oPre:
		return 1
	case !vPre && !oPre:
		return 0
	}

	if c := compareInt(v.PreRelease, other.PreRelease); c != 0 {
		return c
	}

	// No-hotfix sorts before any hotfix on the same rc.
	vHot := v.Hotfix >= 0
	oHot := other.Hotfix >= 0
	switch {
	case !vHot && oHot:
		return -1
	case vHot && !oHot:
		return 1
	case !vHot && !oHot:
		return 0
	}

	return compareInt(v.Hotfix, other.Hotfix)
}

// compareInt returns -1, 0, or +1 reporting whether a is less than, equal to,
// or greater than b.
func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// StripRC removes the pre-release suffix for publishing
func StripRC(version string) (string, error) {
	v, err := Parse(version)
	if err != nil {
		return "", err
	}
	return v.Base(), nil
}

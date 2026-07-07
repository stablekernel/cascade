// Package version provides semantic versioning utilities for release management.
package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/stablekernel/cascade/internal/changelog"
	"github.com/stablekernel/cascade/internal/taggrammar"
)

// defaultSpec is cascade's historical tag grammar. The package-level Parse,
// ParseBase, and String helpers read from it so their behavior stays identical
// to the hand-written regexes they replaced, while the grammar itself lives in
// one place.
var defaultSpec = taggrammar.Default()

// prefixPattern returns the regex fragment matching a tag prefix under spec: the
// literal prefix when StrictPrefix is set, otherwise any alphabetic run so
// historical and foreign-cased tags still parse. It mirrors the read side of the
// canonical grammar.
func prefixPattern(spec taggrammar.Spec) string {
	if spec.StrictPrefix {
		return regexp.QuoteMeta(spec.Prefix)
	}
	return "[a-zA-Z]*"
}

// Version represents a semantic version with optional pre-release suffix
type Version struct {
	Major      int
	Minor      int
	Patch      int
	PreRelease int    // -1 means no pre-release suffix, >= 0 is the RC number
	Hotfix     int    // -1 means no hotfix segment, >= 0 is the hotfix number
	Prefix     string // e.g., "v" or custom prefix

	// grammarSpec, when set, names the tag grammar this version renders under.
	// It is nil for versions built under the default grammar, which keeps the
	// zero value and every literal-constructed Version rendering identically to
	// before. Only versions produced under a non-default grammar carry one.
	grammarSpec *taggrammar.Spec
}

// activeSpec returns the tag grammar this version renders under: its own when
// set, otherwise the default grammar.
func (v *Version) activeSpec() taggrammar.Spec {
	if v.grammarSpec != nil {
		return *v.grammarSpec
	}
	return defaultSpec
}

// BumpType represents the type of version bump
type BumpType int

const (
	BumpNone BumpType = iota
	BumpPatch
	BumpMinor
	BumpMajor
)

// baseRegex matches a semver core (vX.Y.Z) under spec with any optional
// pre-release suffix (for example -rc.4, -dryrun.13, or -beta.1). Only the
// numeric core and prefix are captured; the suffix is intentionally ignored.
// The prefix fragment comes from the spec so a custom prefix widens or narrows
// the accepted set consistently with the strict parser.
func baseRegex(spec taggrammar.Spec) *regexp.Regexp {
	// [-+].+ tolerates either a pre-release suffix (-rc.4, -beta.1) or bare build
	// metadata (+build.5) after the core. Both are discarded; only the numeric
	// core and prefix are captured.
	return regexp.MustCompile(fmt.Sprintf(`^(%s)(\d+)\.(\d+)\.(\d+)(?:[-+].+)?$`, prefixPattern(spec)))
}

// tolerantReadRegex matches a version tag on the read side, accepting shapes the
// strict emit grammar rejects: any foreign pre-release identifier after the core
// (for example -beta.1 or -rc1) and optional +build metadata. Group 5 captures
// the pre-release identifier (empty when absent); build metadata is matched but
// never captured, so it is discarded. This is deliberately distinct from the
// strict grammar so discovery can see historical tags without cascade emitting
// them.
func tolerantReadRegex(spec taggrammar.Spec) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(
		`^(%s)(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`,
		prefixPattern(spec)))
}

// ParseTolerant parses s on the read side under the default grammar, tolerating
// a foreign pre-release identifier and discarding build metadata. See
// ParseTolerantWithGrammar for the full contract.
func ParseTolerant(s string) (*Version, error) {
	return ParseTolerantWithGrammar(defaultSpec, s)
}

// ParseTolerantWithGrammar parses s on the read side under spec. It recognizes
// the numeric core plus an optional foreign pre-release identifier and optional
// build metadata. A recognized pre-release is marked present (PreRelease >= 0) so
// it sorts below its release; the specific identifier is not interpreted, since
// the read-side contract is only "any pre-release sorts below the release" plus
// cascade's own rc counter, which the calculator handles separately. Build
// metadata is discarded and never stored, so it is never emitted. It errors only
// when s lacks a valid numeric core.
func ParseTolerantWithGrammar(spec taggrammar.Spec, s string) (*Version, error) {
	m := tolerantReadRegex(spec).FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("invalid version format: %s", s)
	}

	major, _ := strconv.Atoi(m[2])
	minor, _ := strconv.Atoi(m[3])
	patch, _ := strconv.Atoi(m[4])

	preRelease := -1
	if m[5] != "" {
		// A foreign pre-release identifier is marked present so it sorts below
		// the release. Its value is not interpreted (see the contract above).
		preRelease = 0
	}

	return &Version{
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		PreRelease: preRelease,
		Hotfix:     -1,
		Prefix:     leadingPrefix(s),
	}, nil
}

// preReleaseSuffixRegex captures the pre-release number from a version whose
// core is immediately followed by the spec's pre-release segment, tolerating any
// trailing exercise suffix (for example -rc.4.hotfix.5 or -rc.4.dryrun.1). It is
// anchored only at the start so a foreign suffix such as -beta.1 or -dryrun.4
// simply yields no match. Token and separator both come from the spec.
func preReleaseSuffixRegex(spec taggrammar.Spec) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(
		`^%s\d+\.\d+\.\d+-%s%s(\d+)`,
		prefixPattern(spec),
		regexp.QuoteMeta(spec.PreReleaseToken),
		regexp.QuoteMeta(spec.PreReleaseSeparator),
	))
}

// extractRCWithGrammar returns the pre-release number embedded in s under spec,
// or -1 when s has no pre-release segment directly after its numeric core. It
// tolerates trailing suffixes the strict Parse rejects so a recorded dev version
// can still advance its counter instead of silently resetting to zero.
func extractRCWithGrammar(spec taggrammar.Spec, s string) int {
	matches := preReleaseSuffixRegex(spec).FindStringSubmatch(s)
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
	return ParseBaseWithGrammar(defaultSpec, s)
}

// ParseBaseWithGrammar parses the numeric core of s under spec, tolerating and
// discarding any pre-release suffix. See ParseBase for the full contract; this
// form lets callers supply a non-default grammar.
func ParseBaseWithGrammar(spec taggrammar.Spec, s string) (*Version, error) {
	matches := baseRegex(spec).FindStringSubmatch(s)
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

// Parse parses a version string into a Version struct under the default grammar.
func Parse(s string) (*Version, error) {
	return ParseWithGrammar(defaultSpec, s)
}

// ParseWithGrammar parses s into a Version under spec. The numeric fields come
// from the canonical grammar so the two never drift; the prefix is the literal
// run the tag leads with, which the grammar has already validated.
func ParseWithGrammar(spec taggrammar.Spec, s string) (*Version, error) {
	p, ok := spec.Parse(s)
	if !ok {
		return nil, fmt.Errorf("invalid version format: %s", s)
	}

	return &Version{
		Major:      p.Major,
		Minor:      p.Minor,
		Patch:      p.Patch,
		PreRelease: p.PreRelease,
		Hotfix:     p.Hotfix,
		Prefix:     leadingPrefix(s),
	}, nil
}

// leadingPrefix returns the run of characters before the first digit in s, which
// for a validated version tag is exactly its prefix ("v", "release", or empty).
func leadingPrefix(s string) string {
	i := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return s
	}
	return s[:i]
}

// String returns the version as a string. The prefix and numeric core come from
// the version's own fields; the pre-release token and separator come from its
// grammar, so a custom grammar renders its own shape while the default renders
// the historical "-rc." form.
func (v *Version) String() string {
	spec := v.activeSpec()
	base := fmt.Sprintf("%s%d.%d.%d", v.Prefix, v.Major, v.Minor, v.Patch)
	if v.PreRelease >= 0 {
		rc := fmt.Sprintf("%s-%s%s%d", base, spec.PreReleaseToken, spec.PreReleaseSeparator, v.PreRelease)
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

// WithGrammar returns a copy of v that renders under spec, so a version parsed
// with ParseWithGrammar carries its grammar through subsequent copy operations
// (WithHotfix, WithRC, Bump) and renders its configured pre-release shape. The
// zero/default grammar is preserved as-is, so a default version is unchanged.
func (v *Version) WithGrammar(spec taggrammar.Spec) *Version {
	out := *v
	s := spec
	out.grammarSpec = &s
	return &out
}

// WithRC returns a copy with the specified RC number
func (v *Version) WithRC(rc int) *Version {
	return &Version{
		Major:       v.Major,
		Minor:       v.Minor,
		Patch:       v.Patch,
		PreRelease:  rc,
		Hotfix:      -1,
		Prefix:      v.Prefix,
		grammarSpec: v.grammarSpec,
	}
}

// Bump returns a new version with the specified bump applied
func (v *Version) Bump(bump BumpType) *Version {
	result := &Version{
		Major:       v.Major,
		Minor:       v.Minor,
		Patch:       v.Patch,
		PreRelease:  -1,
		Hotfix:      -1,
		Prefix:      v.Prefix,
		grammarSpec: v.grammarSpec,
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
	spec taggrammar.Spec
}

// NewCalculator creates a new version calculator for the default grammar with a
// custom prefix. An empty prefix defaults to "v".
func NewCalculator(prefix string) *Calculator {
	if prefix == "" {
		prefix = "v"
	}
	spec := taggrammar.Default()
	spec.Prefix = prefix
	return &Calculator{spec: spec}
}

// NewCalculatorWithGrammar creates a version calculator that emits tags in the
// shape described by spec, so a caller with a non-default token, separator, or
// prefix gets a matching next version.
func NewCalculatorWithGrammar(spec taggrammar.Spec) *Calculator {
	return &Calculator{spec: spec}
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
			Prefix:     c.spec.Prefix,
		}
	} else {
		// Only the numeric core of the next env's version feeds the
		// calculation (see BaseVersion below), so tolerate any pre-release
		// suffix here. A stray -dryrun.N or -rc.N value recorded as the latest
		// must not abort the calculation, matching the discovery-side filtering
		// that keeps such exercise tags out of tag lookups.
		var err error
		baseVersion, err = ParseBaseWithGrammar(c.spec, nextEnvVersion)
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
	newVersion.Prefix = c.spec.Prefix

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
		currentBase, err := ParseBaseWithGrammar(c.spec, currentDevVersion)
		switch {
		case err != nil:
			// Base itself is unparseable, so start fresh.
			newVersion.PreRelease = 0
		case newVersion.Equal(currentBase):
			// Same base version, so increment off the recorded rc.
			// extractRCWithGrammar returns -1 when the dev version has no
			// pre-release segment, yielding a fresh rc.0.
			newVersion.PreRelease = extractRCWithGrammar(c.spec, currentDevVersion) + 1
		default:
			// Different base version, so start at rc.0.
			newVersion.PreRelease = 0
		}
	}

	// Stamp the grammar so the result renders in the calculator's shape.
	spec := c.spec
	newVersion.grammarSpec = &spec

	return newVersion, nil
}

// GetLatestRelease returns the latest published (non-prerelease) version from a list of tags
func GetLatestRelease(tags []string) (*Version, error) {
	var latest *Version

	for _, tag := range tags {
		// Read tolerantly so a repo whose history carries a foreign pre-release
		// shape or build metadata is still visible to discovery. Build metadata
		// is discarded to the numeric core; a recognized pre-release is skipped
		// just like a native -rc tag.
		v, err := ParseTolerant(tag)
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
		Major:       v.Major,
		Minor:       v.Minor,
		Patch:       v.Patch,
		PreRelease:  v.PreRelease,
		Hotfix:      m,
		Prefix:      v.Prefix,
		grammarSpec: v.grammarSpec,
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

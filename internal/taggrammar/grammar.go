// Package taggrammar is the single source of truth for the shape of cascade's
// own release tags. It is intentionally dependency-free (stdlib only) so that
// both version discovery and git tagging can share one grammar without pulling
// in cascade internals.
package taggrammar

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Spec describes the shape of a release tag. The zero value is not usable;
// construct a Spec with Default and override individual fields as needed.
// Every field defaults to cascade's historical grammar.
type Spec struct {
	// Prefix is the literal string that leads a tag, historically "v".
	Prefix string
	// PreReleaseToken names a pre-release, historically "rc".
	PreReleaseToken string
	// PreReleaseSeparator sits between the token and its number,
	// historically ".".
	PreReleaseSeparator string
	// DryRunToken names a rehearsal tag, historically "dryrun". Rehearsal
	// tags are deliberately not version-parseable so they stay invisible to
	// version discovery.
	DryRunToken string
	// StrictPrefix, when true, reads the prefix literally. When false the
	// read side accepts any alphabetic prefix so historical and foreign-cased
	// tags still parse.
	StrictPrefix bool
}

// Default returns the historical cascade grammar: v-prefixed semantic versions
// with "rc." pre-releases and "dryrun" rehearsal tags.
func Default() Spec {
	return Spec{
		Prefix:              "v",
		PreReleaseToken:     "rc",
		PreReleaseSeparator: ".",
		DryRunToken:         "dryrun",
	}
}

// prefixPattern returns the regex fragment used to match the tag prefix. With
// StrictPrefix the configured prefix is matched literally; otherwise any
// alphabetic run is accepted so the read side stays permissive.
func (s Spec) prefixPattern() string {
	if s.StrictPrefix {
		return regexp.QuoteMeta(s.Prefix)
	}
	return "[a-zA-Z]*"
}

// versionRegex compiles the anchored pattern that matches a version-parseable
// tag: prefix, numeric core, an optional pre-release using the configured token
// and separator, and an optional nested ".hotfix.<n>".
func (s Spec) versionRegex() *regexp.Regexp {
	pattern := fmt.Sprintf(
		`^(%s)(\d+)\.(\d+)\.(\d+)(?:-%s%s(\d+)(?:\.hotfix\.(\d+))?)?$`,
		s.prefixPattern(),
		regexp.QuoteMeta(s.PreReleaseToken),
		regexp.QuoteMeta(s.PreReleaseSeparator),
	)
	return regexp.MustCompile(pattern)
}

// IsVersionTag reports whether tag is a version-parseable tag under this Spec.
// Rehearsal (dryrun) tags and foreign tags return false so they stay invisible
// to version discovery.
func (s Spec) IsVersionTag(tag string) bool {
	return s.versionRegex().MatchString(tag)
}

// Parsed holds the numeric fields of a version tag. PreRelease and Hotfix use
// -1 to mean absent, matching the convention used elsewhere in cascade.
type Parsed struct {
	Major      int
	Minor      int
	Patch      int
	PreRelease int
	Hotfix     int
}

// Parse extracts the numeric fields of tag under this Spec. It returns ok=false
// when tag is not a version tag. An absent pre-release or hotfix is reported as
// -1.
func (s Spec) Parse(tag string) (Parsed, bool) {
	m := s.versionRegex().FindStringSubmatch(tag)
	if m == nil {
		return Parsed{}, false
	}
	// Submatch layout: [0] whole, [1] prefix, [2] major, [3] minor,
	// [4] patch, [5] pre-release, [6] hotfix.
	p := Parsed{
		Major:      atoi(m[2]),
		Minor:      atoi(m[3]),
		Patch:      atoi(m[4]),
		PreRelease: -1,
		Hotfix:     -1,
	}
	if m[5] != "" {
		p.PreRelease = atoi(m[5])
	}
	if m[6] != "" {
		p.Hotfix = atoi(m[6])
	}
	return p, true
}

// Format renders p under this Spec. It always emits the prefix and numeric
// core, appends "-<token><separator><n>" when PreRelease is present, and
// appends ".hotfix.<m>" when Hotfix is present.
func (s Spec) Format(p Parsed) string {
	out := fmt.Sprintf("%s%d.%d.%d", s.Prefix, p.Major, p.Minor, p.Patch)
	if p.PreRelease >= 0 {
		out += fmt.Sprintf("-%s%s%d", s.PreReleaseToken, s.PreReleaseSeparator, p.PreRelease)
	}
	if p.Hotfix >= 0 {
		out += fmt.Sprintf(".hotfix.%d", p.Hotfix)
	}
	return out
}

// StripPreRelease returns tag with its pre-release segment (and anything after
// it, such as a nested hotfix) removed, leaving just the prefix and numeric
// core. It cuts at this spec's pre-release marker ("-" + token + separator), so
// the default grammar cuts at "-rc." exactly as the historical literal strip
// did, byte for byte. A tag without the marker is returned unchanged.
func (s Spec) StripPreRelease(tag string) string {
	marker := "-" + s.PreReleaseToken + s.PreReleaseSeparator
	if idx := strings.Index(tag, marker); idx != -1 {
		return tag[:idx]
	}
	return tag
}

// PreReleaseStripSedBRE returns the anchored sed basic-regular-expression that
// matches this spec's pre-release segment at the end of a version string, for
// example "-rc\.[0-9]*$" for the default grammar and "-beta[0-9]*$" for a beta
// token with an empty separator. Both the token and the separator are
// escaped, so a metacharacter such as "." (a value the config allowlist
// otherwise permits in either field) matches literally instead of as a
// wildcard. Code generators bake this into the driven repo's release
// workflow so the emitted strip follows the configured grammar.
func (s Spec) PreReleaseStripSedBRE() string {
	return "-" + sedBREEscape(s.PreReleaseToken) + sedBREEscape(s.PreReleaseSeparator) + "[0-9]*$"
}

// sedBREEscape backslash-escapes the characters that carry special meaning in a
// sed basic regular expression so a literal separator matches literally. The
// backslash is escaped first so it is not doubled by a later pass.
func sedBREEscape(s string) string {
	const special = `\.[]*^$`
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// atoi converts a submatch known to be all digits. The grammar guarantees the
// input is non-empty and numeric, so strconv.ErrSyntax is unreachable and the
// only error left is strconv.ErrRange, from a run of digits too large for an
// int. Atoi saturates on that error, returning MaxInt (not 0), and the error is
// discarded.
//
// Saturating is the safe direction for cascade's readers. The reaper deletes a
// tag or draft only when its RC number is strictly LOWER than the one being
// published, so a saturated number compares as greater and the tag is preserved
// rather than reaped. An absurd tag is therefore left alone instead of being
// deleted or dragging siblings down with it.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

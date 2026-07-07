// Package taggrammar is the single source of truth for the shape of cascade's
// own release tags. It is intentionally dependency-free (stdlib only) so that
// both version discovery and git tagging can share one grammar without pulling
// in cascade internals.
package taggrammar

import (
	"fmt"
	"regexp"
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

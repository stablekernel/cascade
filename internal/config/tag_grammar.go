package config

import "github.com/stablekernel/cascade/internal/taggrammar"

// TagGrammarConfig is the optional, additive manifest block that reshapes the
// release tag grammar. Every field is a pointer so an omitted key is
// distinguishable from an explicit empty value and inherits cascade's historical
// default. The block is scoped to tag shape only; sibling concerns such as
// version constraints land as their own optional blocks rather than folding in
// here, so this stays a focused, non-breaking seam.
type TagGrammarConfig struct {
	Prefix              *string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	PreReleaseToken     *string `yaml:"prerelease_token,omitempty" json:"prerelease_token,omitempty"`
	PreReleaseSeparator *string `yaml:"prerelease_separator,omitempty" json:"prerelease_separator,omitempty"`
	DryRunToken         *string `yaml:"dryrun_token,omitempty" json:"dryrun_token,omitempty"`
	StrictPrefix        bool    `yaml:"strict_prefix,omitempty" json:"strict_prefix,omitempty"`
}

// ResolveTagGrammar folds the manifest's tag configuration into a single
// taggrammar.Spec. It starts from the historical default, applies the legacy
// tag_prefix, then layers any tag_grammar block on top. With no tag_grammar
// block and no tag_prefix the result is byte-identical to taggrammar.Default,
// so default behavior is unchanged. When both tag_prefix and tag_grammar.prefix
// are set the block wins; TagGrammarWarnings surfaces the redundancy.
func (c *TrunkConfig) ResolveTagGrammar() taggrammar.Spec {
	spec := taggrammar.Default()
	if c.TagPrefix != "" {
		spec.Prefix = c.TagPrefix
	}
	g := c.TagGrammar
	if g == nil {
		return spec
	}
	if g.Prefix != nil {
		spec.Prefix = *g.Prefix
	}
	if g.PreReleaseToken != nil {
		spec.PreReleaseToken = *g.PreReleaseToken
	}
	if g.PreReleaseSeparator != nil {
		spec.PreReleaseSeparator = *g.PreReleaseSeparator
	}
	if g.DryRunToken != nil {
		spec.DryRunToken = *g.DryRunToken
	}
	spec.StrictPrefix = g.StrictPrefix
	return spec
}

// TagGrammarWarnings returns non-fatal advisories about the tag configuration.
// Today it flags the one redundant case: setting both tag_prefix and
// tag_grammar.prefix. Resolution is well defined (the block wins), so this is an
// advisory, not an error, emitted on the same warnings path as the schema
// version advisory.
func (c *TrunkConfig) TagGrammarWarnings() []string {
	if c.TagPrefix != "" && c.TagGrammar != nil && c.TagGrammar.Prefix != nil {
		return []string{
			"both tag_prefix and tag_grammar.prefix are set; tag_grammar.prefix wins. " +
				"Drop tag_prefix to remove this ambiguity.",
		}
	}
	return nil
}

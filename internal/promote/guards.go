package promote

import (
	"fmt"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stablekernel/cascade/internal/git"
)

// AncestorFunc reports whether ancestor is contained in (is an ancestor of)
// descendant. It mirrors git.IsAncestor and exists so the divergence guards can
// be exercised deterministically in tests without a real object store.
type AncestorFunc func(ancestor, descendant string) (bool, error)

// Option customizes optional, additive behavior on a Promoter or Preflighter.
// Required inputs stay positional on the constructor; cross-cutting concerns
// (such as the git-ancestry checker) are threaded through options so new
// capability never changes an existing signature.
type Option func(*guardConfig)

// guardConfig holds the resolved optional behavior shared by Promoter and
// Preflighter. The zero value is never used directly; constructors seed it with
// production defaults before applying caller options.
type guardConfig struct {
	ancestor AncestorFunc
}

// WithAncestorFunc overrides the git-ancestry checker used by the promotion
// divergence guards. The default is git.IsAncestor; tests inject a stub so the
// patch-containment rule can be exercised without a populated repository.
func WithAncestorFunc(fn AncestorFunc) Option {
	return func(c *guardConfig) {
		if fn != nil {
			c.ancestor = fn
		}
	}
}

// newGuardConfig resolves options over the production defaults.
func newGuardConfig(opts ...Option) guardConfig {
	cfg := guardConfig{ancestor: git.IsAncestor}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// divergedSourceError builds the error returned when a promotion would read its
// source from a diverged environment. The message names the env, its
// integration branch, the carried patches, and the escape hatches, so the
// failure is actionable in the preflight log rather than mid-deploy.
func divergedSourceError(env string, state *config.EnvState) error {
	ref := state.Ref
	if ref == "" {
		ref = "env/" + env
	}
	return fmt.Errorf(
		"cannot promote from diverged environment %q (integration branch %s carries non-trunk patches %s): "+
			"a diverged env holds a non-trunk SHA that must not propagate upward. "+
			"Escape hatches: promote from a lower environment, or direct-promote a trunk SHA that already contains the patches",
		env, ref, strings.Join(state.Patches, ", "),
	)
}

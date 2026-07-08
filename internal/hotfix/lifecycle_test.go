package hotfix

import (
	"reflect"
	"testing"

	"github.com/stablekernel/cascade/internal/taggrammar"

	"github.com/stablekernel/cascade/internal/config"
)

func TestEnvBranchName(t *testing.T) {
	tests := []struct {
		name      string
		component string
		env       string
		want      string
	}{
		{
			name:      "no component is byte-identical to the historical form",
			component: "",
			env:       "staging",
			want:      "env/staging",
		},
		{
			name:      "named component nests the env under the component",
			component: "web",
			env:       "staging",
			want:      "env/web/staging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EnvBranchName(tt.component, tt.env); got != tt.want {
				t.Fatalf("EnvBranchName(%q, %q) = %q, want %q", tt.component, tt.env, got, tt.want)
			}
		})
	}
}

func TestParseEnvBranch(t *testing.T) {
	tests := []struct {
		name          string
		branch        string
		wantComponent string
		wantEnv       string
		wantOK        bool
	}{
		{
			name:    "single-component env branch parses to an empty component",
			branch:  "env/staging",
			wantEnv: "staging",
			wantOK:  true,
		},
		{
			name:          "nested env branch parses component and env unambiguously",
			branch:        "env/web/staging",
			wantComponent: "web",
			wantEnv:       "staging",
			wantOK:        true,
		},
		{
			name:   "a non-env branch is not an env branch",
			branch: "main",
			wantOK: false,
		},
		{
			name:   "a feature branch that merely contains a slash is not an env branch",
			branch: "feature/x",
			wantOK: false,
		},
		{
			name:   "the bare prefix is malformed",
			branch: "env/",
			wantOK: false,
		},
		{
			name:   "an over-nested branch is malformed",
			branch: "env/web/staging/extra",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component, env, ok := ParseEnvBranch(tt.branch)
			if ok != tt.wantOK || component != tt.wantComponent || env != tt.wantEnv {
				t.Fatalf("ParseEnvBranch(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.branch, component, env, ok, tt.wantComponent, tt.wantEnv, tt.wantOK)
			}
		})
	}
}

func TestParseEnvBranch_NestedEnvIsNotMisreadAsComponentPath(t *testing.T) {
	// The naive strings.TrimPrefix("env/") footgun would read env/web/staging as
	// env "web/staging"; the parse must keep the env exactly "staging".
	component, env, ok := ParseEnvBranch("env/web/staging")
	if !ok {
		t.Fatalf("ParseEnvBranch(env/web/staging) ok = false, want true")
	}
	if env == "web/staging" {
		t.Fatalf("env misparsed as %q; the component must be split off", env)
	}
	if component != "web" || env != "staging" {
		t.Fatalf("ParseEnvBranch(env/web/staging) = (%q, %q), want (web, staging)", component, env)
	}
}

func TestEnvBranchNameParseEnvBranchRoundTrip(t *testing.T) {
	cases := []struct {
		component string
		env       string
	}{
		{"", "staging"},
		{"web", "staging"},
		{"api", "prod"},
	}
	for _, c := range cases {
		branch := EnvBranchName(c.component, c.env)
		component, env, ok := ParseEnvBranch(branch)
		if !ok || component != c.component || env != c.env {
			t.Fatalf("round-trip of (%q, %q) via %q = (%q, %q, %v)",
				c.component, c.env, branch, component, env, ok)
		}
	}
}

func TestOrphanEnvBranches(t *testing.T) {
	tests := []struct {
		name      string
		component string
		branches  []string
		state     map[string]*config.EnvState
		want      []string
	}{
		{
			name:      "no branches yields no orphans",
			component: "",
			branches:  nil,
			state: map[string]*config.EnvState{
				"test": {SHA: "abc"},
			},
			want: nil,
		},
		{
			name:      "branch with matching divergence is not an orphan",
			component: "",
			branches:  []string{"env/test"},
			state: map[string]*config.EnvState{
				"test": {Ref: "env/test", BaseSHA: "base", Patches: []string{"p1"}},
			},
			want: nil,
		},
		{
			name:      "branch without matching divergence is an orphan",
			component: "",
			branches:  []string{"env/test"},
			state: map[string]*config.EnvState{
				"test": {SHA: "abc"}, // not diverged
			},
			want: []string{"env/test"},
		},
		{
			name:      "branch for an env absent from state is an orphan",
			component: "",
			branches:  []string{"env/staging"},
			state: map[string]*config.EnvState{
				"test": {Ref: "env/test"},
			},
			want: []string{"env/staging"},
		},
		{
			name:      "non env-prefixed branches are ignored",
			component: "",
			branches:  []string{"main", "feature/x", "env/test"},
			state: map[string]*config.EnvState{
				"test": {SHA: "abc"},
			},
			want: []string{"env/test"},
		},
		{
			name:      "mixed orphan and healthy branches",
			component: "",
			branches:  []string{"env/test", "env/uat"},
			state: map[string]*config.EnvState{
				"test": {Ref: "env/test", Patches: []string{"p1"}},
				"uat":  {SHA: "abc"},
			},
			want: []string{"env/uat"},
		},
		{
			name:      "default component ignores a component's nested branches",
			component: "",
			branches:  []string{"env/test", "env/web/test"},
			state: map[string]*config.EnvState{
				"test": {SHA: "abc"}, // not diverged
			},
			// env/web/test belongs to the web namespace, never the default one,
			// so it is never flagged here even though it has no divergence.
			want: []string{"env/test"},
		},
		{
			name:      "component scopes orphan detection to its own namespace",
			component: "web",
			branches:  []string{"env/staging", "env/web/staging", "env/api/staging"},
			state: map[string]*config.EnvState{
				// web's own env subtree: staging is not diverged, so env/web/staging
				// is web's orphan; env/staging and env/api/staging are siblings.
				"staging": {SHA: "abc"},
			},
			want: []string{"env/web/staging"},
		},
		{
			name:      "a component never flags a sibling's diverged branch",
			component: "web",
			branches:  []string{"env/api/staging", "env/web/staging"},
			state: map[string]*config.EnvState{
				// web's staging is diverged, so env/web/staging is healthy;
				// env/api/staging is api's concern and must never appear here.
				"staging": {Ref: "env/web/staging", BaseSHA: "base", Patches: []string{"p1"}},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OrphanEnvBranches(tt.component, tt.branches, tt.state)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("OrphanEnvBranches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHealOrphanEnvBranches(t *testing.T) {
	state := map[string]*config.EnvState{
		// "test" is legitimately diverged: env/test backs it and must never be
		// deleted by a heal.
		"test": {Ref: "env/test", BaseSHA: "base", Patches: []string{"p1"}},
		// "uat" is not diverged: env/uat is an orphan.
		"uat": {SHA: "abc"},
	}

	t.Run("deletes only orphans and leaves diverged branches intact", func(t *testing.T) {
		var deleted []string
		del := func(remote, branch string) error {
			deleted = append(deleted, branch)
			return nil
		}

		healed, err := HealOrphanEnvBranches(
			"", []string{"main", "env/test", "env/uat", "env/staging"}, state, "origin", del)
		if err != nil {
			t.Fatalf("HealOrphanEnvBranches() error = %v", err)
		}

		want := []string{"env/uat", "env/staging"}
		if !reflect.DeepEqual(healed, want) {
			t.Fatalf("healed = %v, want %v", healed, want)
		}
		if !reflect.DeepEqual(deleted, want) {
			t.Fatalf("deleted = %v, want %v", deleted, want)
		}
		for _, b := range deleted {
			if b == "env/test" {
				t.Fatalf("diverged env branch env/test must never be deleted")
			}
		}
	})

	t.Run("idempotent when an orphan is already absent", func(t *testing.T) {
		// A deleter that no-ops on a missing branch (git.DeleteRemoteBranch's
		// real behavior) keeps the heal a clean, repeatable no-op.
		calls := 0
		del := func(remote, branch string) error {
			calls++
			return nil // already gone is success
		}

		first, err := HealOrphanEnvBranches("", []string{"env/uat"}, state, "origin", del)
		if err != nil {
			t.Fatalf("first heal error = %v", err)
		}
		second, err := HealOrphanEnvBranches("", []string{"env/uat"}, state, "origin", del)
		if err != nil {
			t.Fatalf("second heal error = %v", err)
		}
		if !reflect.DeepEqual(first, []string{"env/uat"}) || !reflect.DeepEqual(second, []string{"env/uat"}) {
			t.Fatalf("heal not idempotent: first=%v second=%v", first, second)
		}
		if calls != 2 {
			t.Fatalf("expected two delete attempts, got %d", calls)
		}
	})

	t.Run("no orphans is a clean no-op", func(t *testing.T) {
		del := func(remote, branch string) error {
			t.Fatalf("delete must not be called when nothing is orphaned")
			return nil
		}
		healed, err := HealOrphanEnvBranches("", []string{"main", "env/test"}, state, "origin", del)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if healed != nil {
			t.Fatalf("healed = %v, want nil", healed)
		}
	})

	t.Run("a component heal never deletes a sibling's branch", func(t *testing.T) {
		// web's staging is diverged, so env/web/staging is healthy. env/api/staging
		// belongs to a sibling and must never be considered by web's heal, even
		// though web's state has no record for it.
		webState := map[string]*config.EnvState{
			"staging": {Ref: "env/web/staging", BaseSHA: "base", Patches: []string{"p1"}},
		}
		var deleted []string
		del := func(remote, branch string) error {
			deleted = append(deleted, branch)
			return nil
		}
		healed, err := HealOrphanEnvBranches(
			"web", []string{"env/web/staging", "env/api/staging"}, webState, "origin", del)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if healed != nil {
			t.Fatalf("healed = %v, want nil (no web orphan, sibling out of scope)", healed)
		}
		for _, b := range deleted {
			if b == "env/api/staging" {
				t.Fatalf("web heal must never delete the sibling branch env/api/staging")
			}
		}
	})

	t.Run("propagates a delete error with the healed-so-far set", func(t *testing.T) {
		del := func(remote, branch string) error {
			return errBoom
		}
		healed, err := HealOrphanEnvBranches("", []string{"env/uat"}, state, "origin", del)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if healed != nil {
			t.Fatalf("healed = %v, want nil on first-delete failure", healed)
		}
	})
}

var errBoom = errBoomType("boom")

type errBoomType string

func (e errBoomType) Error() string { return string(e) }

func TestHotfixTagsForBase(t *testing.T) {
	tests := []struct {
		name        string
		baseVersion string
		tags        []string
		want        []string
	}{
		{
			name:        "rc base matches its dotted hotfix tags only",
			baseVersion: "v1.4.0-rc.2",
			tags: []string{
				"v1.4.0-rc.2",
				"v1.4.0-rc.2.hotfix.1",
				"v1.4.0-rc.2.hotfix.2",
				"v1.4.0-rc.3",
				"v1.4.0-rc.3.hotfix.1", // different base rc
				"v1.3.0",
			},
			want: []string{"v1.4.0-rc.2.hotfix.1", "v1.4.0-rc.2.hotfix.2"},
		},
		{
			name:        "no hotfix tags yields empty",
			baseVersion: "v1.4.0-rc.2",
			tags:        []string{"v1.4.0-rc.2", "v1.4.0-rc.3"},
			want:        nil,
		},
		{
			name:        "hotfix base version normalizes to its rc base",
			baseVersion: "v1.4.0-rc.2.hotfix.1",
			tags: []string{
				"v1.4.0-rc.2.hotfix.1",
				"v1.4.0-rc.2.hotfix.2",
			},
			want: []string{"v1.4.0-rc.2.hotfix.1", "v1.4.0-rc.2.hotfix.2"},
		},
		{
			name:        "unparseable base yields empty",
			baseVersion: "not-a-version",
			tags:        []string{"v1.4.0-rc.2.hotfix.1"},
			want:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HotfixTagsForBase(taggrammar.Default(), tt.baseVersion, tt.tags)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("HotfixTagsForBase() = %v, want %v", got, tt.want)
			}
		})
	}
}

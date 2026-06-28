package hotfix

import (
	"reflect"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

func TestOrphanEnvBranches(t *testing.T) {
	tests := []struct {
		name     string
		branches []string
		state    map[string]*config.EnvState
		want     []string
	}{
		{
			name:     "no branches yields no orphans",
			branches: nil,
			state: map[string]*config.EnvState{
				"test": {SHA: "abc"},
			},
			want: nil,
		},
		{
			name:     "branch with matching divergence is not an orphan",
			branches: []string{"env/test"},
			state: map[string]*config.EnvState{
				"test": {Ref: "env/test", BaseSHA: "base", Patches: []string{"p1"}},
			},
			want: nil,
		},
		{
			name:     "branch without matching divergence is an orphan",
			branches: []string{"env/test"},
			state: map[string]*config.EnvState{
				"test": {SHA: "abc"}, // not diverged
			},
			want: []string{"env/test"},
		},
		{
			name:     "branch for an env absent from state is an orphan",
			branches: []string{"env/staging"},
			state: map[string]*config.EnvState{
				"test": {Ref: "env/test"},
			},
			want: []string{"env/staging"},
		},
		{
			name:     "non env-prefixed branches are ignored",
			branches: []string{"main", "feature/x", "env/test"},
			state: map[string]*config.EnvState{
				"test": {SHA: "abc"},
			},
			want: []string{"env/test"},
		},
		{
			name:     "mixed orphan and healthy branches",
			branches: []string{"env/test", "env/uat"},
			state: map[string]*config.EnvState{
				"test": {Ref: "env/test", Patches: []string{"p1"}},
				"uat":  {SHA: "abc"},
			},
			want: []string{"env/uat"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OrphanEnvBranches(tt.branches, tt.state)
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
			[]string{"main", "env/test", "env/uat", "env/staging"}, state, "origin", del)
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

		first, err := HealOrphanEnvBranches([]string{"env/uat"}, state, "origin", del)
		if err != nil {
			t.Fatalf("first heal error = %v", err)
		}
		second, err := HealOrphanEnvBranches([]string{"env/uat"}, state, "origin", del)
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
		healed, err := HealOrphanEnvBranches([]string{"main", "env/test"}, state, "origin", del)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if healed != nil {
			t.Fatalf("healed = %v, want nil", healed)
		}
	})

	t.Run("propagates a delete error with the healed-so-far set", func(t *testing.T) {
		del := func(remote, branch string) error {
			return errBoom
		}
		healed, err := HealOrphanEnvBranches([]string{"env/uat"}, state, "origin", del)
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
			got := HotfixTagsForBase(tt.baseVersion, tt.tags)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("HotfixTagsForBase() = %v, want %v", got, tt.want)
			}
		})
	}
}

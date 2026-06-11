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

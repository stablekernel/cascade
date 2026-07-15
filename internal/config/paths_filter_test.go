package config

import (
	"reflect"
	"testing"
)

// TestOrchestratePathsFilter locks in the normalized on.push paths filter for
// the orchestrate workflow. GitHub evaluates a paths filter in order
// (last-match-wins), so the emitted list must preserve manifest order, must
// never let one callback's exclusion veto a sibling callback's positive
// pattern, and must translate a negation-only list to paths-ignore because
// GitHub requires at least one non-"!" entry in a paths filter.
func TestOrchestratePathsFilter(t *testing.T) {
	tests := []struct {
		name string
		cfg  *TrunkConfig
		want PathsFilter
	}{
		{
			name: "no triggers anywhere means no filter",
			cfg: &TrunkConfig{
				Builds: []BuildConfig{{Name: "app", Workflow: "build.yaml"}},
			},
			want: PathsFilter{},
		},
		{
			name: "global triggers preserve manifest order",
			cfg: &TrunkConfig{
				Triggers: []string{"src/**", "!src/vendor/**", "src/vendor/keep/**"},
			},
			want: PathsFilter{Patterns: []string{"src/**", "!src/vendor/**", "src/vendor/keep/**"}},
		},
		{
			name: "single build list is emitted verbatim",
			cfg: &TrunkConfig{
				Builds: []BuildConfig{
					{Name: "app", Workflow: "build.yaml", Triggers: []string{"docs/**", "!docs/api/**"}},
				},
			},
			want: PathsFilter{Patterns: []string{"docs/**", "!docs/api/**"}},
		},
		{
			name: "identical lists across callbacks collapse to one verbatim list",
			cfg: &TrunkConfig{
				Builds: []BuildConfig{
					{Name: "a", Workflow: "a.yaml", Triggers: []string{"docs/**", "!docs/api/**"}},
					{Name: "b", Workflow: "b.yaml", Triggers: []string{"docs/**", "!docs/api/**"}},
				},
			},
			want: PathsFilter{Patterns: []string{"docs/**", "!docs/api/**"}},
		},
		{
			name: "positive-only lists union in manifest order",
			cfg: &TrunkConfig{
				Builds: []BuildConfig{
					{Name: "a", Workflow: "a.yaml", Triggers: []string{"src/**", "go.mod"}},
					{Name: "b", Workflow: "b.yaml", Triggers: []string{"lib/**", "go.mod"}},
				},
			},
			want: PathsFilter{Patterns: []string{"src/**", "go.mod", "lib/**"}},
		},
		{
			name: "distinct lists with a negation drop the negations",
			// Keeping build b's "!docs/api/**" in the union would let it veto
			// build a's "**/*.md" positive under last-match-wins, silently
			// never firing the workflow for a docs/api markdown change that
			// build a must see. Dropping negations over-fires at worst; the
			// per-callback CLI detection still applies each list exactly.
			cfg: &TrunkConfig{
				Builds: []BuildConfig{
					{Name: "a", Workflow: "a.yaml", Triggers: []string{"**/*.md"}},
					{Name: "b", Workflow: "b.yaml", Triggers: []string{"docs/**", "!docs/api/**"}},
				},
			},
			want: PathsFilter{Patterns: []string{"**/*.md", "docs/**"}},
		},
		{
			name: "negation-only list translates to paths-ignore",
			cfg: &TrunkConfig{
				Triggers: []string{"!docs/**", "!**/*.md"},
			},
			want: PathsFilter{Patterns: []string{"docs/**", "**/*.md"}, Ignore: true},
		},
		{
			name: "negation-only list among distinct lists removes the filter",
			// The negation-only list means "everything except docs"; no flat
			// paths filter can express its union with a positive list, so the
			// workflow must run on every push and defer to CLI detection.
			cfg: &TrunkConfig{
				Builds: []BuildConfig{
					{Name: "a", Workflow: "a.yaml", Triggers: []string{"src/**"}},
					{Name: "b", Workflow: "b.yaml", Triggers: []string{"!docs/**"}},
				},
			},
			want: PathsFilter{},
		},
		{
			name: "validate triggers come first in the union",
			cfg: &TrunkConfig{
				Validate: &ValidateConfig{Workflow: "validate.yaml", Triggers: []string{"Makefile"}},
				Builds: []BuildConfig{
					{Name: "a", Workflow: "a.yaml", Triggers: []string{"src/**"}},
				},
				Deploys: []DeployConfig{
					{Name: "d", Workflow: "d.yaml", Triggers: []string{"deploy/**"}},
				},
			},
			want: PathsFilter{Patterns: []string{"Makefile", "src/**", "deploy/**"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.OrchestratePathsFilter()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("OrchestratePathsFilter() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestGetAllTriggers_PreservesManifestOrder guards the raw trigger collection:
// pattern order is semantic under the GitHub Actions paths filter, so the
// collection must never re-sort what the manifest declared.
func TestGetAllTriggers_PreservesManifestOrder(t *testing.T) {
	cfg := &TrunkConfig{
		Triggers: []string{"src/**", "!src/vendor/**", "src/vendor/keep/**", "go.mod"},
	}
	want := []string{"src/**", "!src/vendor/**", "src/vendor/keep/**", "go.mod"}
	if got := cfg.GetAllTriggers(); !reflect.DeepEqual(got, want) {
		t.Errorf("GetAllTriggers() = %v, want %v", got, want)
	}

	collected := &TrunkConfig{
		Builds: []BuildConfig{
			{Name: "b", Workflow: "b.yaml", Triggers: []string{"zeta/**", "alpha/**"}},
		},
		Deploys: []DeployConfig{
			{Name: "d", Workflow: "d.yaml", Triggers: []string{"mid/**", "alpha/**"}},
		},
	}
	want = []string{"zeta/**", "alpha/**", "mid/**"}
	if got := collected.GetAllTriggers(); !reflect.DeepEqual(got, want) {
		t.Errorf("GetAllTriggers() = %v, want %v", got, want)
	}
}

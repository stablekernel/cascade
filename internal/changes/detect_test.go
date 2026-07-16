package changes

import (
	"testing"
)

func TestIsTriggered(t *testing.T) {
	tests := []struct {
		name         string
		triggers     []string
		changedFiles []string
		want         bool
	}{
		{
			name:         "no triggers means always triggered",
			triggers:     []string{},
			changedFiles: []string{"anything.go"},
			want:         true,
		},
		{
			name:         "matching trigger",
			triggers:     []string{"src/**"},
			changedFiles: []string{"src/main.go"},
			want:         true,
		},
		{
			name:         "no matching trigger",
			triggers:     []string{"src/**"},
			changedFiles: []string{"other/main.go"},
			want:         false,
		},
		{
			name:         "multiple triggers, one matches",
			triggers:     []string{"src/**", "Dockerfile"},
			changedFiles: []string{"Dockerfile"},
			want:         true,
		},
		{
			name:         "multiple files, one matches",
			triggers:     []string{"src/**"},
			changedFiles: []string{"README.md", "src/main.go"},
			want:         true,
		},

		// Negation ("!") patterns: evaluation is order-dependent, exactly as
		// the emitted GitHub Actions paths filter evaluates them. A matching
		// negation after a positive match excludes the path; a matching
		// positive after a negation includes it again (last match wins).
		{
			name:         "docs-only change excluded by negation does not trigger",
			triggers:     []string{"**", "!**/*.md", "!docs/**"},
			changedFiles: []string{"docs/README.md"},
			want:         false,
		},
		{
			name:         "source change triggers despite negation",
			triggers:     []string{"**", "!**/*.md", "!docs/**"},
			changedFiles: []string{"src/main.go"},
			want:         true,
		},
		{
			name:         "mixed changeset triggers when a non-excluded file is present",
			triggers:     []string{"**", "!**/*.md", "!docs/**"},
			changedFiles: []string{"docs/README.md", "src/main.go"},
			want:         true,
		},
		{
			name:         "negation excludes a file that matches a positive pattern",
			triggers:     []string{"src/**", "!src/**/*.md"},
			changedFiles: []string{"src/docs.md"},
			want:         false,
		},
		{
			name:         "negation-only list triggers any non-excluded file",
			triggers:     []string{"!**/*.md"},
			changedFiles: []string{"src/main.go"},
			want:         true,
		},
		{
			name:         "negation-only list excludes a matching file",
			triggers:     []string{"!**/*.md"},
			changedFiles: []string{"README.md"},
			want:         false,
		},
		{
			name:         "ordering matters: a positive after a negation re-includes",
			triggers:     []string{"!**/*.md", "**"},
			changedFiles: []string{"docs/README.md"},
			want:         true,
		},
		{
			name:         "re-inclusion after exclusion triggers",
			triggers:     []string{"src/**", "!src/vendor/**", "src/vendor/keep/**"},
			changedFiles: []string{"src/vendor/keep/patch.go"},
			want:         true,
		},
		{
			name:         "positive-only behaviour is unchanged by negation support",
			triggers:     []string{"src/**", "Dockerfile"},
			changedFiles: []string{"Dockerfile"},
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTriggered(tt.triggers, tt.changedFiles)
			if got != tt.want {
				t.Errorf("isTriggered() = %v, want %v", got, tt.want)
			}
		})
	}
}

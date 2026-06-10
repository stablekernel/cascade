package changes

import (
	"testing"
)

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Simple patterns
		{"*.go", "main.go", true},
		{"*.go", "main.txt", false},
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false},

		// Double star patterns
		{"src/**", "src/main.go", true},
		{"src/**", "src/sub/main.go", true},
		{"src/**/*.go", "src/main.go", true},
		{"src/**/*.go", "src/sub/main.go", true},
		{"src/**/*.go", "src/sub/deep/main.go", true},
		{"**/*.go", "main.go", true},
		{"**/*.go", "src/main.go", true},
		{"**", "anything/at/all.txt", true},

		// Question mark
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},

		// No match
		{"src/**", "other/main.go", false},
		{"*.yaml", "config.json", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.path, func(t *testing.T) {
			got := matchGlob(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

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

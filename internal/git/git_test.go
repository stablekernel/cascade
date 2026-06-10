package git

import (
	"reflect"
	"testing"
)

func TestParseCommits(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want []Commit
	}{
		{
			name: "single commit",
			// Format: hash, subject, author, email, body
			data: []byte("abc123\x1fFix bug\x1fJohn Doe\x1fjohn@example.com\x1fThis fixes the bug\x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "Fix bug", Author: "John Doe", AuthorEmail: "john@example.com", Body: "This fixes the bug"},
			},
		},
		{
			name: "multiple commits",
			data: []byte("abc123\x1fFirst commit\x1fAlice\x1falice@example.com\x1fBody 1\x1edef456\x1fSecond commit\x1fBob\x1fbob@example.com\x1fBody 2\x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "First commit", Author: "Alice", AuthorEmail: "alice@example.com", Body: "Body 1"},
				{Hash: "def456", Subject: "Second commit", Author: "Bob", AuthorEmail: "bob@example.com", Body: "Body 2"},
			},
		},
		{
			name: "commit without body",
			data: []byte("abc123\x1fSimple fix\x1fDev\x1fdev@example.com\x1f\x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "Simple fix", Author: "Dev", AuthorEmail: "dev@example.com", Body: ""},
			},
		},
		{
			name: "empty input",
			data: []byte(""),
			want: nil,
		},
		{
			name: "multiline body",
			data: []byte("abc123\x1fFeat: something\x1fAuthor Name\x1fauthor@example.com\x1fLine 1\nLine 2\nLine 3\x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "Feat: something", Author: "Author Name", AuthorEmail: "author@example.com", Body: "Line 1\nLine 2\nLine 3"},
			},
		},
		{
			name: "whitespace handling",
			data: []byte("  abc123  \x1f  Subject  \x1f  Author  \x1f  author@example.com  \x1f  Body  \x1e"),
			want: []Commit{
				{Hash: "abc123", Subject: "Subject", Author: "Author", AuthorEmail: "author@example.com", Body: "Body"},
			},
		},
		{
			name: "same author multiple commits",
			data: []byte("commit1\x1fFix 1\x1fAlice\x1falice@example.com\x1f\x1ecommit2\x1fFix 2\x1fAlice\x1falice@example.com\x1f\x1e"),
			want: []Commit{
				{Hash: "commit1", Subject: "Fix 1", Author: "Alice", AuthorEmail: "alice@example.com", Body: ""},
				{Hash: "commit2", Subject: "Fix 2", Author: "Alice", AuthorEmail: "alice@example.com", Body: ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCommits(tt.data)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseCommits() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseLines(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want []string
	}{
		{
			name: "multiple lines",
			data: []byte("file1.go\nfile2.go\nfile3.go"),
			want: []string{"file1.go", "file2.go", "file3.go"},
		},
		{
			name: "trailing newline",
			data: []byte("file1.go\nfile2.go\n"),
			want: []string{"file1.go", "file2.go"},
		},
		{
			name: "empty lines filtered",
			data: []byte("file1.go\n\nfile2.go\n\n"),
			want: []string{"file1.go", "file2.go"},
		},
		{
			name: "whitespace trimmed",
			data: []byte("  file1.go  \n  file2.go  "),
			want: []string{"file1.go", "file2.go"},
		},
		{
			name: "empty input",
			data: []byte(""),
			want: nil,
		},
		{
			name: "single file",
			data: []byte("only-file.txt"),
			want: []string{"only-file.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLines(tt.data)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseLines() = %v, want %v", got, tt.want)
			}
		})
	}
}

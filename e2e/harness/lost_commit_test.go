package harness

import "testing"

func TestParsePushedSHA(t *testing.T) {
	t.Parallel()

	const fullSHA = "0123456789abcdef0123456789abcdef01234567"

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "marker on its own line",
			output: pushedSHAMarker + fullSHA,
			want:   fullSHA,
		},
		{
			name:   "marker after push chatter",
			output: "Enumerating objects: 5, done.\nTo http://gitea/repo.git\n   abc..def main -> main\n" + pushedSHAMarker + fullSHA + "\n",
			want:   fullSHA,
		},
		{
			name:   "trailing whitespace trimmed",
			output: pushedSHAMarker + fullSHA + "  \n",
			want:   fullSHA,
		},
		{
			name:   "leading whitespace tolerated",
			output: "   " + pushedSHAMarker + fullSHA,
			want:   fullSHA,
		},
		{
			name:   "abbreviated sha accepted",
			output: pushedSHAMarker + "abc1234",
			want:   "abc1234",
		},
		{
			name:   "no marker",
			output: "To http://gitea/repo.git\n   abc..def main -> main\n",
			want:   "",
		},
		{
			name:   "marker present but value not hex",
			output: pushedSHAMarker + "not-a-sha",
			want:   "",
		},
		{
			name:   "marker present but value empty",
			output: pushedSHAMarker,
			want:   "",
		},
		{
			name:   "empty output",
			output: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parsePushedSHA(tt.output); got != tt.want {
				t.Fatalf("parsePushedSHA(%q) = %q, want %q", tt.output, got, tt.want)
			}
		})
	}
}

func TestIsHexSHA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "full lowercase sha", in: "0123456789abcdef0123456789abcdef01234567", want: true},
		{name: "uppercase hex", in: "ABCDEF0", want: true},
		{name: "minimum abbreviated length", in: "abc1234", want: true},
		{name: "too short", in: "abc123", want: false},
		{name: "empty", in: "", want: false},
		{name: "non-hex character", in: "abcdefg", want: false},
		{name: "contains hyphen", in: "abc-123", want: false},
		{name: "too long", in: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef01", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isHexSHA(tt.in); got != tt.want {
				t.Fatalf("isHexSHA(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

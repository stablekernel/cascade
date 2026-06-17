package harness

import (
	"os/exec"
	"strings"
	"testing"
)

// runSed pipes in through `sed <expr>` and returns the transformed output. It
// exercises the exact expression the localizer runs inside the act container.
func runSed(t *testing.T, expr, in string) string {
	t.Helper()
	sed, err := exec.LookPath("sed")
	if err != nil {
		t.Skipf("sed not available: %v", err)
	}
	cmd := exec.Command(sed, expr)
	cmd.Stdin = strings.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sed %q failed: %v", expr, err)
	}
	return string(out)
}

// grepMatches reports whether `grep -lE <pattern>` finds the pattern in in,
// exercising the exact extended-regex the post-localize verify runs inside the
// act container. grep exits 0 on a match (un-localized ref present) and 1 on no
// match (clean); any other exit is a hard failure.
func grepMatches(t *testing.T, pattern, in string) bool {
	t.Helper()
	grep, err := exec.LookPath("grep")
	if err != nil {
		t.Skipf("grep not available: %v", err)
	}
	cmd := exec.Command(grep, "-lE", pattern)
	cmd.Stdin = strings.NewReader(in)
	err = cmd.Run()
	if err == nil {
		return true
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("grep -lE %q failed: %v", pattern, err)
	return false
}

// TestUnlocalizedActionRefPattern_IgnoresCrossRepoCallback locks in that the
// post-localize verify only flags the action ref the sed rewrites and never the
// distinct cross-repo reusable-workflow callback repo. A bare `stablekernel/cascade`
// substring also matched `stablekernel/cascade-example-artifact-a/...@ref`
// (scenario 21), so every scenario wiring a cross-repo callback failed staging
// with `localize verify found un-localized refs`.
func TestUnlocalizedActionRefPattern_IgnoresCrossRepoCallback(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantMatch bool
	}{
		{
			name:      "un-localized action ref is flagged",
			in:        "        uses: stablekernel/cascade/.github/actions/setup-cli@v0.1.0\n",
			wantMatch: true,
		},
		{
			name:      "localized action ref is clean",
			in:        "        uses: ./.github/actions/setup-cli\n",
			wantMatch: false,
		},
		{
			name:      "cross-repo callback to a sibling repo is not flagged",
			in:        "    uses: stablekernel/cascade-example-artifact-a/.github/workflows/build-shared.yaml@main\n",
			wantMatch: false,
		},
		{
			name:      "cross-repo reusable workflow in the cascade repo itself is not flagged",
			in:        "    uses: stablekernel/cascade/.github/workflows/x.yaml@v1\n",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grepMatches(t, unlocalizedActionRefPattern, tt.in)
			if got != tt.wantMatch {
				t.Errorf("grep -lE %q over %q: got match=%v, want %v",
					unlocalizedActionRefPattern, tt.in, got, tt.wantMatch)
			}
		})
	}
}

// TestUsesLocalizeSedExpr_Idempotent_LeavesQualifiedPathsUnchanged verifies the
// reusable-workflow localizer prefixes a bare ref with `./` but never adds a
// second `./` to an already-qualified path or mangles a cross-repo `@ref`.
//
// Regression guard: before the generator emitted fully-qualified local callback
// paths, the localizer turned a bare `uses: build.yaml` into `uses: ./build.yaml`.
// Once the generator started emitting `uses: ./.github/workflows/build.yaml`, the
// old expression matched the leading `.` and produced `uses: ././...`, which act
// could not resolve.
func TestUsesLocalizeSedExpr_Idempotent_LeavesQualifiedPathsUnchanged(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "already qualified local path is unchanged",
			in:   "    uses: ./.github/workflows/build.yaml\n",
			want: "    uses: ./.github/workflows/build.yaml\n",
		},
		{
			name: "qualified reusable deploy path is unchanged",
			in:   "    uses: ./.github/workflows/deploy-infra.yaml\n",
			want: "    uses: ./.github/workflows/deploy-infra.yaml\n",
		},
		{
			name: "bare filename gains a single ./ prefix",
			in:   "    uses: build.yaml\n",
			want: "    uses: ./build.yaml\n",
		},
		{
			name: "cross-repo ref with @ is left unchanged",
			in:   "    uses: owner/repo/.github/workflows/y.yaml@main\n",
			want: "    uses: owner/repo/.github/workflows/y.yaml@main\n",
		},
		{
			name: "stablekernel cross-repo ref is left unchanged",
			in:   "    uses: stablekernel/cascade/.github/workflows/x.yaml@v1\n",
			want: "    uses: stablekernel/cascade/.github/workflows/x.yaml@v1\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runSed(t, usesLocalizeSedExpr, tt.in)
			if got != tt.want {
				t.Errorf("single pass:\n in:   %q\n got:  %q\n want: %q", tt.in, got, tt.want)
			}
			// Idempotency: a second pass over the output must be a no-op.
			again := runSed(t, usesLocalizeSedExpr, got)
			if again != got {
				t.Errorf("second pass changed output (not idempotent):\n first:  %q\n second: %q", got, again)
			}
			if strings.Contains(again, "././") {
				t.Errorf("produced a double ./ prefix: %q", again)
			}
		})
	}
}

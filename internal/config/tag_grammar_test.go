package config

import (
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/taggrammar"
)

func strptr(s string) *string { return &s }

// A nil tag_grammar block resolves to the historical default grammar, and a
// tag_grammar.prefix lands on Spec.Prefix so the prefix knob keeps working.
func TestResolveTagGrammar_NilUsesDefaultWithPrefix(t *testing.T) {
	def := taggrammar.Default()

	cfg := &TrunkConfig{}
	got := cfg.ResolveTagGrammar()
	if got != def {
		t.Fatalf("nil tag_grammar: got %+v, want default %+v", got, def)
	}

	cfg = &TrunkConfig{TagGrammar: &TagGrammarConfig{Prefix: strptr("release")}}
	got = cfg.ResolveTagGrammar()
	want := def
	want.Prefix = "release"
	if got != want {
		t.Fatalf("tag_grammar.prefix only: got %+v, want %+v", got, want)
	}
}

// A populated tag_grammar block overrides the token, separator, and dryrun
// token independently.
func TestResolveTagGrammar_PopulatedOverrides(t *testing.T) {
	cfg := &TrunkConfig{
		TagGrammar: &TagGrammarConfig{
			Prefix:              strptr("ver"),
			PreReleaseToken:     strptr("beta"),
			PreReleaseSeparator: strptr("-"),
			DryRunToken:         strptr("rehearsal"),
			StrictPrefix:        true,
		},
	}
	got := cfg.ResolveTagGrammar()
	want := taggrammar.Spec{
		Prefix:              "ver",
		PreReleaseToken:     "beta",
		PreReleaseSeparator: "-",
		DryRunToken:         "rehearsal",
		StrictPrefix:        true,
	}
	if got != want {
		t.Fatalf("populated block: got %+v, want %+v", got, want)
	}
}

func TestValidateTagGrammar(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *TrunkConfig
		wantErr string // substring; "" means expect no error
	}{
		{
			name: "nil block is valid",
			cfg:  &TrunkConfig{},
		},
		{
			name: "default-shaped block is valid",
			cfg: &TrunkConfig{TagGrammar: &TagGrammarConfig{
				PreReleaseToken: strptr("rc"),
			}},
		},
		{
			name:    "empty prerelease token rejected",
			cfg:     &TrunkConfig{TagGrammar: &TagGrammarConfig{PreReleaseToken: strptr("")}},
			wantErr: "prerelease_token",
		},
		{
			name:    "prerelease token with slash rejected",
			cfg:     &TrunkConfig{TagGrammar: &TagGrammarConfig{PreReleaseToken: strptr("r/c")}},
			wantErr: "prerelease_token",
		},
		{
			name:    "prefix with whitespace rejected",
			cfg:     &TrunkConfig{TagGrammar: &TagGrammarConfig{Prefix: strptr("re lease")}},
			wantErr: "prefix",
		},
		{
			name:    "separator with regex metachar rejected",
			cfg:     &TrunkConfig{TagGrammar: &TagGrammarConfig{PreReleaseSeparator: strptr("*")}},
			wantErr: "prerelease_separator",
		},
		{
			name:    "dryrun equal to prerelease token rejected",
			cfg:     &TrunkConfig{TagGrammar: &TagGrammarConfig{PreReleaseToken: strptr("beta"), DryRunToken: strptr("beta")}},
			wantErr: "dryrun_token",
		},
		{
			name:    "dryrun defaulting to collide with prerelease rejected",
			cfg:     &TrunkConfig{TagGrammar: &TagGrammarConfig{DryRunToken: strptr("rc")}},
			wantErr: "dryrun_token",
		},
		{
			name: "custom separator empty is allowed",
			cfg:  &TrunkConfig{TagGrammar: &TagGrammarConfig{PreReleaseSeparator: strptr("")}},
		},
		{
			name:    "prerelease token with embedded single quote rejected",
			cfg:     &TrunkConfig{TagGrammar: &TagGrammarConfig{PreReleaseToken: strptr("b'ad")}},
			wantErr: "prerelease_token",
		},
		{
			name: "prefix v is allowed",
			cfg:  &TrunkConfig{TagGrammar: &TagGrammarConfig{Prefix: strptr("v")}},
		},
		{
			name: "prerelease token beta is allowed",
			cfg:  &TrunkConfig{TagGrammar: &TagGrammarConfig{PreReleaseToken: strptr("beta")}},
		},
		{
			name: "prerelease token pre is allowed",
			cfg:  &TrunkConfig{TagGrammar: &TagGrammarConfig{PreReleaseToken: strptr("pre")}},
		},
		{
			name: "separator dot is allowed",
			cfg:  &TrunkConfig{TagGrammar: &TagGrammarConfig{PreReleaseSeparator: strptr(".")}},
		},
		{
			name: "dryrun token dryrun is allowed",
			cfg:  &TrunkConfig{TagGrammar: &TagGrammarConfig{DryRunToken: strptr("dryrun")}},
		},
		{
			name: "dryrun token rehearsal is allowed",
			cfg:  &TrunkConfig{TagGrammar: &TagGrammarConfig{DryRunToken: strptr("rehearsal")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateTagGrammar(tt.cfg)
			if tt.wantErr == "" {
				if len(errs) != 0 {
					t.Fatalf("want no errors, got %v", errs)
				}
				return
			}
			joined := strings.Join(errs, "\n")
			if !strings.Contains(joined, tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, errs)
			}
		})
	}
}

// validateTagGrammar is reachable through the top-level Validate flow.
func TestValidate_WiresTagGrammar(t *testing.T) {
	cfg := &TrunkConfig{
		TrunkBranch: "main",
		TagGrammar:  &TagGrammarConfig{PreReleaseToken: strptr("")},
	}
	errs := Validate(cfg)
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, "prerelease_token") {
		t.Fatalf("Validate must surface tag_grammar errors, got %v", errs)
	}
}

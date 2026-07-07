package taggrammar

import (
	"reflect"
	"testing"
)

func TestDefault_ReproducesHistoricalGrammar(t *testing.T) {
	s := Default()
	if s.Prefix != "v" || s.PreReleaseToken != "rc" ||
		s.PreReleaseSeparator != "." || s.DryRunToken != "dryrun" {
		t.Fatalf("Default() drifted from historical grammar: %+v", s)
	}
}

func TestParse_DefaultAcceptsCanonicalTags(t *testing.T) {
	s := Default()
	cases := map[string]bool{
		"v1.2.3":               true,
		"v1.2.3-rc.4":          true,
		"v1.2.3-rc.4.hotfix.5": true,
		"1.2.3":                true,  // permissive read prefix
		"v1.2.3-dryrun.4":      false, // dryrun is not a version-parseable tag
		"v1.2.3-rc.x":          false,
		"v1.2.3-hotfix.1":      false, // hotfix only nests under rc
		"nightly":              false,
	}
	for tag, want := range cases {
		if got := s.IsVersionTag(tag); got != want {
			t.Errorf("IsVersionTag(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestParse_DefaultExtractsFields(t *testing.T) {
	s := Default()
	cases := map[string]Parsed{
		"v1.2.3":               {Major: 1, Minor: 2, Patch: 3, PreRelease: -1, Hotfix: -1},
		"v1.2.3-rc.4":          {Major: 1, Minor: 2, Patch: 3, PreRelease: 4, Hotfix: -1},
		"v1.2.3-rc.4.hotfix.5": {Major: 1, Minor: 2, Patch: 3, PreRelease: 4, Hotfix: 5},
	}
	for tag, want := range cases {
		got, ok := s.Parse(tag)
		if !ok {
			t.Errorf("Parse(%q) ok = false, want true", tag)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Parse(%q) = %+v, want %+v", tag, got, want)
		}
	}
	if _, ok := s.Parse("nightly"); ok {
		t.Errorf("Parse(%q) ok = true, want false", "nightly")
	}
}

func TestFormat_DefaultRoundTripsHistoricalStrings(t *testing.T) {
	s := Default()
	for _, tag := range []string{"v1.2.3", "v1.2.3-rc.4", "v1.2.3-rc.4.hotfix.5"} {
		p, ok := s.Parse(tag)
		if !ok {
			t.Fatalf("Parse(%q) ok = false, want true", tag)
		}
		if got := s.Format(p); got != tag {
			t.Errorf("Format(Parse(%q)) = %q, want %q", tag, got, tag)
		}
	}
}

func TestStripPreRelease_DefaultMatchesHistoricalStrip(t *testing.T) {
	s := Default()
	cases := map[string]string{
		"v1.0.0-rc.0":          "v1.0.0",
		"v1.0.0-rc.5":          "v1.0.0",
		"v2.3.4-rc.123":        "v2.3.4",
		"v1.0.0":               "v1.0.0",
		"v1.0.0-beta":          "v1.0.0-beta",
		"v1.4.0-rc.2.hotfix.1": "v1.4.0",
	}
	for in, want := range cases {
		if got := s.StripPreRelease(in); got != want {
			t.Errorf("StripPreRelease(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripPreRelease_CustomToken(t *testing.T) {
	s := Default()
	s.PreReleaseToken = "beta"
	s.PreReleaseSeparator = "."
	if got := s.StripPreRelease("1.4.0-beta.2"); got != "1.4.0" {
		t.Errorf("StripPreRelease(%q) = %q, want %q", "1.4.0-beta.2", got, "1.4.0")
	}

	s.PreReleaseSeparator = ""
	if got := s.StripPreRelease("1.4.0-beta2"); got != "1.4.0" {
		t.Errorf("StripPreRelease(%q) = %q, want %q", "1.4.0-beta2", got, "1.4.0")
	}
}

func TestParseFormat_NonDefaultSpec(t *testing.T) {
	s := Default()
	s.Prefix = ""
	s.PreReleaseToken = "beta"
	s.PreReleaseSeparator = ""

	const tag = "1.2.3-beta4"
	p, ok := s.Parse(tag)
	if !ok {
		t.Fatalf("Parse(%q) ok = false, want true", tag)
	}
	want := Parsed{Major: 1, Minor: 2, Patch: 3, PreRelease: 4, Hotfix: -1}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("Parse(%q) = %+v, want %+v", tag, p, want)
	}
	if got := s.Format(p); got != tag {
		t.Errorf("Format(Parse(%q)) = %q, want %q", tag, got, tag)
	}
}

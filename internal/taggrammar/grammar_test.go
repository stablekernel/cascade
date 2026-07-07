package taggrammar

import "testing"

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

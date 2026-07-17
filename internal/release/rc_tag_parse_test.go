package release

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stablekernel/cascade/internal/taggrammar"
)

// strictSpec returns a strict per-component grammar, mirroring the only shape a
// production caller threads: ResolvedComponent.TagGrammarSpec forces
// StrictPrefix on, so the read side matches the component prefix literally.
func strictSpec(prefix, token string) taggrammar.Spec {
	spec := taggrammar.Default()
	spec.Prefix = prefix
	spec.PreReleaseToken = token
	spec.StrictPrefix = true
	return spec
}

// TestParseRCTagStrict_Table pins the plain-RC-tag contract of parseRCTagStrict
// across the grammar's boundaries: only a version tag carrying a pre-release and
// no nested hotfix parses, and the base is rendered back through the grammar so
// it carries the component's prefix.
func TestParseRCTagStrict_Table(t *testing.T) {
	def := taggrammar.Default()
	api := strictSpec("api-", "rc")
	beta := strictSpec("svc-", "beta")

	tests := []struct {
		name     string
		spec     taggrammar.Spec
		tag      string
		wantBase string
		wantRC   int
		wantOK   bool
	}{
		// Accepted shapes.
		{"default rc tag", def, "v1.2.0-rc.3", "v1.2.0", 3, true},
		{"rc zero boundary", def, "v1.2.0-rc.0", "v1.2.0", 0, true},
		{"multi-digit rc", def, "v1.2.0-rc.10", "v1.2.0", 10, true},
		{"zero version core", def, "v0.0.0-rc.1", "v0.0.0", 1, true},
		{"component tag under its own grammar", api, "api-1.2.0-rc.3", "api-1.2.0", 3, true},
		{"custom pre-release token", beta, "svc-1.2.0-beta.4", "svc-1.2.0", 4, true},

		// Rejected: not a pre-release.
		{"plain version, no pre-release", def, "v1.2.0", "", -1, false},
		{"component plain version", api, "api-1.2.0", "", -1, false},

		// Rejected: nested hotfix variants belong to the hotfix rejoin path.
		{"hotfix variant", def, "v1.2.0-rc.3.hotfix.1", "", -1, false},
		{"component hotfix variant", api, "api-1.2.0-rc.3.hotfix.2", "", -1, false},

		// Rejected: wrong pre-release token for the grammar.
		{"beta token under rc grammar", def, "v1.2.0-beta.3", "", -1, false},
		{"rc token under beta grammar", beta, "svc-1.2.0-rc.3", "", -1, false},
		{"dryrun rehearsal tag", def, "v1.2.0-dryrun.1", "", -1, false},

		// Rejected: malformed.
		{"empty string", def, "", "", -1, false},
		{"empty string under component grammar", api, "", "", -1, false},
		{"rc with no number", def, "v1.2.0-rc.", "", -1, false},
		{"negative rc", def, "v1.2.0-rc.-1", "", -1, false},
		{"trailing junk", def, "v1.2.0-rc.3-extra", "", -1, false},
		{"leading space", def, " v1.2.0-rc.3", "", -1, false},
		{"two-segment core", def, "v1.2-rc.3", "", -1, false},
		{"non-numeric core", def, "vx.y.z-rc.3", "", -1, false},

		// Rejected: a sibling component's tag never parses under this
		// component's grammar. This is the namespace isolation that keeps the
		// draft reaper from crossing into a sibling's releases.
		{"sibling tag under component grammar", api, "web-1.2.0-rc.3", "", -1, false},
		{"bare tag under component grammar", api, "1.2.0-rc.3", "", -1, false},
		{"component tag under default grammar", def, "api-1.2.0-rc.3", "", -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, rc, ok := parseRCTagStrict(tt.spec, tt.tag)
			assert.Equal(t, tt.wantOK, ok, "ok for tag %q", tt.tag)
			assert.Equal(t, tt.wantBase, base, "base for tag %q", tt.tag)
			assert.Equal(t, tt.wantRC, rc, "rc for tag %q", tt.tag)
		})
	}
}

// TestParseRCTagStrict_RejectsForeignPrefix proves parseRCTagStrict never
// launders a foreign tag into the spec's own namespace.
//
// The hazard is specific: a Spec with StrictPrefix off matches ANY alphabetic
// prefix on the read side, while Format always re-emits the spec's OWN prefix.
// Without a prefix check, "api1.2.0-rc.1" read under the default "v" grammar
// would parse and be reported with base "v1.2.0", colliding with a genuine
// v-prefixed base. In cleanupStaleDrafts that collision deletes the foreign
// draft. No production caller threads a non-strict spec (TagGrammarSpec forces
// StrictPrefix on), so this guards the function's own contract rather than a
// reachable defect.
func TestParseRCTagStrict_RejectsForeignPrefix(t *testing.T) {
	nonStrict := taggrammar.Default() // Prefix "v", StrictPrefix off.

	for _, tag := range []string{
		"api1.2.0-rc.1",
		"svc1.2.0-rc.1",
		"1.2.0-rc.1",
	} {
		t.Run(tag, func(t *testing.T) {
			base, rc, ok := parseRCTagStrict(nonStrict, tag)
			assert.False(t, ok, "a tag outside the spec's prefix must not parse")
			assert.Equal(t, "", base, "no base may be reported for a foreign tag")
			assert.Equal(t, -1, rc)
		})
	}

	// The spec's own tags still parse, including a zero-padded RC number.
	base, rc, ok := parseRCTagStrict(nonStrict, "v1.2.0-rc.007")
	assert.True(t, ok)
	assert.Equal(t, "v1.2.0", base)
	assert.Equal(t, 7, rc)
}

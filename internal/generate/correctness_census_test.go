package generate

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Generation-CORRECTNESS census.
//
// PR #631's census forces every emitted-affecting manifest field into
// actionlint coverage: the emitted output is VALID (real GitHub accepts it at
// parse). Validity is not correctness. A field can emit a workflow GitHub
// accepts that nonetheless does the WRONG thing: a matrix deploy that drops
// sha, a dry_run guard blind to a boolean, a dependent deploy gated on the
// immutable base result. Those defects (the Pass 10 "silent half") emit
// valid-but-wrong YAML that actionlint, act, and reviews all wave through.
//
// This census closes that gap the same way: it forces every emitted-affecting
// field to declare, in one reviewed table, whether its emitted output has a
// distinct SEMANTIC shape pinned by a named correctness assertion, or whether
// its only contract is validity + round-trip (already covered by the T1
// battery and the actionlint sweeps). A NEW emitted-affecting field reds
// TestCorrectnessCensus_EveryEmittedFieldClassified until it is classified, so
// "the next feature nobody asserts" cannot escape silently.
//
// The emitted-affecting surface is exactly the union the actionlint sweeps
// already drive: every key of emittedFieldRegistry (fields spliced into an emit
// sink) plus every key of emittedAffectingAllowlistMutators (allowlist fields
// that still change emitted workflow structure). This census reuses that
// surface rather than rebuilding it, so the two stay in lockstep by
// construction.
//
// HONEST LIMIT (stated once, here, and repeated on the runtime-semantic
// assertions): a generation-correctness assertion pins the emitted shape the
// AUTHOR believes correct. It catches a missing key, a wrong-versus-baseline
// expression, and a regression away from the pinned shape. It does NOT prove
// the shape is correct against real GitHub Actions RUNTIME semantics: whether
// `!= 'true'` actually rescues a boolean dry_run, whether a retry shim really
// rescues a failed ladder, whether a `needs:` gate fires as intended. That
// proof is the fleet's job (Part B). Where a field's correctness turns on
// runtime semantics, its assertion says so and defers the runtime proof to the
// fleet.
// ---------------------------------------------------------------------------

// correctnessMarker classifies an emitted-affecting field that carries no
// distinct correctness assertion. Exactly one marker or one assertion applies
// to each census field.
type correctnessMarker string

const (
	// markerValidityOnly: the field's only correctness contract is that its
	// emitted splice is valid and round-trips. Its value is a free scalar,
	// glob, name, or path with no distinct downstream semantic shape, already
	// pinned by the T1 hostile/good battery and the actionlint sweep.
	markerValidityOnly correctnessMarker = "validity-only"

	// markerStructural: the field's emitted shape is identical to a sibling
	// field's and is pinned by that sibling's named assertion (recorded in the
	// note). One assertion covers both; duplicating it would add no coverage.
	markerStructural correctnessMarker = "structural"

	// markerNotEmitted: the field never reaches emitted output as a splice
	// (validated-only enum, reserved block, or rejected combination), so there
	// is no emitted shape to pin. Mirrors a notEmittedAllowlist reason.
	markerNotEmitted correctnessMarker = "not-emitted"

	// markerFollowup: the field IS emitted-affecting with a distinct semantic
	// shape that warrants a correctness assertion not yet written. It is an
	// explicit, reviewed placeholder: the census keeps it visible so a
	// followup tranche can pin it. The enumerated markerFollowup set is the
	// remaining-fields backlog.
	markerFollowup correctnessMarker = "followup"
)

// correctnessCoverage is one census field's classification. Exactly one of
// assertion / marker is set; note is required for a marker and records the
// sibling for markerStructural or the reason for the others.
type correctnessCoverage struct {
	assertion string
	marker    correctnessMarker
	note      string
}

// emittedAffectingCensus returns the sorted union of every emitted-affecting
// manifest field path: the registry (direct emit-sink splices) plus the
// emitted-affecting allowlist (fields that change emitted structure without a
// raw splice). This is the exact surface the actionlint sweeps drive, so
// correctness coverage tracks validity coverage field-for-field.
func emittedAffectingCensus() []string {
	seen := map[string]bool{}
	for p := range emittedFieldRegistry {
		seen[p] = true
	}
	for p := range emittedAffectingAllowlistMutators {
		seen[p] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// reconcileCorrectnessCensus is the pure forcing core, tested directly with a
// synthetic census so its red behavior is proven without mutating the real
// schema. It returns the census fields with no coverage entry (missing) and the
// coverage entries naming a field no longer in the census (stale). A non-empty
// missing set is the guard's red state: a new emitted-affecting field has
// appeared with no correctness classification.
func reconcileCorrectnessCensus(census []string, coverage map[string]correctnessCoverage) (missing, stale []string) {
	present := map[string]bool{}
	for _, p := range census {
		present[p] = true
		if _, ok := coverage[p]; !ok {
			missing = append(missing, p)
		}
	}
	for p := range coverage {
		if !present[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	return missing, stale
}

// TestCorrectnessCensus_EveryEmittedFieldClassified is the forcing guard. Every
// emitted-affecting field (the actionlint-sweep surface) must carry exactly one
// correctnessCoverage entry: a named assertion pinning its emitted semantic
// shape, or a reviewed marker recording why it has none. A new field reds
// `missing`; a removed field reds `stale`; so the census cannot rot and a new
// emitted-affecting feature cannot ship with nobody asserting its output.
func TestCorrectnessCensus_EveryEmittedFieldClassified(t *testing.T) {
	missing, stale := reconcileCorrectnessCensus(emittedAffectingCensus(), correctnessCensus)

	if len(missing) > 0 {
		t.Errorf("emitted-affecting fields with no generation-correctness classification "+
			"(add each to correctnessCensus: name a correctness assertion pinning its emitted shape, "+
			"or a reviewed marker: markerValidityOnly / markerStructural / markerNotEmitted / markerFollowup):\n  %s",
			strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("correctnessCensus entries naming a field no longer emitted-affecting (remove them):\n  %s",
			strings.Join(stale, "\n  "))
	}
}

// TestCorrectnessCensus_ForcesNewField proves the forcing core reds when a
// brand-new emitted-affecting field appears with no classification. This is the
// durable promise: a future field registered into emittedFieldRegistry (or
// given an allowlist mutator) but never classified for correctness reds
// TestCorrectnessCensus_EveryEmittedFieldClassified. The synthetic census keeps
// the proof independent of the real schema.
func TestCorrectnessCensus_ForcesNewField(t *testing.T) {
	fake := "brand_new.emitted_affecting_field[]"
	missing, stale := reconcileCorrectnessCensus([]string{fake}, correctnessCensus)
	require.Contains(t, missing, fake,
		"an unclassified emitted-affecting field must red the correctness census")
	require.NotEmpty(t, stale,
		"the real coverage table must be reported stale against a census that omits its fields, "+
			"proving stale-entry detection is live")
}

// TestCorrectnessCensus_EntriesWellFormed pins the shape of every coverage
// entry: exactly one of assertion / marker, a note wherever a marker (or a
// structural pointer) needs one, and only recognized markers. A malformed entry
// (both set, neither set, an unknown marker, a markerStructural with no sibling
// note) reds here, so the table stays a real classification rather than a
// grab-bag.
func TestCorrectnessCensus_EntriesWellFormed(t *testing.T) {
	known := map[correctnessMarker]bool{
		markerValidityOnly: true, markerStructural: true,
		markerNotEmitted: true, markerFollowup: true,
	}
	for path, c := range correctnessCensus {
		hasAssertion := c.assertion != ""
		hasMarker := c.marker != ""
		if hasAssertion == hasMarker {
			t.Errorf("%s: exactly one of assertion / marker must be set (assertion=%q marker=%q)",
				path, c.assertion, c.marker)
			continue
		}
		if hasMarker {
			if !known[c.marker] {
				t.Errorf("%s: unknown marker %q", path, c.marker)
			}
			if c.note == "" {
				t.Errorf("%s: marker %q requires a note recording the reason or sibling", path, c.marker)
			}
		}
	}
}

// TestCorrectnessCensus_AssertionsExist grounds every named assertion in a real
// test. It scans the package's *_test.go sources for `func <Name>(t *testing.T)`
// declarations and fails on any correctnessCensus assertion (or markerStructural
// sibling note) that names a test which does not exist, so a typo or a deleted
// assertion cannot leave a census entry pointing at nothing.
func TestCorrectnessCensus_AssertionsExist(t *testing.T) {
	funcs := testFuncNames(t)

	var dangling []string
	for path, c := range correctnessCensus {
		if c.assertion != "" && !funcs[c.assertion] {
			dangling = append(dangling, path+" -> "+c.assertion)
		}
		if c.marker == markerStructural {
			for _, name := range extractTestNames(c.note) {
				if !funcs[name] {
					dangling = append(dangling, path+" -> "+name+" (structural sibling)")
				}
			}
		}
	}
	sort.Strings(dangling)
	if len(dangling) > 0 {
		t.Errorf("correctnessCensus entries naming a test that does not exist in this package:\n  %s",
			strings.Join(dangling, "\n  "))
	}
}

var testFuncRE = regexp.MustCompile(`func (Test[A-Za-z0-9_]+)\(t \*testing\.T\)`)

// testFuncNames returns the set of top-level test function names declared in the
// package's _test.go files.
func testFuncNames(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := filepath.Glob("*_test.go")
	require.NoError(t, err)
	names := map[string]bool{}
	for _, f := range entries {
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range testFuncRE.FindAllStringSubmatch(string(src), -1) {
			names[m[1]] = true
		}
	}
	return names
}

// extractTestNames pulls every Test... identifier out of a structural note so
// the sibling assertion it points at is grounded like a direct assertion.
func extractTestNames(note string) []string {
	return regexp.MustCompile(`Test[A-Za-z0-9_]+`).FindAllString(note, -1)
}

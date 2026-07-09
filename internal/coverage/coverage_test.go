package coverage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// emittedWorkflowKinds derives the set of generated workflow kinds directly from
// the generator source. It parses every non-test Go file under internal/generate
// and collects the basename of each .github/workflows/<name>.yaml string literal
// that classifies as a generated kind. This is the code-derived denominator: it
// never reads the registry, so the registry cannot define its own completeness.
func emittedWorkflowKinds(t *testing.T) map[string]struct{} {
	t.Helper()

	genDir := filepath.Join("..", "generate")
	entries, err := os.ReadDir(genDir)
	if err != nil {
		t.Fatalf("reading generator source dir %s: %v", genDir, err)
	}

	kinds := map[string]struct{}{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(genDir, name), nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if kind, ok := workflowKindFromLiteral(val); ok {
				kinds[kind] = struct{}{}
			}
			return true
		})
	}

	if len(kinds) == 0 {
		t.Fatal("derived no generated workflow kinds from internal/generate; the scan is broken")
	}
	return kinds
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestCoverage_EveryEmittedWorkflowKindHasExecutingCoverage is the coverage
// gate: it derives the emitted workflow kinds from the generator source and
// requires each to carry at least one executing-coverage reference in the
// registry. A generated workflow that no scenario, harness test, or fleet lane
// runs fails here.
func TestCoverage_EveryEmittedWorkflowKindHasExecutingCoverage(t *testing.T) {
	kinds := emittedWorkflowKinds(t)

	reg, err := Load()
	if err != nil {
		t.Fatalf("loading coverage registry: %v", err)
	}

	for _, kind := range sortedKeys(kinds) {
		cov, ok := reg.Kinds[kind]
		if !ok || cov.Refs() == 0 {
			t.Errorf("generated workflow kind %q has no executing coverage in internal/coverage/registry.yaml; "+
				"add an e2e scenario under e2e/scenarios/, an e2e harness test, or a fleet lane that runs it", kind)
		}
	}

	// A registry entry for a kind the generator no longer emits is stale and
	// masks a rename, so flag it rather than let it linger.
	for kind := range reg.Kinds {
		if _, ok := kinds[kind]; !ok {
			t.Errorf("registry records workflow kind %q that the generator no longer emits; remove or rename its entry", kind)
		}
	}
}

// TestCoverage_RegistryReferencesResolve keeps the registry honest: every
// referenced scenario file exists under e2e/scenarios/, every referenced harness
// test exists under e2e/, and every referenced fleet lane is a known lane. A
// reference that stops resolving fails here, so the registry cannot rot.
func TestCoverage_RegistryReferencesResolve(t *testing.T) {
	reg, err := Load()
	if err != nil {
		t.Fatalf("loading coverage registry: %v", err)
	}

	scenariosDir := filepath.Join("..", "..", "e2e", "scenarios")
	e2eDir := filepath.Join("..", "..", "e2e")

	for _, kind := range sortedRegistryKinds(reg) {
		cov := reg.Kinds[kind]
		if strings.TrimSpace(cov.Summary) == "" {
			t.Errorf("kind %q has no summary", kind)
		}
		for _, s := range cov.Scenarios {
			if _, statErr := os.Stat(filepath.Join(scenariosDir, s)); statErr != nil {
				t.Errorf("kind %q references scenario %q that does not exist under e2e/scenarios/", kind, s)
			}
		}
		for _, tf := range cov.E2ETests {
			if _, statErr := os.Stat(filepath.Join(e2eDir, tf)); statErr != nil {
				t.Errorf("kind %q references e2e test %q that does not exist under e2e/", kind, tf)
			}
		}
		for _, lane := range cov.FleetLanes {
			if _, ok := KnownFleetLanes[lane]; !ok {
				t.Errorf("kind %q references fleet lane %q that is not a known lane", kind, lane)
			}
		}
	}
}

// TestWorkflowKindFromLiteral_TemplatedOnlyKindIsCaptured drives the real
// derivation function to prove a component-templated path literal with no bare
// sibling still yields its bare kind, so a future workflow emitted only through a
// format string cannot slip past the coverage denominator. It also proves no
// printf verb ever survives into a derived kind.
func TestWorkflowKindFromLiteral_TemplatedOnlyKindIsCaptured(t *testing.T) {
	// A synthetic templated-only literal (no bare cascade-synthetic.yaml
	// sibling) must resolve to its bare kind.
	kind, ok := workflowKindFromLiteral(".github/workflows/cascade-synthetic-%s.yaml")
	if !ok {
		t.Fatalf("templated-only literal derived no kind; the gate would skip such a workflow")
	}
	if kind != "cascade-synthetic" {
		t.Errorf("derived kind = %q, want %q", kind, "cascade-synthetic")
	}
	if strings.ContainsRune(kind, '%') {
		t.Errorf("derived kind %q retains a printf verb; a phantom kind escaped", kind)
	}

	// Repeated trailing templated segments strip down to the same bare stem.
	if k, ok := workflowKindFromLiteral(".github/workflows/cascade-synthetic-%d-%s.yaml"); !ok || k != "cascade-synthetic" {
		t.Errorf("multi-segment literal derived (%q, %v), want (cascade-synthetic, true)", k, ok)
	}

	// The existing bare-literal path still resolves to its kind.
	if k, ok := workflowKindFromLiteral(".github/workflows/cascade-rollback.yaml"); !ok || k != "cascade-rollback" {
		t.Errorf("bare literal derived (%q, %v), want (cascade-rollback, true)", k, ok)
	}

	// A raw %-bearing name with no strippable trailing segment never becomes a
	// kind, so no phantom cascade-...-%s kind is ever produced.
	for _, bad := range []string{
		".github/workflows/cascade-foo%s.yaml",
		".github/workflows/cascade-%2s.yaml",
		".github/workflows/%s.yaml",
	} {
		if k, ok := workflowKindFromLiteral(bad); ok {
			t.Errorf("literal %q produced phantom kind %q, want no kind", bad, k)
		}
	}
}

func sortedRegistryKinds(reg *Registry) []string {
	keys := make([]string, 0, len(reg.Kinds))
	for k := range reg.Kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

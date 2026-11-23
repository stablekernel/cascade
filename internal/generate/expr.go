package generate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// Input value expression handling.
//
// Callback `inputs`/`env_inputs` values are GitHub Actions expressions. A value
// is classified as one of three forms:
//
//  1. Literal: the value contains no `${{ ... }}` wrapper. It is emitted as-is.
//  2. Passthrough expression: the entire (trimmed) value is a single
//     `${{ ... }}` expression whose root context is NOT cascade-owned. It is
//     emitted verbatim into the callback `with:` block so GitHub Actions
//     evaluates it at run time (e.g. `${{ vars.X }}`, `${{ secrets.Y }}`,
//     `${{ env.Z }}`, `${{ inputs.target_region }}`).
//  3. Cascade-resolved expression: the entire (trimmed) value is a single
//     `${{ state.<env>.<path> }}` expression. The `state.` namespace is owned
//     by cascade and resolved at generation time from the manifest `state:`
//     block. See resolveStateExpression.
//
// `${{ matrix.environment }}`, `${{ matrix.sha }}` and `${{ matrix.version }}`
// remain cascade-substituted placeholders on the promote matrix path: they are
// per-promotion values injected during matrix building, not run-time GHA
// contexts the operator controls. They are handled by the matrix-building logic
// and are intentionally NOT treated as passthrough here.

// exprPattern matches a value that is exactly one whole `${{ ... }}` expression
// (optionally surrounded by whitespace), capturing the inner expression body.
var exprPattern = regexp.MustCompile(`^\s*\$\{\{\s*(.+?)\s*\}\}\s*$`)

// stateRefPattern matches a cascade state reference body of the form
// `state.<env>.<dotted.path>` and captures the environment and the remaining
// path. The path may reference nested maps (builds, deploys, tags) by key.
var stateRefPattern = regexp.MustCompile(`^state\.([A-Za-z0-9_-]+)\.(.+)$`)

// isWholeExpression reports whether value is a single, whole `${{ ... }}`
// expression and returns the trimmed inner expression body.
func isWholeExpression(value string) (body string, ok bool) {
	m := exprPattern.FindStringSubmatch(value)
	if m == nil {
		return "", false
	}
	// Reject values that contain more than one expression (e.g. a templated
	// string mixing two `${{ }}` blocks); those are not pure passthroughs.
	if strings.Count(value, "${{") != 1 {
		return "", false
	}
	return m[1], true
}

// exprSpanPattern matches a single `${{ ... }}` expression span anywhere in
// a value, capturing the inner expression body. Unlike exprPattern (a whole-
// value match), a span may be embedded in surrounding literal text (e.g.
// `img-${{ matrix.os }}`), and a value may contain several spans. The
// non-greedy body cannot span past the first closing `}}`, matching the same
// limitation exprPattern already accepts.
var exprSpanPattern = regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)

// matrixContextPattern matches a reference to the GitHub Actions `matrix`
// context as a standalone token inside an expression body: `matrix` at the
// start of the body or preceded only by a delimiter that can open an operand
// in expression syntax -- whitespace, an argument separator, or an operator
// (so `format('{0}', matrix.os)` matches while `inputs.matrix.x`,
// `xmatrix.os`, and a string literal like `'ci/matrix.yaml'` do not, since
// `.`, identifier characters, and in-string punctuation cannot introduce the
// context) -- and not followed by an identifier character (so `matrix.os`
// and a bare `matrix` do, while `matrix-foo` does not).
var matrixContextPattern = regexp.MustCompile(`(^|[\s(,!&|?:=<>+*])matrix([^A-Za-z0-9_-]|$)`)

// referencesMatrixContext reports whether value references the GitHub
// Actions `matrix` context inside any `${{ ... }}` expression span, whether
// the value is a whole expression (`${{ matrix.os }}`,
// `${{ format('{0}', matrix.os) }}`) or an embedded one (`img-${{ matrix.os }}`).
// A reference must live inside an expression GitHub evaluates, so neither a
// plain literal that merely contains the text (`ci/matrix.yaml`,
// `matrix.build`) nor an in-expression string literal quoting it
// (`${{ 'ci/matrix.yaml' }}`) matches.
func referencesMatrixContext(value string) bool {
	for _, span := range exprSpanPattern.FindAllStringSubmatch(value, -1) {
		if matrixContextPattern.MatchString(span[1]) {
			return true
		}
	}
	return false
}

// needsContextPattern matches a reference to the GitHub Actions `needs`
// context as a standalone token inside an expression body, mirroring
// matrixContextPattern's operand-opening delimiters, and captures the job
// name the token is qualified with when there is one (`needs.setup.outputs.x`
// captures `setup`; a bare `needs` captures none). The same delimiter rules
// as matrixContextPattern apply, so `inputs.needs.x`, `xneeds.setup`, and a
// string literal like `'needs.setup'` do not match, and a job name must end
// at a non-identifier character, so `needs.setup.outputs.x` captures exactly
// `setup`.
var needsContextPattern = regexp.MustCompile(`(^|[\s(,!&|?:=<>+*])needs(\.([A-Za-z0-9_-]+))?([^A-Za-z0-9_-]|$)`)

// referencesNeedsContextExcept reports whether value references the GitHub
// Actions `needs` context for any job other than keepJob inside any
// `${{ ... }}` expression span, whether the value is a whole expression
// (`${{ needs.setup.outputs.head_sha }}`) or an embedded one
// (`sha-${{ needs.setup.outputs.head_sha }}`). A reference must live inside
// an expression GitHub evaluates, so neither a plain literal that merely
// contains the text (`needs.setup`) nor an in-expression string literal
// quoting it (`${{ 'needs.setup' }}`) matches. A bare `needs` token matches
// (it references the whole context, every job at once, not just keepJob's
// entry). The hotfix lane passes "context", the hotfix build job's only
// need, so the references it can still resolve do not match.
func referencesNeedsContextExcept(value, keepJob string) bool {
	for _, span := range exprSpanPattern.FindAllStringSubmatch(value, -1) {
		for _, m := range needsContextPattern.FindAllStringSubmatch(span[1], -1) {
			if m[3] != keepJob {
				return true
			}
		}
	}
	return false
}

// isStateExpression reports whether body (the inner text of a `${{ }}`
// expression) is a cascade-owned `state.*` reference.
func isStateExpression(body string) bool {
	return stateRefPattern.MatchString(strings.TrimSpace(body))
}

// resolveStateExpression resolves a `state.<env>.<path>` reference against the
// manifest state. Supported paths:
//
//	state.<env>.sha
//	state.<env>.version
//	state.<env>.builds.<name>.sha|version|artifact_id|tags.<key>
//	state.<env>.deploys.<name>.sha|version|tags.<key>
//
// On a missing reference it returns ("", false) so callers can apply a default
// or surface an error.
func resolveStateExpression(state map[string]*config.EnvState, body string) (string, bool) {
	m := stateRefPattern.FindStringSubmatch(strings.TrimSpace(body))
	if m == nil {
		return "", false
	}
	env, path := m[1], m[2]
	envState, ok := state[env]
	if !ok || envState == nil {
		return "", false
	}

	parts := strings.Split(path, ".")
	switch parts[0] {
	case "sha":
		return envState.SHA, envState.SHA != ""
	case "version":
		return envState.Version, envState.Version != ""
	case "builds":
		// builds.<name>.<field>[.<key>]
		if len(parts) < 3 {
			return "", false
		}
		b, ok := envState.Builds[parts[1]]
		if !ok || b == nil {
			return "", false
		}
		return resolveBuildField(b, parts[2:])
	case "deploys":
		if len(parts) < 3 {
			return "", false
		}
		d, ok := envState.Deploys[parts[1]]
		if !ok || d == nil {
			return "", false
		}
		return resolveDeployField(d, parts[2:])
	}
	return "", false
}

func resolveBuildField(b *config.BuildState, fields []string) (string, bool) {
	switch fields[0] {
	case "sha":
		return b.SHA, b.SHA != ""
	case "artifact_id":
		return b.ArtifactID, b.ArtifactID != ""
	case "tags":
		if len(fields) < 2 {
			return "", false
		}
		v, ok := b.Tags[fields[1]]
		return v, ok && v != ""
	}
	return "", false
}

func resolveDeployField(d *config.DeployState, fields []string) (string, bool) {
	switch fields[0] {
	case "sha":
		return d.SHA, d.SHA != ""
	case "version":
		return d.Version, d.Version != ""
	case "tags":
		if len(fields) < 2 {
			return "", false
		}
		v, ok := d.Tags[fields[1]]
		return v, ok && v != ""
	}
	return "", false
}

// classifiedInput is the emit-time classification of a single input value.
type inputKind int

const (
	// inputLiteral is a plain value with no expression wrapper.
	inputLiteral inputKind = iota
	// inputPassthrough is a whole `${{ ... }}` expression that GHA evaluates at
	// run time; emitted verbatim.
	inputPassthrough
	// inputStateRef is a whole `${{ state.* }}` expression resolved by cascade
	// at generation time.
	inputStateRef
)

// classifyInputValue determines how an input value should be emitted.
func classifyInputValue(value string) inputKind {
	body, ok := isWholeExpression(value)
	if !ok {
		return inputLiteral
	}
	if isStateExpression(body) {
		return inputStateRef
	}
	// matrix.* placeholders are cascade-substituted per promotion, not
	// passthrough. Treat them as literals so the matrix-substitution path
	// (and any non-matrix substitution) handles them.
	if strings.HasPrefix(strings.TrimSpace(body), "matrix.") {
		return inputLiteral
	}
	return inputPassthrough
}

// resolveInputValue computes the emitted string for an input value given the
// manifest state. Literals and passthrough expressions are returned unchanged
// (passthroughs survive verbatim to the callback `with:` block). State
// references are resolved against the manifest; an unresolved state reference
// returns an error so generation fails loudly rather than emitting a dead ref.
func resolveInputValue(value string, state map[string]*config.EnvState) (string, error) {
	switch classifyInputValue(value) {
	case inputStateRef:
		body, _ := isWholeExpression(value)
		resolved, ok := resolveStateExpression(state, body)
		if !ok {
			return "", fmt.Errorf("unresolved prior-state reference %q (no matching value in manifest state)", value)
		}
		return resolved, nil
	default:
		// Literal or passthrough: emit verbatim.
		return value, nil
	}
}

// looksLikeUnwrappedExpression reports whether a literal value appears to be an
// expression the operator forgot to wrap in `${{ }}` (e.g. a bare `vars.X` or
// `state.prod.sha`). Used by validation to warn, not to alter behavior.
var unwrappedExprPattern = regexp.MustCompile(`^(vars|secrets|env|state|matrix|inputs|needs|github)\.[A-Za-z0-9_.-]+$`)

func looksLikeUnwrappedExpression(value string) bool {
	if _, ok := isWholeExpression(value); ok {
		return false
	}
	if strings.Contains(value, "${{") {
		return false
	}
	return unwrappedExprPattern.MatchString(strings.TrimSpace(value))
}

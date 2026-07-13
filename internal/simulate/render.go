package simulate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// renderOptions carry the display-only inputs the human renderer needs beyond
// the Result itself. They never affect the JSON output.
type renderOptions struct {
	repoURL string
}

// RenderOption configures the human renderer.
type RenderOption func(*renderOptions)

// WithRepoURL supplies the repository browse URL (for example
// https://github.com/owner/name) used to build the per-environment compare link
// in the state diff. When empty, the compare line is omitted rather than a
// broken URL emitted.
func WithRepoURL(url string) RenderOption {
	return func(o *renderOptions) { o.repoURL = url }
}

// RenderHuman writes a human-readable report of the simulation to w. The output
// is deterministic: environment keys are sorted and run-stamped timestamps are
// excluded from the diff.
func (r *Result) RenderHuman(w io.Writer, opts ...RenderOption) error {
	o := renderOptions{}
	for _, opt := range opts {
		opt(&o)
	}

	if _, err := fmt.Fprintf(w, "Simulating: %s\n", r.ActionDescribe); err != nil {
		return err
	}

	if err := r.renderDiff(w, o.repoURL); err != nil {
		return err
	}
	if err := r.renderChain(w); err != nil {
		return err
	}
	if err := r.renderEffects(w); err != nil {
		return err
	}
	if r.Note != "" {
		if _, err := fmt.Fprintf(w, "\n%s\n", r.Note); err != nil {
			return err
		}
	}
	return nil
}

// labeledValue is one rendered row inside an environment's diff block: a label
// and its value. Rows are aligned on their labels for a scannable column.
type labeledValue struct {
	label string
	value string
}

func (r *Result) renderDiff(w io.Writer, repoURL string) error {
	if _, err := fmt.Fprintln(w, "State diff:"); err != nil {
		return err
	}
	if !r.Diff.Changed() {
		_, err := fmt.Fprintln(w, "  (no state change)")
		return err
	}

	for _, env := range r.Diff.Envs {
		if _, err := fmt.Fprintf(w, "  %s:\n", env.Environment); err != nil {
			return err
		}
		rows := envDiffRows(env, repoURL)
		width := 0
		for _, row := range rows {
			if len(row.label) > width {
				width = len(row.label)
			}
		}
		for _, row := range rows {
			if _, err := fmt.Fprintf(w, "    %-*s  %s\n", width, row.label, row.value); err != nil {
				return err
			}
		}
	}
	return nil
}

// envDiffRows returns the ordered, aligned change rows for one environment. SHA
// values are shortened to 7 characters, and a compare link is inserted after the
// sha row when both endpoints are real shas and the repository is known.
func envDiffRows(env EnvDiff, repoURL string) []labeledValue {
	var rows []labeledValue
	if env.Version.Changed {
		rows = append(rows, labeledValue{"version:", fmt.Sprintf("%s -> %s", env.Version.From, env.Version.To)})
	}
	if env.SHA.Changed {
		rows = append(rows, labeledValue{"sha:", fmt.Sprintf("%s -> %s", shortOrNone(env.SHA.From), shortOrNone(env.SHA.To))})
		if url := compareURL(repoURL, env.SHA.From, env.SHA.To); url != "" {
			rows = append(rows, labeledValue{"diff:", url})
		}
	}
	for _, d := range env.Deploys {
		if d.SHA.Changed {
			rows = append(rows, labeledValue{
				fmt.Sprintf("deploy/%s sha:", d.Name),
				fmt.Sprintf("%s -> %s", shortOrNone(d.SHA.From), shortOrNone(d.SHA.To)),
			})
		}
		if d.Version.Changed {
			rows = append(rows, labeledValue{
				fmt.Sprintf("deploy/%s version:", d.Name),
				fmt.Sprintf("%s -> %s", d.Version.From, d.Version.To),
			})
		}
	}
	if env.Divergence.Changed {
		rows = append(rows, labeledValue{"divergence:", fmt.Sprintf("%s -> %s", env.Divergence.From, env.Divergence.To)})
	}
	if env.PreviousRing.Changed {
		rows = append(rows, labeledValue{"previous ring:", fmt.Sprintf("%s -> %s", env.PreviousRing.From, env.PreviousRing.To)})
	}
	return rows
}

// compareURL builds a compare link between two shas, or "" when the repository
// is unknown or either endpoint is not a real sha. Both endpoints are shortened
// to match the sha row.
func compareURL(repoURL, from, to string) string {
	if repoURL == "" || from == "" || to == "" || from == noneValue || to == noneValue {
		return ""
	}
	return fmt.Sprintf("%s/compare/%s...%s", repoURL, shortOrNone(from), shortOrNone(to))
}

// renderChain writes the multi-environment cherry-pick chain: one numbered step
// per environment the fix elevates through, bottom-up from the second
// environment up to and including the target. It is omitted entirely when the
// action produced no chain (every action other than a hotfix, and a hotfix whose
// chain could not be computed). The steps render in the same effect vocabulary
// as the effect list.
func (r *Result) renderChain(w io.Writer) error {
	if len(r.Chain) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "Cherry-pick chain (in order):"); err != nil {
		return err
	}
	for i, e := range r.Chain {
		if _, err := fmt.Fprintf(w, "  %d. %s\n", i+1, effectLine(e)); err != nil {
			return err
		}
	}
	return nil
}

func (r *Result) renderEffects(w io.Writer) error {
	if _, err := fmt.Fprintln(w, "Effects (in order):"); err != nil {
		return err
	}
	if len(r.Effects) == 0 {
		_, err := fmt.Fprintln(w, "  (none)")
		return err
	}
	for i, e := range r.Effects {
		if _, err := fmt.Fprintf(w, "  %d. %s\n", i+1, effectLine(e)); err != nil {
			return err
		}
	}
	return nil
}

// effectLine renders one effect as a human line. The run disposition carries no
// prefix; skip and gate are worded ("skipped", "gated"). The promotion deploy
// marker is trimmed to "deploy <target> from <source>" because the state diff
// already carries the sha and version; every other effect appends its detail in
// parentheses.
func effectLine(e Effect) string {
	if e.Disposition == DispositionRun && e.Action == "deploy" && strings.HasPrefix(e.Detail, "from ") {
		return fmt.Sprintf("%s %s %s", e.Action, e.Target, trimParenthetical(e.Detail))
	}

	prefix := ""
	switch e.Disposition {
	case DispositionSkip:
		prefix = "skipped "
	case DispositionGate:
		prefix = "gated "
	case DispositionRun:
		prefix = ""
	}

	body := fmt.Sprintf("%s%s %s", prefix, e.Action, e.Target)
	if e.Detail != "" {
		body += fmt.Sprintf(" (%s)", e.Detail)
	}
	return body
}

// trimParenthetical drops a trailing " (...)" clause from a detail string,
// leaving the leading phrase (for example "from dev").
func trimParenthetical(detail string) string {
	if i := strings.Index(detail, " ("); i >= 0 {
		return detail[:i]
	}
	return detail
}

// RenderJSON writes the simulation result as deterministic, 2-space-indented
// JSON to w.
func (r *Result) RenderJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

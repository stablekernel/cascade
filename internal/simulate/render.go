package simulate

import (
	"encoding/json"
	"fmt"
	"io"
)

// RenderHuman writes a human-readable report of the simulation to w. The output
// is deterministic: environment keys are sorted and run-stamped timestamps are
// excluded from the diff.
func (r *Result) RenderHuman(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "Simulating: %s\n", r.ActionDescribe); err != nil {
		return err
	}

	if err := r.renderDiff(w); err != nil {
		return err
	}
	return r.renderEffects(w)
}

func (r *Result) renderDiff(w io.Writer) error {
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
		lines := envDiffLines(env)
		for _, line := range lines {
			if _, err := fmt.Fprintf(w, "    %s\n", line); err != nil {
				return err
			}
		}
	}
	return nil
}

// envDiffLines returns the ordered, rendered change lines for one environment.
func envDiffLines(env EnvDiff) []string {
	var lines []string
	if env.Version.Changed {
		lines = append(lines, fmt.Sprintf("version: %s -> %s", env.Version.From, env.Version.To))
	}
	if env.SHA.Changed {
		lines = append(lines, fmt.Sprintf("sha:     %s -> %s", env.SHA.From, env.SHA.To))
	}
	for _, d := range env.Deploys {
		if d.SHA.Changed {
			lines = append(lines, fmt.Sprintf("deploy/%s sha: %s -> %s", d.Name, d.SHA.From, d.SHA.To))
		}
		if d.Version.Changed {
			lines = append(lines, fmt.Sprintf("deploy/%s version: %s -> %s", d.Name, d.Version.From, d.Version.To))
		}
	}
	if env.Divergence.Changed {
		lines = append(lines, fmt.Sprintf("divergence: %s -> %s", env.Divergence.From, env.Divergence.To))
	}
	if env.PreviousRing.Changed {
		lines = append(lines, fmt.Sprintf("previous ring: %s -> %s", env.PreviousRing.From, env.PreviousRing.To))
	}
	return lines
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
		line := fmt.Sprintf("  %d. [%s] %s %s", i+1, e.Disposition, e.Action, e.Target)
		if e.Detail != "" {
			line += fmt.Sprintf(" (%s)", e.Detail)
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// RenderJSON writes the simulation result as deterministic, 2-space-indented
// JSON to w.
func (r *Result) RenderJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

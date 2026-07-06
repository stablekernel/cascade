package pinreconcile

import (
	"errors"
	"fmt"
)

// ErrAmbiguousSource means governed source files disagree on the ref for one
// action key. The engine refuses that key and falls through to a comment rather
// than guessing, matching the consensus-over-source rule.
var ErrAmbiguousSource = errors.New("governed source files disagree on the action ref")

// consensusRef folds every source-observed ref for one action key into a single
// adopted value, refusing when the sources disagree. Only source files feed this;
// generated files are targets and are never read back.
func consensusRef(action string, refs []string) (string, error) {
	if len(refs) == 0 {
		return "", fmt.Errorf("%s: no source ref observed", action)
	}
	first := refs[0]
	for _, r := range refs[1:] {
		if r != first {
			return "", fmt.Errorf("%w: %s (%q vs %q)", ErrAmbiguousSource, action, first, r)
		}
	}
	return first, nil
}

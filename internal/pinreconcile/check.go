package pinreconcile

import (
	"encoding/json"
	"fmt"
	"os"
)

// CheckResult is the data-only detector output. The companions read it
// strictly as data (never executed) to decide whether to run and, if so,
// which governed refs changed. It never carries the target PR number: the
// companion derives that only from trusted workflow_run metadata.
type CheckResult struct {
	Relevant    bool              `json:"relevant"`
	ChangedRefs map[string]string `json:"changed_refs"`
}

// Check computes relevance without writing anything, so a fork-safe read-only
// job can run it. It reuses PlanAdoptions, so relevance and the changed set
// are exactly what a subsequent reconcile would adopt.
func Check(in Input) (CheckResult, error) {
	adopts, err := PlanAdoptions(in)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Relevant: adopts.Relevant(), ChangedRefs: adopts.Pins}, nil
}

// WriteCheckArtifact serializes the detector result for the companion to read.
func WriteCheckArtifact(path string, res CheckResult) error {
	b, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("marshaling check result: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("writing check artifact %s: %w", path, err)
	}
	return nil
}

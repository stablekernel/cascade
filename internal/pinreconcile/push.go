// Package pinreconcile implements the pin-reconciliation engine: adopting an
// external action-pin change back into the manifest and regenerating so every
// owned file agrees with it again.
package pinreconcile

// StagePathspec returns the ONLY pathspec a reconcile commit may stage, so a
// crafted working-tree change cannot ride along (never git add -A). The
// common case is manifest-only (Contents: write); pushWorkflows is set only
// when a regenerate must push workflow files (the Workflows: write path).
func StagePathspec(manifestPath string, pushWorkflows bool) []string {
	spec := []string{manifestPath}
	if pushWorkflows {
		spec = append(spec, ".github/workflows/*.yaml")
	}
	return spec
}

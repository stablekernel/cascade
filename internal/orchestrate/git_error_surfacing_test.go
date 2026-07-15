package orchestrate

import (
	"testing"

	"github.com/stablekernel/cascade/internal/config"
)

// TestCommitAndPush_GitStatusFailureSurfaces requires the finalize state commit
// to fail loudly when it cannot even determine whether the manifest changed. A
// swallowed `git status` error reads as empty output, which is
// indistinguishable from "no changes": finalize then reports success without
// committing or pushing the state write, silently dropping it.
func TestCommitAndPush_GitStatusFailureSurfaces(t *testing.T) {
	o := &Orchestrator{
		baseDir:     t.TempDir(), // not a git repository: git status fails here
		configPath:  "manifest.yaml",
		environment: "dev",
		cicdFile:    &config.CICDFile{},
	}
	if err := o.commitAndPush("v1.0.0"); err == nil {
		t.Fatal("commitAndPush() with a failing git status = nil, want the failure surfaced instead of a silent no-op success")
	}
}

// TestCalculateVersion_TagLookupFailureSurfaces requires the no-environment
// version derivation to propagate a failing tag lookup rather than degrade it
// to a warning: continuing with empty data restarts an established project at
// v0.1.0-rc.0, a regressive mint that can collide with already-published tags.
func TestCalculateVersion_TagLookupFailureSurfaces(t *testing.T) {
	o := &Orchestrator{
		baseDir:     t.TempDir(), // not a git repository: every tag lookup fails
		environment: DefaultStateKey,
		cicdFile:    &config.CICDFile{Config: &config.TrunkConfig{}},
	}
	if _, err := o.calculateVersion(); err == nil {
		t.Fatal("calculateVersion() with failing tag lookups = nil error, want the git failure surfaced instead of a wrong or regressive version")
	}
}

// TestCalculateComponentVersion_TagLookupFailureSurfaces is the component-scoped
// sibling: a component whose strict-namespace tag lookups fail must not silently
// restart at its component v0.1.0-rc.0 or under-bump from an empty commit range.
func TestCalculateComponentVersion_TagLookupFailureSurfaces(t *testing.T) {
	const manifest = `ci:
  config:
    trunk_branch: main
    environments:
      - dev
    components:
      api:
        path: services/api
        tag_grammar:
          prefix: api-
`
	file, err := config.ParseManifestBytes([]byte(manifest), "ci")
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	o := &Orchestrator{
		baseDir:     t.TempDir(), // not a git repository: every tag lookup fails
		environment: "dev",
		component:   "api",
		cicdFile:    file,
	}
	if _, err := o.calculateVersion(); err == nil {
		t.Fatal("calculateVersion() for a component with failing tag lookups = nil error, want the git failure surfaced")
	}
}

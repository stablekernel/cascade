package config

import "testing"

// TestReleaseBuild_RejectsLegacyReleaseKey proves the former user-facing
// release: config block is now an unknown key with a did-you-mean that points at
// release_build:, not at the similarly-named release_token/release_trigger keys.
func TestReleaseBuild_RejectsLegacyReleaseKey(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev]
release:
  workflow: .github/workflows/release-build.yaml
`)
	errs := Validate(cfg)
	if !hasErrContaining(errs, `unknown field "release"; did you mean "release_build"`) {
		t.Fatalf("expected release did-you-mean pointing at release_build, got %v", errs)
	}
}

// TestReleaseBuild_ParsesNewKey proves the renamed key parses into the modeled
// ReleaseBuild field and validates clean, so the post-publish dispatch workflow
// reaches the generator.
func TestReleaseBuild_ParsesNewKey(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev]
release_build:
  workflow: .github/workflows/release-build.yaml
`)
	if cfg.ReleaseBuild == nil || cfg.ReleaseBuild.Workflow != ".github/workflows/release-build.yaml" {
		t.Fatalf("release_build.workflow did not parse: %#v", cfg.ReleaseBuild)
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
}

// TestReleaseBuild_ReservedVersionOverrides proves the reserved pointer is still
// rejected under its renamed path.
func TestReleaseBuild_ReservedVersionOverrides(t *testing.T) {
	cfg := parseInline(t, `
trunk_branch: main
environments: [dev]
release_build:
  version_overrides:
    dir: .cascade/overrides
`)
	if !hasErrContaining(Validate(cfg), "release_build.version_overrides is reserved and not implemented in this cascade version") {
		t.Fatalf("expected reserved release_build.version_overrides rejection, got %v", Validate(cfg))
	}
}

// TestReleaseBuild_TagErrorsUseRenamedPath proves the tag-reference validation
// errors name the field by its current path (release_build.tag). A message
// pointing at the old release.tag would send a user to a key that no longer
// exists and would itself trip the unknown-key did-you-mean, so the text must
// track the rename.
func TestReleaseBuild_TagErrorsUseRenamedPath(t *testing.T) {
	t.Run("invalid format", func(t *testing.T) {
		cfg := parseInline(t, `
trunk_branch: main
environments: [dev]
release_build:
  tag: nodot
`)
		errs := Validate(cfg)
		if !hasErrContaining(errs, "release_build.tag invalid format") {
			t.Fatalf("expected release_build.tag invalid-format error, got %v", errs)
		}
		if hasErrContaining(errs, "release.tag ") || hasErrContaining(errs, "release.tag invalid") {
			t.Fatalf("error must not reference the removed release.tag path, got %v", errs)
		}
	})
	t.Run("unknown callback", func(t *testing.T) {
		cfg := parseInline(t, `
trunk_branch: main
environments: [dev]
release_build:
  tag: ghost.output
`)
		errs := Validate(cfg)
		if !hasErrContaining(errs, "release_build.tag references unknown callback: ghost") {
			t.Fatalf("expected release_build.tag unknown-callback error, got %v", errs)
		}
	})
}

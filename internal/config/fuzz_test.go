package config

import "testing"

// FuzzLoadManifest exercises the manifest parse-and-validate path with mutated
// bytes to assert the parser and validator never panic. Malformed input must
// surface as an error, never a crash. Errors are an acceptable outcome; a panic
// (or any other non-error abort) is a failure.
//
// Run the seed corpus as a normal test: go test -run FuzzLoadManifest ./internal/config/
// Run active fuzzing locally: go test -fuzz=FuzzLoadManifest ./internal/config/
func FuzzLoadManifest(f *testing.F) {
	seeds := []string{
		// Minimal valid manifest.
		`ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
`,
		// Manifest exercising config-level fields, pins, and rollout.
		`ci:
  config:
    trunk_branch: main
    environments: [dev, staging, prod]
    runs_on: ubuntu-latest
    job_timeout_minutes: 30
    pin_mode: sha
    action_pins:
      actions/checkout: v4.2.2
    validate:
      workflow: .github/workflows/validate.yaml
    builds:
      - name: cli
        workflow: .github/workflows/build-cli.yaml
    changelog:
      contributors: true
`,
		// Manifest carrying a state section alongside config.
		`ci:
  config:
    trunk_branch: main
    environments: [dev, prod]
  state:
    prerelease:
      sha: a47eb32931502bf345db5c5eba67d17f7ef6b886
      version: v0.3.0-rc.1
`,
		// Empty body and a body missing the required key: both must error, not panic.
		``,
		`not_ci:
  config:
    trunk_branch: main
`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// ParseManifestBytes is the entry point for a manifest read from a
		// branch ref. A nil error implies a config we can hand to Validate.
		file, err := ParseManifestBytes(data, DefaultManifestKey)
		if err != nil {
			return
		}
		if file == nil || file.Config == nil {
			return
		}
		// Validate must tolerate any parsed config without panicking. The
		// returned messages are not asserted; only the absence of a panic is.
		_ = Validate(file.Config)
	})
}

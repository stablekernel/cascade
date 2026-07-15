// Package setupcli holds the hermetic fixture tests for the setup-cli
// composite action's install script (.github/actions/setup-cli/install.sh).
// Go tooling skips dot-directories, so the tests live here and locate the
// script relative to the repository root. The tests stub the gh CLI (and
// cosign) on PATH, so no network or real release is involved: they build a
// fake release directory (archive plus checksums.txt), run the script against
// it, and assert the integrity and authenticity gates fail closed.
package setupcli

package generate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/config"
	"github.com/stretchr/testify/require"
)

// TestSetupCLIInstallScript_MatchesActionCopy guards the one-source-of-truth
// invariant: the install.sh embedded into this package (and emitted inline in
// binary mode) must be byte-for-byte identical to the composite action's
// install.sh. go:embed cannot reach across the package boundary into the
// dot-directory action tree, so the script is copied here; this gate ensures the
// copy never drifts from the canonical script the setupcli hermetic tests run.
func TestSetupCLIInstallScript_MatchesActionCopy(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	root := filepath.Join(filepath.Dir(file), "..", "..")
	canonical := filepath.Join(root, ".github", "actions", "setup-cli", "install.sh")
	want, err := os.ReadFile(canonical)
	require.NoError(t, err, "canonical install.sh missing")
	require.Equal(t, string(want), setupCLIInstallScript,
		"embedded setup_cli_install.sh drifted from .github/actions/setup-cli/install.sh; "+
			"copy the action's install.sh over internal/generate/setup_cli_install.sh")
}

// TestParseCLIInstallMode covers the flag parser: the default and explicit
// action, binary, and a loud rejection of anything else.
func TestParseCLIInstallMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    cliInstallMode
		wantErr bool
	}{
		{"", cliInstallModeAction, false},
		{"action", cliInstallModeAction, false},
		{"binary", cliInstallModeBinary, false},
		{"Action", cliInstallModeAction, true},
		{"bin", cliInstallModeAction, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseCLIInstallMode(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestWriteSetupCLIStep_ActionModeUnchanged pins the default action-mode output
// byte-for-byte, so the binary-mode branch can never perturb the emission every
// existing golden depends on. It covers the two field orderings and the if:
// guard the call sites use.
func TestWriteSetupCLIStep_ActionModeUnchanged(t *testing.T) {
	t.Parallel()

	tokenFirst := func() string {
		var sb strings.Builder
		writeSetupCLIStep(&sb, setupCLIStep{
			ref:                "v1.2.3",
			version:            "latest",
			token:              "${{ secrets.RELEASE_TOKEN }}",
			tokenBeforeVersion: true,
		})
		return sb.String()
	}()
	require.Equal(t,
		"      - name: Setup CLI\n"+
			"        uses: stablekernel/cascade/.github/actions/setup-cli@v1.2.3\n"+
			"        with:\n"+
			"          token: ${{ secrets.RELEASE_TOKEN }}\n"+
			"          version: latest\n",
		tokenFirst)

	versionFirstWithIf := func() string {
		var sb strings.Builder
		writeSetupCLIStep(&sb, setupCLIStep{
			ref:     "v1.2.3",
			version: "latest",
			token:   "${{ github.token }}",
			ifExpr:  "steps.resolve.outputs.relevant == 'true'",
		})
		return sb.String()
	}()
	require.Equal(t,
		"      - name: Setup CLI\n"+
			"        if: steps.resolve.outputs.relevant == 'true'\n"+
			"        uses: stablekernel/cascade/.github/actions/setup-cli@v1.2.3\n"+
			"        with:\n"+
			"          version: latest\n"+
			"          token: ${{ github.token }}\n",
		versionFirstWithIf)
}

// TestWriteSetupCLIStepBinary_VerifyContract asserts the binary-mode emission
// carries every guarantee the composite action provides while using no
// third-party action.
func TestWriteSetupCLIStepBinary_VerifyContract(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	writeSetupCLIStep(&sb, setupCLIStep{
		ref:                "v1.2.3",
		version:            "latest",
		token:              "${{ secrets.RELEASE_TOKEN }}",
		tokenBeforeVersion: true,
		ifExpr:             "steps.resolve.outputs.relevant == 'true'",
		installMode:        cliInstallModeBinary,
	})
	out := sb.String()

	// No third-party action of any kind: not the setup-cli composite action, not
	// the cosign installer.
	require.NotContains(t, out, "uses: stablekernel/cascade/.github/actions/setup-cli@",
		"binary mode must not reference the setup-cli composite action")
	require.NotContains(t, out, "sigstore/cosign-installer",
		"binary mode must install cosign by direct download, not the installer action")
	require.NotContains(t, out, "\n        uses:",
		"binary mode must emit no uses: step at all")

	// Token and version flow through env, never spliced into the run: script, so
	// no ${{ }} appears inside the script body (which real GitHub would evaluate
	// at parse time).
	require.Contains(t, out, "        env:\n")
	require.Contains(t, out, "          GH_TOKEN: ${{ secrets.RELEASE_TOKEN }}\n")
	require.Contains(t, out, "          CASCADE_CLI_VERSION: latest\n")

	// The step-level if: guard is honored.
	require.Contains(t, out, "        if: steps.resolve.outputs.relevant == 'true'\n")

	// Mandatory sha256 integrity gate (from the shared install.sh).
	require.Contains(t, out, "checksums.txt")
	require.Contains(t, out, "sha256sum -c")

	// Keyless cosign verification with the exact identity and issuer the action
	// uses. These are load-bearing: a wrong regexp or issuer would verify against
	// the wrong signer.
	require.Contains(t, out, "cosign verify-blob")
	require.Contains(t, out, "--certificate-identity-regexp='^https://github.com/stablekernel/cascade/'")
	require.Contains(t, out, "--certificate-oidc-issuer=https://token.actions.githubusercontent.com")

	// cosign installed by pinned, checksum-verified direct download.
	require.Contains(t, out, "https://github.com/sigstore/cosign/releases/download/")
	require.Contains(t, out, "COSIGN_VERSION=")

	// The loud sha256-only fallback semantics survive (never a silent downgrade).
	require.Contains(t, out, "::warning::")

	// The run: body carries no ${{ so GitHub does not evaluate the script at parse.
	body := out[strings.Index(out, "        run: |\n"):]
	require.NotContains(t, body, "${{",
		"the binary-mode run: script must contain no ${{ }} expression")
}

// TestGenerateBinaryMode_ActionlintCleanAndThirdPartyFree renders a full
// orchestrate workflow in binary mode and asserts (a) actionlint accepts it,
// proving real GitHub would parse the inline install step (a literal ${{ }} in a
// run: script would 422 at parse), and (b) it references no third-party action.
//
// Only the emitted YAML is exercised here. The download-and-verify half is not
// run: act (the e2e runner) and this unit test cannot fetch a real GitHub
// release or run cosign, so faking it would prove nothing. The executed proof of
// the verify gates (good checksum, tampered checksum, missing entry, rejected
// signature) lives in internal/setupcli against the same install.sh this mode
// embeds byte-for-byte, guarded by TestSetupCLIInstallScript_MatchesActionCopy.
func TestGenerateBinaryMode_ActionlintCleanAndThirdPartyFree(t *testing.T) {
	actionlint := locateActionlint(t)
	dir, wfDir := stageActionlintProject(t)

	cfg := &config.TrunkConfig{
		TrunkBranch:  "main",
		Environments: config.EnvNames("staging", "production"),
		Builds: []config.BuildConfig{
			{Name: "app", Workflow: "build.yaml", Triggers: []string{"src/**"}},
		},
		Deploys: []config.DeployConfig{
			{Name: "app", Workflow: "deploy.yaml", DependsOn: []string{"app"}},
		},
	}

	g := NewGenerator(cfg, dir)
	g.setInstallMode(cliInstallModeBinary)
	content, err := g.Generate()
	require.NoError(t, err)

	require.NotContains(t, content, "uses: stablekernel/cascade/.github/actions/setup-cli@")
	require.NotContains(t, content, "sigstore/cosign-installer")
	require.Contains(t, content, "cosign verify-blob")

	path := filepath.Join(wfDir, "orchestrate.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	out, runErr := runActionlint(t, actionlint, path)
	require.NoErrorf(t, runErr, "actionlint rejected the binary-mode orchestrate workflow:\n%s", out)
}

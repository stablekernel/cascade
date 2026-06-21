package generate

import (
	"strings"

	"github.com/stablekernel/cascade/internal/config"
)

// Shared step ids for the App-token minting steps. One id per seam so a job that
// consumes both the release and state tokens can carry both mint steps without
// collision, and every consuming ref can name the matching minted output.
const (
	releaseAppTokenStepID = "cascade-release-app-token"
	stateAppTokenStepID   = "cascade-state-app-token"
)

// appTokenServerGuard keeps the minting step on real GitHub only. On act/gitea
// the step is skipped and its output is empty, so the consuming ref falls back
// to the static token. This matches the existing real-vs-gitea split that keys
// on GITHUB_SERVER_URL (see state_write.go).
const appTokenServerGuard = "${{ github.server_url == 'https://github.com' }}"

// stripTokenExprWrapper returns the inner expression of a wrapped GitHub Actions
// expression, dropping the outer "${{" / "}}" so it can be inlined inside a
// larger "${{ ... }}". A value that is not wrapped is returned trimmed and
// unchanged, which keeps the fallback resilient to any future ref shape.
func stripTokenExprWrapper(expr string) string {
	trimmed := strings.TrimSpace(expr)
	if strings.HasPrefix(trimmed, "${{") && strings.HasSuffix(trimmed, "}}") {
		inner := trimmed[len("${{") : len(trimmed)-len("}}")]
		return strings.TrimSpace(inner)
	}
	return trimmed
}

// resolveReleaseTokenRef returns the release-token expression a consuming step
// must use. With no release App identity it returns today's value verbatim, so
// OFF-state output is byte-identical. With an App identity it returns a fallback
// expression that prefers the minted token on real GitHub and the static token
// on act/gitea.
func resolveReleaseTokenRef(cfg *config.TrunkConfig) string {
	static := cfg.GetReleaseToken()
	if !cfg.HasReleaseTokenApp() {
		return static
	}
	return mintedTokenFallbackRef(releaseAppTokenStepID, static)
}

// resolveStateTokenRef mirrors resolveReleaseTokenRef for the state-token seam.
func resolveStateTokenRef(cfg *config.TrunkConfig) string {
	static := cfg.GetStateToken()
	if !cfg.HasStateTokenApp() {
		return static
	}
	return mintedTokenFallbackRef(stateAppTokenStepID, static)
}

// mintedTokenFallbackRef builds "${{ steps.<id>.outputs.token || <static> }}",
// inlining the static token's inner expression so the wrapped form stays valid.
func mintedTokenFallbackRef(stepID, staticExpr string) string {
	return "${{ steps." + stepID + ".outputs.token || " + stripTokenExprWrapper(staticExpr) + " }}"
}

// tokenSeam identifies which App-token mint step(s) a job consumes.
type tokenSeam int

const (
	// seamRelease marks a job that consumes the release token.
	seamRelease tokenSeam = iota
	// seamState marks a job that consumes the state token.
	seamState
)

// writeMintSteps writes the App-token minting step(s) for the seams a job
// consumes, at the given indent (spaces before "- name:"). It is a no-op for any
// seam without a configured App identity, so OFF-state output is byte-identical:
// nothing, not even a blank line, is emitted. Call it immediately after a job's
// "steps:" line. Pass each seam the job consumes; a job consuming both tokens
// passes both and gets both mint steps (the ids are distinct).
func writeMintSteps(sb *strings.Builder, cfg *config.TrunkConfig, indent string, seams ...tokenSeam) {
	for _, seam := range seams {
		switch seam {
		case seamRelease:
			if cfg.HasReleaseTokenApp() {
				writeMintStep(sb, cfg, indent,
					"Mint release app token", releaseAppTokenStepID,
					cfg.GetReleaseTokenAppID(), cfg.GetReleaseTokenAppPrivateKey())
			}
		case seamState:
			if cfg.HasStateTokenApp() {
				writeMintStep(sb, cfg, indent,
					"Mint state app token", stateAppTokenStepID,
					cfg.GetStateTokenAppID(), cfg.GetStateTokenAppPrivateKey())
			}
		}
	}
}

// writeMintStep writes a single actions/create-github-app-token step. The
// with-indent is the step indent plus two spaces; the key indent under with: is
// four spaces deeper, matching the surrounding generated YAML.
func writeMintStep(sb *strings.Builder, cfg *config.TrunkConfig, indent, name, id, appID, privateKey string) {
	body := indent + "  "
	with := indent + "    "
	sb.WriteString(indent + "- name: " + name + "\n")
	sb.WriteString(body + "id: " + id + "\n")
	sb.WriteString(body + "if: " + appTokenServerGuard + "\n")
	sb.WriteString(body + "uses: " + actionRef(cfg, actionCreateAppToken) + "\n")
	sb.WriteString(body + "with:\n")
	sb.WriteString(with + "app-id: " + appID + "\n")
	sb.WriteString(with + "private-key: " + privateKey + "\n")
}

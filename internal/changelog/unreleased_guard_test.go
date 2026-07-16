package changelog_test

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/changelog"
	"github.com/stablekernel/cascade/internal/git"
)

// unreleasedStableTagPattern matches an immutable stable release tag
// (vX.Y.Z with no prerelease suffix). The freshness guard measures the
// unreleased window from the newest of these.
var unreleasedStableTagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// unreleasedBulletPattern matches a top-level entry bullet inside the
// [Unreleased] section, capturing the bold scope label when present
// (for example "- **promote:** ...").
var unreleasedBulletPattern = regexp.MustCompile(`^- (?:\*\*([^*]+):\*\*)?`)

// TestChangelog_UnreleasedCoversUserFacingCommits guards CHANGELOG.md
// [Unreleased] against lagging the commit history. The changelog stated
// policy is that entries are derived from the conventional-commit history,
// and the lag has recurred repeatedly: user-facing fixes merge to main and
// [Unreleased] stays silent until an audit catches it.
//
// The guard enumerates commits since the newest stable release tag,
// classifies them with the same conventional-commit parser the release
// notes use (changelog.ParseCommit), and treats feat, fix, perf, and any
// breaking commit as user-facing, mirroring the sections CategorizeCommits
// surfaces in release notes. It then requires:
//
//  1. [Unreleased] has at least one entry bullet whenever at least one
//     user-facing commit exists since the last stable tag.
//  2. Every scope token named by a user-facing commit (fix(promote),
//     fix(config,simulate), ...) appears as a scope label on some
//     [Unreleased] bullet. Bullet labels split on "/" and "," so a
//     combined "**config/simulate:**" bullet covers both tokens.
//
// Heuristic boundary, for future maintainers: hand-written bullets carry
// no commit hash or PR number, so this guard proves presence and scope
// representation, not prose accuracy or one-to-one coverage. A bullet
// whose scope matches an earlier unreleased commit also satisfies a later
// commit with the same scope (two fix(config) commits may legitimately
// share one bullet, so that masking is accepted). Unscoped user-facing
// commits ("fix: ...") only strengthen the non-empty requirement, since
// there is no token to match. What the guard does guarantee is exactly
// the failure mode that kept recurring: a user-facing commit whose scope
// has no [Unreleased] representation at all fails the suite.
//
// The test skips on shallow or tagless checkouts, like
// TestDefaultCLIVersion_MatchesLatestReleaseTag: full local clones and CI
// jobs whose checkout fetches tags and full history enforce it.
func TestChangelog_UnreleasedCoversUserFacingCommits(t *testing.T) {
	if out, err := exec.Command("git", "rev-parse", "--is-shallow-repository").Output(); err != nil {
		t.Skipf("git unavailable (%v); cannot inspect commit history", err)
	} else if strings.TrimSpace(string(out)) == "true" {
		t.Skip("shallow checkout; history since the last release tag is incomplete")
	}

	latest := latestStableTag(t)
	if latest == "" {
		t.Skip("no stable release tags visible; cannot bound the unreleased window")
	}

	commits, err := git.GetCommits(latest, "HEAD", nil)
	if err != nil {
		t.Skipf("cannot enumerate commits since %s (%v)", latest, err)
	}

	var userFacing []changelog.ConventionalCommit
	for _, c := range commits {
		cc := changelog.ParseCommit(c)
		if cc == nil {
			continue
		}
		switch {
		case cc.Breaking, cc.Type == "feat", cc.Type == "fix", cc.Type == "perf":
			userFacing = append(userFacing, *cc)
		}
	}
	if len(userFacing) == 0 {
		return // nothing user-facing since the last release; [Unreleased] may be empty
	}

	bullets, scopeTokens := parseUnreleasedSection(t)
	if len(bullets) == 0 {
		t.Fatalf("%d user-facing commit(s) since %s but CHANGELOG.md [Unreleased] has no entries; add an entry for each user-facing change (see CONTRIBUTING.md)", len(userFacing), latest)
	}

	for _, cc := range userFacing {
		if cc.Scope == "" {
			continue
		}
		for _, token := range strings.Split(cc.Scope, ",") {
			token = strings.ToLower(strings.TrimSpace(token))
			if token == "" {
				continue
			}
			if !scopeTokens[token] {
				t.Errorf("commit %s (%s(%s): %s) is user-facing but no CHANGELOG.md [Unreleased] bullet carries the **%s:** scope; add an entry (see CONTRIBUTING.md)",
					cc.Hash, cc.Type, cc.Scope, cc.Description, token)
			}
		}
	}
}

// latestStableTag returns the highest vX.Y.Z tag visible in the checkout,
// or "" when none is.
func latestStableTag(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("git", "tag", "--list", "v*").Output()
	if err != nil {
		t.Skipf("git tags unavailable (%v); cannot derive the latest release tag", err)
	}

	latest := ""
	var latestParts [3]int
	for _, tag := range strings.Fields(string(out)) {
		m := unreleasedStableTagPattern.FindStringSubmatch(tag)
		if m == nil {
			continue // prerelease (rc/dryrun) or foreign tag
		}
		var parts [3]int
		for i := 0; i < 3; i++ {
			parts[i], _ = strconv.Atoi(m[i+1])
		}
		if latest == "" || parts[0] > latestParts[0] ||
			(parts[0] == latestParts[0] && (parts[1] > latestParts[1] ||
				(parts[1] == latestParts[1] && parts[2] > latestParts[2]))) {
			latest, latestParts = tag, parts
		}
	}
	return latest
}

// parseUnreleasedSection reads CHANGELOG.md and returns the entry bullets
// under [Unreleased] plus the set of scope tokens their bold labels name,
// lowercased and split on "/" and ",".
func parseUnreleasedSection(t *testing.T) ([]string, map[string]bool) {
	t.Helper()

	data, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatalf("reading CHANGELOG.md: %v", err)
	}

	var bullets []string
	scopeTokens := make(map[string]bool)
	inUnreleased := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "## [Unreleased]"):
			inUnreleased = true
			continue
		case strings.HasPrefix(line, "## "):
			inUnreleased = false
			continue
		}
		if !inUnreleased {
			continue
		}
		m := unreleasedBulletPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		bullets = append(bullets, line)
		for _, token := range strings.FieldsFunc(strings.ToLower(m[1]), func(r rune) bool {
			return r == '/' || r == ','
		}) {
			if token = strings.TrimSpace(token); token != "" {
				scopeTokens[token] = true
			}
		}
	}
	return bullets, scopeTokens
}

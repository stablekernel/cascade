package release

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// maxReleaseBodyChars is the maximum length GitHub's Releases API accepts for a
// release body. Exceeding it fails the create/update with a 422 "body is too
// long (maximum is 125000 characters)" rather than truncating server-side, so
// the body must be capped before it is sent.
//
// GitHub counts characters, not bytes, so the cap is measured in runes.
const maxReleaseBodyChars = 125000

// truncationNotice is the human-readable explanation embedded in a body that had
// to be shortened. It is a stable substring so the marker is greppable.
const truncationNotice = "Release notes truncated: this changelog exceeded GitHub's release body limit of 125,000 characters."

// webBaseURL returns the browser-facing base URL of the GitHub host. Actions
// always sets GITHUB_SERVER_URL, which is correct for github.com and for a
// GitHub Enterprise host alike; the constant is the fallback for a local run.
func webBaseURL() string {
	if serverURL := os.Getenv("GITHUB_SERVER_URL"); serverURL != "" {
		return strings.TrimSuffix(serverURL, "/")
	}
	return "https://github.com"
}

// changesLink builds a link to the changes the truncated body omits. With a
// previous tag it is a compare link bounded to this release's range; without
// one (the case that produces an oversized body in the first place, since an
// empty previous tag makes the changelog span the repository's whole history)
// it is the commit history for the tag.
func changesLink(repo, previousTag, tag string) string {
	if repo == "" || tag == "" {
		return ""
	}
	if previousTag != "" {
		return fmt.Sprintf("%s/%s/compare/%s...%s", webBaseURL(), repo, previousTag, tag)
	}
	return fmt.Sprintf("%s/%s/commits/%s", webBaseURL(), repo, tag)
}

// truncationMarker renders the footer appended to a truncated body: what
// happened, plus where to read the changes that were dropped.
func truncationMarker(repo, previousTag, tag string) string {
	if link := changesLink(repo, previousTag, tag); link != "" {
		return fmt.Sprintf("\n\n---\n\n_%s See the [full list of changes](%s) for the entries omitted here._\n",
			truncationNotice, link)
	}
	return fmt.Sprintf("\n\n---\n\n_%s_\n", truncationNotice)
}

// truncateRunes cuts s to at most maxRunes characters on a rune boundary, so a
// multi-byte character is never split into invalid UTF-8.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	count := 0
	// Ranging a string yields the byte offset of each rune's first byte, so
	// slicing at one of those offsets always lands on a rune boundary.
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

// keepWholeLines returns the longest run of leading whole lines of body that
// fits in budget characters. Truncating on a line boundary keeps every retained
// changelog entry intact instead of severing one mid-sentence. If not even the
// first line fits, that line is cut on a rune boundary as a last resort.
func keepWholeLines(body string, budget int) string {
	if budget <= 0 {
		return ""
	}

	var kept strings.Builder
	used := 0
	for _, line := range strings.SplitAfter(body, "\n") {
		if line == "" {
			continue
		}
		n := utf8.RuneCountInString(line)
		if used+n > budget {
			break
		}
		kept.WriteString(line)
		used += n
	}

	if kept.Len() == 0 {
		return truncateRunes(body, budget)
	}
	return strings.TrimRight(kept.String(), "\n")
}

// capReleaseBody shortens body so the value sent to GitHub stays within the
// Releases API limit. A body already within the cap is returned untouched.
//
// An oversized body is cut on a whole-line boundary and gets a marker naming the
// truncation and linking to the omitted changes, so nothing is lost silently.
// The line budget reserves room for that marker, leaving the final body under
// the cap rather than exactly at it.
func capReleaseBody(body, repo, previousTag, tag string) string {
	if utf8.RuneCountInString(body) <= maxReleaseBodyChars {
		return body
	}

	marker := truncationMarker(repo, previousTag, tag)
	budget := maxReleaseBodyChars - utf8.RuneCountInString(marker)
	if budget <= 0 {
		// Pathological: the marker alone fills the cap. Keep the marker's
		// meaning over the notes and stay within the limit regardless.
		return truncateRunes(marker, maxReleaseBodyChars)
	}

	return keepWholeLines(body, budget) + marker
}

// capBody applies the Releases API body cap using this manager's repository and
// the caller's tag context, so a truncated body links to the right compare view.
func (m *Manager) capBody(body, previousTag, tag string) string {
	return capReleaseBody(body, m.repo, previousTag, tag)
}

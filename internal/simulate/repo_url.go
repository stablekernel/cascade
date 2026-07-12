package simulate

import (
	"os"
	"os/exec"
	"strings"
)

// resolveRepoURL determines the repository browse URL used to build the compare
// links in the human simulation output. It prefers the GitHub Actions
// environment (GITHUB_REPOSITORY, with GITHUB_SERVER_URL defaulting to
// https://github.com) and falls back to the origin remote of the working-dir
// git repository. It returns "" when neither source identifies a repository, so
// the caller omits the compare line rather than emit a broken URL.
func resolveRepoURL() string {
	if repo := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY")); repo != "" {
		server := strings.TrimSpace(os.Getenv("GITHUB_SERVER_URL"))
		if server == "" {
			server = "https://github.com"
		}
		return strings.TrimRight(server, "/") + "/" + repo
	}

	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return normalizeGitRemote(strings.TrimSpace(string(out)))
}

// normalizeGitRemote converts a git remote URL (ssh or https) into a browse URL
// with any trailing ".git" removed. It returns "" for a remote it cannot map.
func normalizeGitRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimSuffix(raw, ".git")

	switch {
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "http://"):
		return raw
	case strings.HasPrefix(raw, "ssh://"):
		rest := strings.TrimPrefix(raw, "ssh://")
		if at := strings.Index(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		host, path, ok := strings.Cut(rest, "/")
		if !ok || host == "" || path == "" {
			return ""
		}
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
		return "https://" + host + "/" + path
	case strings.HasPrefix(raw, "git@"):
		rest := strings.TrimPrefix(raw, "git@")
		host, path, ok := strings.Cut(rest, ":")
		if !ok || host == "" || path == "" {
			return ""
		}
		return "https://" + host + "/" + path
	default:
		return ""
	}
}

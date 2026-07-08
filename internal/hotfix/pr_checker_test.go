package hotfix

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// prTestEntry is the JSON shape the httptest server returns for each PR entry,
// mirroring the subset of GitHub/Gitea pull-request response the checker reads.
type prTestEntry struct {
	Number  int            `json:"number"`
	HTMLURL string         `json:"html_url"`
	Base    prTestBase     `json:"base"`
	Labels  []prTestLabel  `json:"labels"`
}

type prTestBase struct {
	Ref string `json:"ref"`
}

type prTestLabel struct {
	Name string `json:"name"`
}

// newPRServer creates an httptest server that returns the given PRs on every
// GET /repos/{owner}/{repo}/pulls request. It does not implement pagination
// unless paginated is set; see newPaginatedPRServer for the two-page variant.
func newPRServer(t *testing.T, prs []prTestEntry) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(prs); err != nil {
			t.Errorf("encoding PR list: %v", err)
		}
	}))
}

// checker returns a restPRChecker pointed at the given httptest server.
func checker(t *testing.T, ts *httptest.Server, repo string) *restPRChecker {
	t.Helper()
	return &restPRChecker{repo: repo, apiBase: ts.URL, token: "test-token"}
}

// (1) A PR that matches the base branch and carries the cascade-hotfix label is returned.
func TestRestPRChecker_MatchingPRReturned(t *testing.T) {
	prs := []prTestEntry{
		{
			Number:  42,
			HTMLURL: "https://example.test/pulls/42",
			Base:    prTestBase{Ref: "env/test"},
			Labels:  []prTestLabel{{Name: hotfixPRLabel}},
		},
	}
	ts := newPRServer(t, prs)
	defer ts.Close()

	c := checker(t, ts, "owner/repo")
	got, err := c.OpenHotfixPRs("env/test")
	if err != nil {
		t.Fatalf("OpenHotfixPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(got))
	}
	if got[0].Number != 42 || got[0].URL != "https://example.test/pulls/42" {
		t.Errorf("got %+v, want {42 https://example.test/pulls/42}", got[0])
	}
}

// (2) A PR whose base branch does not match the requested branch is filtered out.
func TestRestPRChecker_WrongBaseBranchFiltered(t *testing.T) {
	prs := []prTestEntry{
		{
			Number:  7,
			HTMLURL: "https://example.test/pulls/7",
			Base:    prTestBase{Ref: "env/prod"},
			Labels:  []prTestLabel{{Name: hotfixPRLabel}},
		},
	}
	ts := newPRServer(t, prs)
	defer ts.Close()

	c := checker(t, ts, "owner/repo")
	got, err := c.OpenHotfixPRs("env/test")
	if err != nil {
		t.Fatalf("OpenHotfixPRs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 PRs for a wrong-base PR, got %d: %+v", len(got), got)
	}
}

// (3) A PR that targets the right base but lacks the cascade-hotfix label is filtered out.
func TestRestPRChecker_MissingLabelFiltered(t *testing.T) {
	prs := []prTestEntry{
		{
			Number:  11,
			HTMLURL: "https://example.test/pulls/11",
			Base:    prTestBase{Ref: "env/test"},
			Labels:  []prTestLabel{{Name: "some-other-label"}},
		},
	}
	ts := newPRServer(t, prs)
	defer ts.Close()

	c := checker(t, ts, "owner/repo")
	got, err := c.OpenHotfixPRs("env/test")
	if err != nil {
		t.Fatalf("OpenHotfixPRs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 PRs for a non-hotfix PR, got %d: %+v", len(got), got)
	}
}

// (4) When the response spans two pages the checker follows the Link rel=next header
// and finds a match on the second page.
func TestRestPRChecker_PaginatedMatchOnPageTwo(t *testing.T) {
	page1 := []prTestEntry{
		{
			Number:  1,
			HTMLURL: "https://example.test/pulls/1",
			Base:    prTestBase{Ref: "env/prod"},
			Labels:  []prTestLabel{{Name: hotfixPRLabel}},
		},
	}
	page2 := []prTestEntry{
		{
			Number:  99,
			HTMLURL: "https://example.test/pulls/99",
			Base:    prTestBase{Ref: "env/test"},
			Labels:  []prTestLabel{{Name: hotfixPRLabel}},
		},
	}

	var page2URL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.RawQuery, "page=2") {
			// Page 2: no next link.
			if err := json.NewEncoder(w).Encode(page2); err != nil {
				t.Errorf("encoding page 2: %v", err)
			}
			return
		}
		// Page 1: add Link header pointing at page 2.
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, page2URL))
		if err := json.NewEncoder(w).Encode(page1); err != nil {
			t.Errorf("encoding page 1: %v", err)
		}
	}))
	defer ts.Close()

	page2URL = ts.URL + "/repos/owner/repo/pulls?state=open&per_page=100&page=2"
	c := &restPRChecker{repo: "owner/repo", apiBase: ts.URL, token: "test-token"}
	got, err := c.OpenHotfixPRs("env/test")
	if err != nil {
		t.Fatalf("OpenHotfixPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 PR from page 2, got %d", len(got))
	}
	if got[0].Number != 99 {
		t.Errorf("got PR %d, want 99", got[0].Number)
	}
}

// (5) A non-200 response causes an error so the gate fails closed.
func TestRestPRChecker_NonOKResponseReturnsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer ts.Close()

	c := checker(t, ts, "owner/repo")
	_, err := c.OpenHotfixPRs("env/test")
	if err == nil {
		t.Fatal("expected an error for a non-200 response, got nil")
		return
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q should reference the HTTP status code", err.Error())
	}
}

// (6) A PR carrying only the cascade-hotfix-conflict label is returned. Conflict
// resolution PRs represent active human work on env/<branch>; the single-flight
// gate must catch them to prevent ResetBranch from destroying that work.
func TestRestPRChecker_ConflictLabelPRReturned(t *testing.T) {
	prs := []prTestEntry{
		{
			Number:  13,
			HTMLURL: "https://example.test/pulls/13",
			Base:    prTestBase{Ref: "env/test"},
			Labels:  []prTestLabel{{Name: hotfixConflictPRLabel}},
		},
	}
	ts := newPRServer(t, prs)
	defer ts.Close()

	c := checker(t, ts, "owner/repo")
	got, err := c.OpenHotfixPRs("env/test")
	if err != nil {
		t.Fatalf("OpenHotfixPRs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected cascade-hotfix-conflict PR to be returned, got %d PRs", len(got))
	}
	if got[0].Number != 13 {
		t.Errorf("got PR %d, want 13", got[0].Number)
	}
}

// nextPageURL unit tests.

func TestNextPageURL_ExtractsNext(t *testing.T) {
	link := `<https://api.github.com/repos/o/r/pulls?page=2>; rel="next", <https://api.github.com/repos/o/r/pulls?page=5>; rel="last"`
	got := nextPageURL(link)
	if got != "https://api.github.com/repos/o/r/pulls?page=2" {
		t.Errorf("nextPageURL(%q) = %q, want the next URL", link, got)
	}
}

func TestNextPageURL_EmptyLinkReturnsEmpty(t *testing.T) {
	if got := nextPageURL(""); got != "" {
		t.Errorf("nextPageURL(%q) = %q, want empty", "", got)
	}
}

func TestNextPageURL_NoNextReturnsEmpty(t *testing.T) {
	link := `<https://api.github.com/repos/o/r/pulls?page=1>; rel="first"`
	if got := nextPageURL(link); got != "" {
		t.Errorf("nextPageURL(%q) = %q, want empty (no next)", link, got)
	}
}

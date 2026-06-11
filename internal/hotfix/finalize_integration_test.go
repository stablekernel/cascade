package hotfix

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stablekernel/cascade/internal/release"
)

// TestFinalize_Integration_PlanThenMergeThenFinalize walks the full hotfix
// lifecycle against a real temporary git repository and a release-stub server:
// plan reconciles env/<target> at the recorded base, a manual cherry-pick plus
// merge advances the branch tip, and finalize writes the diverged manifest,
// allocates the hotfix version, and drives the release API. This is the
// committed scratch-repo plus release-stub coverage that complements the focused
// unit tests; full act/gitea e2e for the hotfix flow (plan, apply, finalize)
// lands in the e2e harness unit per the implementation plan.
func TestFinalize_Integration_PlanThenMergeThenFinalize(t *testing.T) {
	newScratchRepo(t)
	base := commitFile(t, "a.txt", "one", "first")
	fix := commitFile(t, "fix.txt", "patched", "fix on trunk")

	manifest := writeFinalizeManifest(t, []string{"dev", "test", "prod"}, map[string]envFixture{
		"dev":  {sha: fix, version: "v1.4.0-rc.2"},
		"test": {sha: base, version: "v1.4.0-rc.2"},
		"prod": {sha: base, version: "v1.4.0-rc.2"},
	})

	// Step 1: plan reconciles env/test at the recorded base SHA.
	planner := newPlanner(t, manifest)
	planRes, err := planner.Plan(fix, "test")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if planRes.NoOp {
		t.Fatal("expected a real hotfix, got no-op")
	}
	if got := gitOut(t, "rev-parse", "env/test"); got != base {
		t.Fatalf("env/test tip = %q, want recorded base %q", got, base)
	}

	// Step 2: simulate the resolution: cherry-pick the fix onto env/test and
	// "merge" it (the branch tip is what the workflow would record as merge SHA).
	runGit(t, "checkout", "env/test")
	runGit(t, "cherry-pick", "-x", fix)
	mergeSHA := gitOut(t, "rev-parse", "env/test")
	runGit(t, "checkout", "main")

	// Step 3: finalize against a release-stub server.
	var createBody string
	var sawCreate bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases"):
			// cleanupStaleDrafts list: no drafts.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]release.GitHubRelease{})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			// createGitTag.
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/releases"):
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if b, ok := payload["body"].(string); ok {
				createBody = b
			}
			sawCreate = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(release.GitHubRelease{ID: 1})
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer server.Close()

	mgr := release.NewManagerWithURL("owner/repo", "token", server.URL)

	f := newFinalizer(t, manifest,
		WithReleaseManager(mgr),
		WithTagLister(stubTagLister{}),
		WithStatePusher(&recordingPusher{}),
	)
	f.SetDeployResult("api", "success")
	f.SetBuildResult("api", "success")

	if err := f.Finalize("test", mergeSHA, fix, base); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// Manifest now records the diverged state.
	st := loadState(t, manifest, "test")
	if st.SHA != mergeSHA {
		t.Errorf("state.sha = %q, want merge SHA %q", st.SHA, mergeSHA)
	}
	if st.Version != "v1.4.0-rc.2.hotfix.1" {
		t.Errorf("state.version = %q, want v1.4.0-rc.2.hotfix.1", st.Version)
	}
	if st.Ref != "env/test" || st.BaseSHA != base || len(st.Patches) != 1 {
		t.Errorf("divergence fields wrong: ref=%q base=%q patches=%v", st.Ref, st.BaseSHA, st.Patches)
	}
	if st.Deploys["api"] == nil || st.Deploys["api"].SHA != mergeSHA {
		t.Errorf("deploy substate not recorded for api: %+v", st.Deploys)
	}
	if st.Builds["api"] == nil || st.Builds["api"].SHA != mergeSHA {
		t.Errorf("build substate not recorded for api: %+v", st.Builds)
	}

	// The release object was created with the hotfix tag and a base-version body.
	if !sawCreate {
		t.Fatal("expected a release create call")
	}
	if !strings.Contains(createBody, "v1.4.0-rc.2") || !strings.Contains(createBody, short(fix)) {
		t.Errorf("release body missing base version or carried commit: %q", createBody)
	}
}

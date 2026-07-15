package release

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stablekernel/cascade/internal/globals"
)

// TestManager_DryRunGuardRefusesMutation simulates a future command that
// forgets its explicit --dry-run gate and drives the release Manager anyway.
// The backstop in newRequest must refuse the mutating request with a loud
// error before it leaves the process: the recording server may see reads but
// never a POST, PATCH, PUT, or DELETE.
func TestManager_DryRunGuardRefusesMutation(t *testing.T) {
	globals.SetDryRun(true)
	t.Cleanup(func() { globals.SetDryRun(false) })

	var mu sync.Mutex
	var mutations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mu.Lock()
			mutations = append(mutations, r.Method+" "+r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.Contains(r.URL.Path, "/releases/tags/") {
			_, _ = w.Write([]byte(`{"id":1,"tag_name":"v1.2.3","draft":true,"url":"u","html_url":"h"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	m := NewManagerWithURL("owner/repo", "test-token", srv.URL)
	_, err := m.Manage(Options{Action: ActionDelete, Environment: "prod", Tag: "v1.2.3"})
	if err == nil {
		t.Fatal("an ungated delete under dry-run must be refused by the guard, got nil error")
	}
	if !strings.Contains(err.Error(), "dry-run guard") {
		t.Errorf("expected the loud dry-run guard error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(mutations) > 0 {
		t.Fatalf("guard must refuse before the request is sent, but mutations reached the API: %v", mutations)
	}
}

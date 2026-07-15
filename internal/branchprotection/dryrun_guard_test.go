package branchprotection

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stablekernel/cascade/internal/globals"
)

// TestApplier_DryRunGuardRefusesPUT simulates a caller that reaches the
// protection PUT without an explicit --dry-run gate. The backstop in the
// applier must refuse loudly and the request must never reach the server.
func TestApplier_DryRunGuardRefusesPUT(t *testing.T) {
	globals.SetDryRun(true)
	t.Cleanup(func() { globals.SetDryRun(false) })

	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	err := newApplier(srv.URL, "test-token").apply(context.Background(), "owner/repo", "main", Protection{})
	if err == nil {
		t.Fatal("an ungated protection PUT under dry-run must be refused by the guard, got nil error")
	}
	if !strings.Contains(err.Error(), "dry-run guard") {
		t.Errorf("expected the loud dry-run guard error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) > 0 {
		t.Fatalf("guard must refuse before the PUT is sent, but requests reached the API: %v", requests)
	}
}

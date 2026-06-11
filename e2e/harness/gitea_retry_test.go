package harness

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransientResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "405 try again later is transient",
			status: http.StatusMethodNotAllowed,
			body:   `{"message":"Please try again later"}`,
			want:   true,
		},
		{
			name:   "405 try again later case-insensitive",
			status: http.StatusMethodNotAllowed,
			body:   `{"message":"please TRY AGAIN LATER"}`,
			want:   true,
		},
		{
			name:   "405 method not allowed without throttle body is a real client error",
			status: http.StatusMethodNotAllowed,
			body:   `{"message":"merge is not allowed"}`,
			want:   false,
		},
		{
			name:   "500 server error is transient",
			status: http.StatusInternalServerError,
			body:   "boom",
			want:   true,
		},
		{
			name:   "503 service unavailable is transient",
			status: http.StatusServiceUnavailable,
			body:   "",
			want:   true,
		},
		{
			name:   "404 not found is a real client error",
			status: http.StatusNotFound,
			body:   `{"message":"not found"}`,
			want:   false,
		},
		{
			name:   "409 conflict is a real client error",
			status: http.StatusConflict,
			body:   `{"message":"conflict"}`,
			want:   false,
		},
		{
			name:   "200 ok is not transient",
			status: http.StatusOK,
			body:   "",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, transientResponse(tt.status, tt.body))
		})
	}
}

func TestDoRetry_RetriesTransientThenSucceeds(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Body must be replayable across attempts: assert it is intact each call.
		got, _ := io.ReadAll(r.Body)
		assert.Equal(t, "payload", string(got))

		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"message":"Please try again later"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := doRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("payload"))
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "ok", string(body))
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
}

func TestDoRetry_DoesNotRetryRealClientError(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"conflict"}`))
	}))
	defer srv.Close()

	resp, err := doRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, srv.URL, nil)
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// 409 is surfaced to the caller without retry so expect-failure assertions
	// stay deterministic.
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))

	// The caller still sees the full body after classification rewinds it.
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "conflict")
}

func TestDoRetry_ExhaustsAttemptsAndReturnsLastResponse(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"message":"Please try again later"}`))
	}))
	defer srv.Close()

	resp, err := doRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, srv.URL, nil)
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// After the attempt budget is spent the final transient response is returned
	// so the caller's own status check produces a clear error.
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, int32(giteaRetryAttempts), atomic.LoadInt32(&calls))
}

func TestDoRetry_HonoursContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := doRetry(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
	})
	require.Error(t, err)
}

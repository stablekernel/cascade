package harness

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// giteaRetryAttempts bounds how many times a throttled gitea REST call is
// retried before the error is surfaced.
const giteaRetryAttempts = 5

// giteaRetryBackoff is the base delay between retry attempts. Attempt n waits
// giteaRetryBackoff*n, giving a short linear backoff that clears gitea's
// transient "try again later" throttle without materially slowing the suite.
const giteaRetryBackoff = 250 * time.Millisecond

// transientResponse reports whether an HTTP response from gitea represents a
// transient, safe-to-retry condition rather than a real client error.
//
// Under container and gitea load the suite observes two throttling shapes:
//   - 405 Method Not Allowed with a body of {"message":"Please try again
//     later"} (gitea's mergeability/lock throttle), and
//   - 5xx server errors while gitea is briefly overwhelmed.
//
// A 405 that is NOT the "try again later" throttle is a genuine client error
// (wrong method / disallowed operation) and must not be retried, so the body is
// inspected. Other 4xx codes are real client errors and are never retried,
// which keeps legitimate expect-failure assertions deterministic.
func transientResponse(status int, body string) bool {
	if status == http.StatusMethodNotAllowed {
		return strings.Contains(strings.ToLower(body), "try again later")
	}
	return status >= 500 && status <= 599
}

// doRetry issues the request built by newReq, retrying on transient gitea
// throttling responses and transient transport errors. newReq must return a
// fresh *http.Request on every call so the request body can be replayed safely.
//
// Retries are bounded and apply ONLY to idempotent-by-effect throttling cases:
// a transient response is one where gitea explicitly asked the caller to try
// again (or returned 5xx) BEFORE applying any state change, so re-issuing the
// request does not double-apply a mutation. Real 4xx client errors and a
// successful response are returned to the caller immediately.
//
// On success the caller owns the returned response body and must close it. On a
// transient response the body is drained and closed between attempts.
func doRetry(ctx context.Context, newReq func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= giteaRetryAttempts; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			// Transport-level errors (connection reset, refused) under load are
			// transient; retry within the attempt budget.
			lastErr = err
			if !sleepBeforeRetry(ctx, attempt) {
				return nil, ctx.Err()
			}
			continue
		}

		if attempt < giteaRetryAttempts && transientResponseBody(resp) {
			// Drain and close so the connection can be reused, then back off.
			drained, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("gitea transient response: %s - %s", resp.Status, strings.TrimSpace(string(drained)))
			if !sleepBeforeRetry(ctx, attempt) {
				return nil, ctx.Err()
			}
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("gitea request failed after %d attempts: %w", giteaRetryAttempts, lastErr)
}

// transientResponseBody peeks the response body to classify a transient gitea
// throttle, then rewinds it so the caller still sees the full body. Only used
// when a retry is still possible.
func transientResponseBody(resp *http.Response) bool {
	if resp.StatusCode != http.StatusMethodNotAllowed && (resp.StatusCode < 500 || resp.StatusCode > 599) {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		// Body was consumed; rebuild an empty one so callers do not panic.
		resp.Body = io.NopCloser(strings.NewReader(""))
		return false
	}
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	return transientResponse(resp.StatusCode, string(body))
}

// sleepBeforeRetry waits the linear backoff for the given attempt, returning
// false if the context is cancelled first.
func sleepBeforeRetry(ctx context.Context, attempt int) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Duration(attempt) * giteaRetryBackoff):
		return true
	}
}

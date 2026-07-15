package statewrite

import (
	"errors"
	"testing"
)

func TestClassifyPutError_BranchRefCAS409(t *testing.T) {
	// A branch-ref compare-and-swap 409 (the shape two racing finalizes produce)
	// carries an "is at X but expected Y" marker and NO "does not match"
	// substring. It must still classify as a typed ConflictError so the retry
	// loop re-fetches and re-applies instead of hard-failing.
	got := classifyPutError(branchRefCAS409Body(), errors.New("exit 1"))
	if got == nil {
		t.Fatal("classifyPutError() = nil, want a *ConflictError for a branch-ref compare-and-swap 409")
	}
	var ce *ConflictError
	if !errors.As(got, &ce) {
		t.Fatalf("classifyPutError() = %T, want it to unwrap to *ConflictError", got)
	}
	if !IsConflict(got) {
		t.Error("IsConflict() = false, want true for a branch-ref compare-and-swap 409")
	}
}

func TestClassifyPutError_NonConflictNotClassified(t *testing.T) {
	// A genuine non-conflict failure with no 409/Conflict marker must not be
	// classified as a conflict, or the retry loop would spin on unrecoverable
	// errors.
	got := classifyPutError("HTTP 500 Internal Server Error", errors.New("exit 1"))
	if got == nil {
		t.Fatal("classifyPutError() = nil, want a wrapped non-nil error")
	}
	if IsConflict(got) {
		t.Error("IsConflict() = true, want false for a 500 with no conflict marker")
	}
}

// TestClassifyPutError_TransientVsPermanent pins the shared write-path
// taxonomy: rate limits (HTTP 429, or a 403 whose body names a rate,
// secondary, or abuse limit), HTTP 5xx, and transport failures carrying no
// HTTP status are transient and must classify as retryable, while 401
// (revoked token), 403 authorization, 404, and 422 are permanent and must
// not. GitHub's secondary rate limits surface as 403/429 with a rate-limit
// message, not as auth failures, so the message decides where a 403 lands.
func TestClassifyPutError_TransientVsPermanent(t *testing.T) {
	transient := map[string]string{
		"429 too many requests":        `gh: API rate limit exceeded for installation ID 123. (HTTP 429)`,
		"403 secondary rate limit":     `gh: You have exceeded a secondary rate limit. Please wait a few minutes before you try again. (HTTP 403)`,
		"403 abuse detection":          `gh: You have triggered an abuse detection mechanism. (HTTP 403)`,
		"retry-after hint":             `gh: Please retry your request again later. Retry-After: 60 (HTTP 403)`,
		"5xx server error":             `gh: Server Error (HTTP 502)`,
		"transport without status":     `error connecting to api.github.com: dial tcp: connection reset by peer`,
		"bare 409 without lock marker": `gh: Conflict (HTTP 409)`,
	}
	for name, out := range transient {
		t.Run("transient/"+name, func(t *testing.T) {
			got := classifyPutError(out, errors.New("exit 1"))
			if got == nil {
				t.Fatal("classifyPutError() = nil, want a non-nil error")
			}
			if !IsTransient(got) {
				t.Errorf("IsTransient() = false, want true for %q", out)
			}
		})
	}

	permanent := map[string]string{
		"401 revoked token": `gh: Bad credentials (HTTP 401)`,
		"403 authorization": `gh: Resource not accessible by integration (HTTP 403)`,
		"404 absent":        `gh: Not Found (HTTP 404)`,
		"422 validation":    `gh: Invalid request. "sha" wasn't supplied. (HTTP 422)`,
	}
	for name, out := range permanent {
		t.Run("permanent/"+name, func(t *testing.T) {
			got := classifyPutError(out, errors.New("exit 1"))
			if got == nil {
				t.Fatal("classifyPutError() = nil, want a non-nil error")
			}
			if IsTransient(got) {
				t.Errorf("IsTransient() = true, want false for %q", out)
			}
			if IsConflict(got) {
				t.Errorf("IsConflict() = true, want false for %q", out)
			}
		})
	}
}

func TestClassifyPutError(t *testing.T) {
	tests := []struct {
		name         string
		out          string
		err          error
		wantConflict bool
		wantNil      bool
	}{
		{
			name:         "409 conflict",
			out:          `{"message":"manifest.yaml does not match abc","status":"409"}`,
			err:          errors.New("exit 1"),
			wantConflict: true,
		},
		{
			name:         "conflict word",
			out:          `manifest.yaml does not match abc: Conflict`,
			err:          errors.New("exit 1"),
			wantConflict: true,
		},
		{
			name:         "401 not conflict",
			out:          "401 Unauthorized",
			err:          errors.New("exit 1"),
			wantConflict: false,
		},
		{
			name:    "nil err",
			out:     "",
			err:     nil,
			wantNil: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyPutError(tc.out, tc.err)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("classifyPutError() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("classifyPutError() = nil, want non-nil error")
			}
			if IsConflict(got) != tc.wantConflict {
				t.Errorf("IsConflict() = %v, want %v", IsConflict(got), tc.wantConflict)
			}
		})
	}
}

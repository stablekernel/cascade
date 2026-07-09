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

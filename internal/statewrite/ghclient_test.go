package statewrite

import (
	"errors"
	"testing"
)

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

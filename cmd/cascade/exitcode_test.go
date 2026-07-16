package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/stablekernel/cascade/internal/verify"
)

// codedError is a test double for a command error that opts into a specific
// process exit code via the ExitCode() int interface main.go reads.
type codedError struct {
	code int
}

func (e *codedError) Error() string { return fmt.Sprintf("coded error %d", e.code) }

func (e *codedError) ExitCode() int { return e.code }

// TestExitCodeFor pins the documented exit-code contract for the glue that
// maps a command error to the process exit code: errors carrying an
// ExitCode() int method return that code (verify returns 1 for drift and 2
// for an operational failure), and every other error keeps cobra's default
// exit code 1.
func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "plain error defaults to 1",
			err:  errors.New("boom"),
			want: 1,
		},
		{
			name: "verify drift sentinel maps to 1",
			err:  verify.ErrDrift,
			want: 1,
		},
		{
			name: "wrapped verify drift sentinel maps to 1",
			err:  fmt.Errorf("running verify: %w", verify.ErrDrift),
			want: 1,
		},
		{
			name: "error with ExitCode 2 maps to 2",
			err:  &codedError{code: 2},
			want: 2,
		},
		{
			name: "wrapped error with ExitCode 2 maps to 2",
			err:  fmt.Errorf("running verify: %w", &codedError{code: 2}),
			want: 2,
		},
		{
			name: "error with ExitCode 3 keeps its own code",
			err:  &codedError{code: 3},
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeFor(tt.err); got != tt.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestExitCodeFor_VerifyOperationalError proves the full in-process link for
// the only distinct mapping in the documented table: a real operational
// failure from verify.Run (here, a missing manifest) carries exit code 2
// through exitCodeFor, not the generic 1.
func TestExitCodeFor_VerifyOperationalError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	err := verify.Run(verify.Options{ConfigPath: missing}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("verify.Run with a missing manifest returned nil, want operational error")
	}
	if errors.Is(err, verify.ErrDrift) {
		t.Fatalf("verify.Run with a missing manifest returned drift, want operational: %v", err)
	}
	if got := exitCodeFor(err); got != 2 {
		t.Errorf("exitCodeFor(operational verify error) = %d, want 2", got)
	}
}

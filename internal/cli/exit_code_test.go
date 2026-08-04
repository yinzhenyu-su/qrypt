package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"plain error", errors.New("boom"), ExitFailure},
		{"wrapped plain error", fmt.Errorf("outer: %w", errors.New("inner")), ExitFailure},
		{"usage error", &ExitError{Code: ExitUsage, Err: errors.New("bad flag")}, ExitUsage},
		{"wrapped usage error", fmt.Errorf("wrap: %w", &ExitError{Code: ExitUsage, Err: errors.New("bad flag")}), ExitUsage},
		{"context canceled", context.Canceled, ExitInterrupted},
		{"canceled wrapped", context.DeadlineExceeded, ExitFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != tt.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestExitErrorUnwrap(t *testing.T) {
	inner := errors.New("inner")
	xe := &ExitError{Code: ExitPartial, Err: inner}
	if !errors.Is(xe, inner) {
		t.Fatal("ExitError must unwrap to its inner error")
	}
}

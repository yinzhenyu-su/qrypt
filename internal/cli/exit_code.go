package cli

import (
	"context"
	"errors"
	"fmt"
)

// Process exit codes exposed to scripts. Keep stable: tooling depends on them.
const (
	ExitOK          = 0
	ExitFailure     = 1   // runtime/transfer error not otherwise categorised
	ExitUsage       = 2   // syntax or usage error
	ExitPartial     = 3   // operation completed but some transfers failed
	ExitInterrupted = 130 // terminated by SIGINT (128 + 2)
)

// ExitError carries an explicit process exit code for a command failure.
// The error message still formats like a normal error; only the exit code
// is different.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode maps a command error to a process exit code. nil is success (0).
// Errors explicitly tagged with ExitError keep their code; context
// cancellations (user interrupt) map to ExitInterrupted; everything else
// is ExitError.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var xe *ExitError
	if errors.As(err, &xe) {
		return xe.Code
	}
	if errors.Is(err, context.Canceled) {
		return ExitInterrupted
	}
	return ExitFailure
}

// usageErrorf tags a user-facing usage error with the usage exit code so
// scripts can distinguish "you called me wrong" from "the operation failed".
func usageErrorf(format string, args ...any) error {
	return &ExitError{Code: ExitUsage, Err: fmt.Errorf(format, args...)}
}

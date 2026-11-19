package cassette

import (
	"context"
	"errors"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Error kinds are the stable strings written to a cassette's error_kind fields.
// They name the driver-level failure a request met so replay can rebuild an
// error the read layer classifies the same way, without depending on the exact
// provider error text.
const (
	// ErrorKindRateLimit is a provider throttling error (drive.ErrRateLimit).
	ErrorKindRateLimit = "rate_limit"
	// ErrorKindAuth is an authentication failure (drive.ErrAuth).
	ErrorKindAuth = "auth"
	// ErrorKindPermission is a permission failure (drive.ErrPermission).
	ErrorKindPermission = "permission"
	// ErrorKindNotFound is a missing object (drive.ErrNotFound).
	ErrorKindNotFound = "not_found"
	// ErrorKindInvalidInput is a rejected request (drive.ErrInvalidInput).
	ErrorKindInvalidInput = "invalid_input"
	// ErrorKindUnsupported is a driver that cannot serve the range at all
	// (drive.ErrUnsupported); the read layer downgrades parallel windows on it.
	ErrorKindUnsupported = "unsupported"
	// ErrorKindConflict is an object-state conflict (drive.ErrConflict).
	ErrorKindConflict = "conflict"
	// ErrorKindHTTPStatus is an HTTP status error (drive.HTTPStatusError),
	// retryable or not depending on the recorded status code.
	ErrorKindHTTPStatus = "http_status"
	// ErrorKindNetwork is a transport failure (net.Error).
	ErrorKindNetwork = "network"
	// ErrorKindCanceled is a caller cancellation (context.Canceled).
	ErrorKindCanceled = "canceled"
	// ErrorKindDeadline is an expired deadline (context.DeadlineExceeded).
	ErrorKindDeadline = "deadline"
	// ErrorKindError is any other error; replay returns the recorded message.
	ErrorKindError = "error"
)

// classifyError maps a driver error to the cassette's stable error kind and,
// for HTTP status errors, its status code. The sentinel classification wins
// over the HTTP wrapping so a 429 that also carries drive.ErrRateLimit keeps
// both the status and the sentinel on replay; a status error with no known
// sentinel is recorded as http_status.
func classifyError(err error) (kind string, status int) {
	if err == nil {
		return "", 0
	}
	var statusErr *drive.HTTPStatusError
	if errors.As(err, &statusErr) {
		status = statusErr.Status
	}
	switch {
	case errors.Is(err, drive.ErrRateLimit):
		return ErrorKindRateLimit, status
	case errors.Is(err, drive.ErrAuth):
		return ErrorKindAuth, status
	case errors.Is(err, drive.ErrPermission):
		return ErrorKindPermission, status
	case errors.Is(err, drive.ErrNotFound):
		return ErrorKindNotFound, status
	case errors.Is(err, drive.ErrInvalidInput):
		return ErrorKindInvalidInput, status
	case errors.Is(err, drive.ErrUnsupported):
		return ErrorKindUnsupported, status
	case errors.Is(err, drive.ErrConflict):
		return ErrorKindConflict, status
	case errors.Is(err, context.Canceled):
		return ErrorKindCanceled, status
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorKindDeadline, status
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) {
		return ErrorKindNetwork, status
	}
	if status != 0 {
		return ErrorKindHTTPStatus, status
	}
	return ErrorKindError, 0
}

// sentinelForKind returns the drive sentinel a recorded kind stands for. It is
// used as the Unwrap target of a replayed error so retry classification in the
// read layer works on the same conditions as during recording.
func sentinelForKind(kind string) error {
	switch kind {
	case ErrorKindRateLimit:
		return drive.ErrRateLimit
	case ErrorKindAuth:
		return drive.ErrAuth
	case ErrorKindPermission:
		return drive.ErrPermission
	case ErrorKindNotFound:
		return drive.ErrNotFound
	case ErrorKindInvalidInput:
		return drive.ErrInvalidInput
	case ErrorKindUnsupported:
		return drive.ErrUnsupported
	case ErrorKindConflict:
		return drive.ErrConflict
	case ErrorKindCanceled:
		return context.Canceled
	case ErrorKindDeadline:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// recordedError replays an error message while keeping the recorded sentinel
// reachable through errors.Is. Keeping the recorded message (rather than
// re-wrapping the sentinel) means logs from a replayed run read like the
// original incident.
type recordedError struct {
	msg   string
	cause error
}

func (e *recordedError) Error() string { return e.msg }

func (e *recordedError) Unwrap() error { return e.cause }

// replayError rebuilds the error recorded in a cassette. A zero kind and empty
// message means there was no error. When status is set the error is wrapped in
// a drive.HTTPStatusError so retry classification sees the status code exactly
// as it did during recording.
func replayError(kind string, status int, message string) error {
	if kind == "" && message == "" {
		return nil
	}
	if message == "" {
		message = kind
	}
	var err error = &recordedError{msg: message, cause: sentinelForKind(kind)}
	if status != 0 {
		err = &drive.HTTPStatusError{Status: status, Err: err}
	}
	return err
}

// validErrorKind reports whether kind is one of the recorded error kinds or
// empty (no error).
func validErrorKind(kind string) bool {
	switch kind {
	case "", ErrorKindRateLimit, ErrorKindAuth, ErrorKindPermission, ErrorKindNotFound,
		ErrorKindInvalidInput, ErrorKindUnsupported, ErrorKindConflict, ErrorKindHTTPStatus,
		ErrorKindNetwork, ErrorKindCanceled, ErrorKindDeadline, ErrorKindError:
		return true
	default:
		return false
	}
}

// validStatus reports whether status is a plausible HTTP status code (or 0,
// meaning "not an HTTP failure").
func validStatus(status int) bool {
	return status == 0 || (status >= 100 && status <= 599)
}

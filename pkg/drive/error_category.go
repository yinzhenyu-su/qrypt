package drive

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"strings"
)

const (
	ErrorCategoryAuth           = "auth"
	ErrorCategoryRateLimit      = "rate_limit"
	ErrorCategoryNetwork        = "network"
	ErrorCategoryTimeout        = "timeout"
	ErrorCategoryRemote5xx      = "remote_5xx"
	ErrorCategoryNotFound       = "not_found"
	ErrorCategoryConflict       = "conflict"
	ErrorCategoryInvalidRequest = "invalid_request"
	ErrorCategoryLocalIO        = "local_io"
	ErrorCategoryConsistency    = "consistency"
	ErrorCategoryUnsupported    = "unsupported"
	ErrorCategoryCancelled      = "cancelled"
	ErrorCategoryUnknown        = "unknown"
)

// ErrorCategory returns a stable, low-cardinality category for debug and
// metrics output. It intentionally preserves the original error elsewhere.
func ErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ErrorCategoryCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorCategoryTimeout
	case errors.Is(err, ErrSpaceUnsupported):
		return ErrorCategoryUnsupported
	case isNetTimeout(err):
		return ErrorCategoryTimeout
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, os.ErrNotExist):
		return ErrorCategoryNotFound
	case errors.Is(err, fs.ErrExist), errors.Is(err, os.ErrExist):
		return ErrorCategoryConflict
	case isLocalIO(err):
		return ErrorCategoryLocalIO
	}
	return ErrorCategoryMessage(err.Error())
}

// ErrorCategoryMessage classifies an error string when the original error value
// is no longer available, such as persisted debug traces.
func ErrorCategoryMessage(message string) string {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return ""
	}
	switch {
	case containsAny(msg, "context canceled", "cancelled", "canceled"):
		return ErrorCategoryCancelled
	case containsAny(msg, "deadline exceeded", "timeout", "timed out", "i/o timeout"):
		return ErrorCategoryTimeout
	case containsAny(msg, "unauthorized", "forbidden", "permission denied", "access denied", "invalid token", "token expired", "login", "credential", "401", "403"):
		return ErrorCategoryAuth
	case containsAny(msg, "rate limit", "too many requests", "quota exceeded", "429"):
		return ErrorCategoryRateLimit
	case containsAny(msg, "not found", "no such file", "no such object", "404"):
		return ErrorCategoryNotFound
	case containsAny(msg, "already exists", "conflict", "409"):
		return ErrorCategoryConflict
	case containsAny(msg, "bad request", "invalid parameter", "invalid request", "malformed", "400"):
		return ErrorCategoryInvalidRequest
	case containsAny(msg, "unsupported", "not supported", "does not support", "not implemented"):
		return ErrorCategoryUnsupported
	case containsAny(msg, "content mismatch", "size mismatch", "hash mismatch", "checksum mismatch", "consistency"):
		return ErrorCategoryConsistency
	case containsAny(msg, "500", "502", "503", "504", "internal server error", "bad gateway", "service unavailable", "gateway timeout"):
		return ErrorCategoryRemote5xx
	case containsAny(msg, "connection reset", "connection refused", "broken pipe", "no route to host", "network is unreachable", "temporary failure", "eof"):
		return ErrorCategoryNetwork
	case containsAny(msg, "input/output error", "disk full", "no space left", "read-only file system"):
		return ErrorCategoryLocalIO
	default:
		return ErrorCategoryUnknown
	}
}

func isNetTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isLocalIO(err error) bool {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return true
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return true
	}
	return false
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

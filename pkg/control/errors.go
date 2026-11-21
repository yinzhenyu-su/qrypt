package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// ErrorCode is the stable, machine-readable reason of a failed request. Codes
// are part of the wire contract: clients branch on them instead of parsing
// messages, and existing codes are never reused for a different meaning.
type ErrorCode string

const (
	// CodeInvalid means the request itself was malformed or unusable.
	CodeInvalid ErrorCode = "invalid"
	// CodeNotFound means the addressed path, mount, task, or share does not
	// exist.
	CodeNotFound ErrorCode = "not_found"
	// CodeConflict means the request conflicts with current state (already
	// exists, busy, wrong lifecycle stage).
	CodeConflict ErrorCode = "conflict"
	// CodeForbidden means the filesystem or the provider denied access.
	CodeForbidden ErrorCode = "forbidden"
	// CodeUnsupported means the operation cannot be satisfied by this Core or
	// driver (for example a cross-mount rename).
	CodeUnsupported ErrorCode = "unsupported"
	// CodeUnavailable means a required internal component is not running.
	CodeUnavailable ErrorCode = "unavailable"
	// CodeTimeout means a bounded operation did not finish in time and the
	// caller may retry.
	CodeTimeout ErrorCode = "timeout"
	// CodeRateLimited means the caller exceeded an enforced request budget.
	CodeRateLimited ErrorCode = "rate_limited"
)

// ErrorBody is the payload of a failed response. Message is human-readable and
// may change; Code, Retryable, and Details are stable.
type ErrorBody struct {
	Code      ErrorCode      `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// ErrorEnvelope is the shape of every non-2xx response on the command
// endpoint. Successful responses are the bare value described for the route.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// Error implements error so a caller can reuse the body as a value.
func (e ErrorBody) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}

func writeErrorEnvelope(w http.ResponseWriter, status int, code ErrorCode, message string, retryable bool, details map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorBody{
		Code:      code,
		Message:   message,
		Retryable: retryable,
		Details:   details,
	}})
}

// writeBadRequest reports a malformed or unusable request.
func writeBadRequest(w http.ResponseWriter, message string) {
	writeErrorEnvelope(w, http.StatusBadRequest, CodeInvalid, message, false, nil)
}

// writeMethodNotAllowed reports an unsupported HTTP method for the route.
func writeMethodNotAllowed(w http.ResponseWriter) {
	writeErrorEnvelope(w, http.StatusMethodNotAllowed, CodeInvalid, "method not allowed", false, nil)
}

// writeNotFound reports a missing path, mount, task, or share.
func writeNotFound(w http.ResponseWriter, message string) {
	writeErrorEnvelope(w, http.StatusNotFound, CodeNotFound, message, false, nil)
}

// writeConflict reports a request that conflicts with current state.
func writeConflict(w http.ResponseWriter, message string) {
	writeErrorEnvelope(w, http.StatusConflict, CodeConflict, message, false, nil)
}

// writeNotImplemented reports an operation this Core does not support.
func writeNotImplemented(w http.ResponseWriter, message string) {
	writeErrorEnvelope(w, http.StatusNotImplemented, CodeUnsupported, message, false, nil)
}

// writeUnavailable reports a required component that is not running. Callers
// may retry once the component starts, so the response is retryable.
func writeUnavailable(w http.ResponseWriter, message string) {
	writeErrorEnvelope(w, http.StatusServiceUnavailable, CodeUnavailable, message, true, nil)
}

// writeTimeout reports a bounded operation that expired; retrying is safe.
func writeTimeout(w http.ResponseWriter, message string) {
	writeErrorEnvelope(w, http.StatusGatewayTimeout, CodeTimeout, message, true, nil)
}

// writeStatus reports with an explicitly chosen HTTP status, deriving the
// error code from that status. It exists for handlers that already computed a
// status, so no route falls back to a plain-text body.
func writeStatus(w http.ResponseWriter, status int, message string) {
	code, retryable := codeForStatus(status)
	writeErrorEnvelope(w, status, code, message, retryable, nil)
}

// writeFSError classifies a filesystem failure into a stable code and reports
// it. It is the only error writer used by the /v1/fs/* surface so the same Go
// error always maps to the same wire code.
func writeFSError(w http.ResponseWriter, err error) {
	status, code, retryable := classifyFSError(err)
	writeErrorEnvelope(w, status, code, err.Error(), retryable, nil)
}

// writeFSErrorPath is writeFSError with the addressed path attached as details,
// which lets a client attribute a batch failure without re-deriving it.
func writeFSErrorPath(w http.ResponseWriter, err error, path string) {
	status, code, retryable := classifyFSError(err)
	var details map[string]any
	if path != "" {
		details = map[string]any{"path": path}
	}
	writeErrorEnvelope(w, status, code, err.Error(), retryable, details)
}

// classifyFSError maps a filesystem error onto the wire contract. The mapping
// is intentionally conservative: an unknown failure is a non-retryable
// conflict so a client never retries blindly.
func classifyFSError(err error) (status int, code ErrorCode, retryable bool) {
	if err == nil {
		return http.StatusConflict, CodeConflict, false
	}
	switch {
	case errors.Is(err, vfs.ErrCrossMount):
		return http.StatusConflict, CodeUnsupported, false
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, CodeTimeout, true
	case errors.Is(err, context.Canceled):
		return http.StatusConflict, CodeConflict, false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"), strings.Contains(msg, "no such file"):
		return http.StatusNotFound, CodeNotFound, false
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"), strings.Contains(msg, "deadline exceeded"):
		return http.StatusGatewayTimeout, CodeTimeout, true
	case strings.Contains(msg, "permission denied"), strings.Contains(msg, "forbidden"), strings.Contains(msg, "not authorized"), strings.Contains(msg, "unauthorized"):
		return http.StatusForbidden, CodeForbidden, false
	case strings.Contains(msg, "read-only"), strings.Contains(msg, "read only"):
		return http.StatusForbidden, CodeForbidden, false
	case strings.Contains(msg, "cross-mount"), strings.Contains(msg, "unsupported"), strings.Contains(msg, "not supported"):
		return http.StatusConflict, CodeUnsupported, false
	case strings.Contains(msg, "already exists"), strings.Contains(msg, "conflict"), strings.Contains(msg, "busy"), strings.Contains(msg, "in use"):
		return http.StatusConflict, CodeConflict, false
	default:
		return http.StatusConflict, CodeConflict, false
	}
}

// codeForStatus derives the wire code for a bare HTTP status.
func codeForStatus(status int) (ErrorCode, bool) {
	switch status {
	case http.StatusBadRequest:
		return CodeInvalid, false
	case http.StatusNotFound:
		return CodeNotFound, false
	case http.StatusForbidden:
		return CodeForbidden, false
	case http.StatusConflict:
		return CodeConflict, false
	case http.StatusNotImplemented:
		return CodeUnsupported, false
	case http.StatusServiceUnavailable:
		return CodeUnavailable, true
	case http.StatusGatewayTimeout, http.StatusRequestTimeout:
		return CodeTimeout, true
	case http.StatusTooManyRequests:
		return CodeRateLimited, true
	case http.StatusMethodNotAllowed:
		return CodeInvalid, false
	default:
		return CodeConflict, false
	}
}

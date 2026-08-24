package httputil

import "net/http"

// IsNonRetryableClientStatus reports whether a client error should not be
// retried by a generic upload path. Timeouts and rate limits remain retryable.
func IsNonRetryableClientStatus(status int) bool {
	return status >= http.StatusBadRequest &&
		status < http.StatusInternalServerError &&
		status != http.StatusRequestTimeout &&
		status != http.StatusTooManyRequests
}

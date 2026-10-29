package drive

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var sensitiveSnippetPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(access_token=)[^&\s"']+`),
	regexp.MustCompile(`(?i)(refresh_token=)[^&\s"']+`),
	regexp.MustCompile(`(?i)(sessionKey=)[^,\s"']+`),
	regexp.MustCompile(`(?i)("access_token"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("refresh_token"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("sessionKey"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("downloadUrl"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("fileDownloadUrl"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("requestURL"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(Cookie:\s*)[^,\s"']+`),
	regexp.MustCompile(`(?i)(Authorization:\s*(?:Bearer\s+)?)[^,\s"']+`),
	regexp.MustCompile(`(?i)(token=)[^&\s"']+`),
	// URL userinfo (user:password@) - webdav-style endpoints can embed
	// credentials in the request URL itself.
	regexp.MustCompile(`(://)[^/@\s]+@`),
}

// Snippet returns a short, redacted diagnostic snippet suitable for driver
// error strings and metric payloads.
func Snippet(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 300 {
		raw = raw[:300]
	}
	snippet := string(raw)
	for _, pattern := range sensitiveSnippetPatterns {
		snippet = pattern.ReplaceAllString(snippet, "${1}<masked>")
	}
	return snippet
}

// HTTPError builds a driver-prefixed error for an HTTP failure with every
// sensitive field redacted in one place, so drivers do not hand-roll
// status/URL/body splicing:
//
//	<prefix>: METHOD <redacted-URL>: <status> (body: <redacted-snippet>)
//
// The request URL goes through Snippet, masking query tokens (token=,
// access_token=, sessionKey=, ...) and any embedded credentials; the
// response body is truncated to 300 bytes and masked for tokens, cookies,
// download URLs and authorization headers. req and body are optional and
// omitted when nil/empty; a nil resp is allowed for transport failures.
//
// The returned error wraps a stable drive sentinel selected from the status
// code (401 -> ErrAuth, 403 -> ErrPermission, 404 -> ErrNotFound,
// 409 -> ErrConflict, 422/400 -> ErrInvalidInput, 429 -> ErrRateLimit), so
// upper layers classify with errors.Is instead of parsing text. Never
// construct driver HTTP errors by hand. Route them here so redaction and
// category rules live in exactly one place.
//
// An endpoint whose structured 422 body means "missing" can pass
// HTTPErrorStatusAsNotFound(http.StatusUnprocessableEntity) without weakening
// invalid-input classification for every other endpoint.
func HTTPError(prefix string, req *http.Request, resp *http.Response, body []byte, opts ...HTTPErrorOption) error {
	var b strings.Builder
	b.WriteString(prefix)
	if req != nil && req.URL != nil {
		b.WriteString(": ")
		b.WriteString(req.Method)
		b.WriteString(" ")
		b.WriteString(Snippet([]byte(req.URL.String())))
	}
	if resp != nil {
		b.WriteString(": ")
		b.WriteString(resp.Status)
	}
	if len(body) > 0 {
		b.WriteString(" (body: ")
		b.WriteString(Snippet(body))
		b.WriteString(")")
	}
	err := errors.New(b.String())
	if resp == nil {
		return err
	}
	settings := httpErrorSettings{}
	for _, opt := range opts {
		if opt != nil {
			opt(&settings)
		}
	}
	if settings.notFoundStatuses[resp.StatusCode] {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %v", ErrAuth, err)
	case http.StatusForbidden:
		return fmt.Errorf("%w: %v", ErrPermission, err)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case http.StatusConflict:
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %v", ErrRateLimit, err)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	default:
		return err
	}
}

// HTTPErrorOption adjusts status classification for one endpoint.
type HTTPErrorOption func(*httpErrorSettings)

type httpErrorSettings struct {
	notFoundStatuses map[int]bool
}

// HTTPErrorStatusAsNotFound classifies one status code as ErrNotFound for this
// call. It exists for endpoints that overload a status whose default meaning
// differs, for example a delete API that answers 422 for an unknown id.
func HTTPErrorStatusAsNotFound(status int) HTTPErrorOption {
	return func(settings *httpErrorSettings) {
		if settings.notFoundStatuses == nil {
			settings.notFoundStatuses = map[int]bool{}
		}
		settings.notFoundStatuses[status] = true
	}
}

package util

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

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
// code (401 → drive.ErrAuth, 404 → drive.ErrNotFound, 429 →
// drive.ErrRateLimit, 400 → drive.ErrInvalidInput), so upper layers classify
// with errors.Is instead of parsing text. Never construct driver HTTP
// errors by hand — route them here so redaction and category rules live in
// exactly one place (covered by this package's tests).
func HTTPError(prefix string, req *http.Request, resp *http.Response, body []byte) error {
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
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %v", drive.ErrAuth, err)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %v", drive.ErrNotFound, err)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %v", drive.ErrRateLimit, err)
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %v", drive.ErrInvalidInput, err)
	default:
		return err
	}
}

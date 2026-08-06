package util

import (
	"errors"
	"net/http"
	"strings"
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
// Never construct driver HTTP errors by hand — route them here so the
// redaction rules live in exactly one place (covered by this package's
// tests) instead of being re-implemented per driver.
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
	return errors.New(b.String())
}

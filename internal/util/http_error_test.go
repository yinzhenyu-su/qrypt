package util_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/util"
)

// The public sanitizer's contract: one entry point that redacts every
// sensitive field of an HTTP failure (status, body snippet, URL query
// tokens, embedded credentials) so drivers never hand-roll this. These
// tests are the single source of truth for what may appear in a driver
// error string.
func TestHTTPErrorRedactsAllSensitiveFields(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/files?access_token=tok-abc123&token=t1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: 401, Status: "401 Unauthorized"}
	body := []byte(`{"error":"unauthorized","refresh_token":"rt-xyz789","downloadUrl":"https://cdn/private?token=u2"}`)

	err = util.HTTPError("quark: list", req, resp, body)

	msg := err.Error()
	t.Logf("message: %s", msg)
	for _, secret := range []string{"tok-abc123", "t1", "rt-xyz789", "https://cdn/private", "u2"} {
		if strings.Contains(msg, secret) {
			t.Errorf("message leaks %q: %s", secret, msg)
		}
	}
	for _, keep := range []string{"quark: list", "GET", "api.example.com", "401 Unauthorized", "<masked>"} {
		if !strings.Contains(msg, keep) {
			t.Errorf("message lost %q: %s", keep, msg)
		}
	}
}

func TestHTTPErrorRedactsCookieAndAuthorizationHeaders(t *testing.T) {
	// Headers themselves are never printed, but response bodies may echo
	// them; the shared Snippet rules must still mask them.
	resp := &http.Response{Status: "403 Forbidden"}
	body := []byte(`Cookie: SESSIONID=deadbeef; Authorization: Bearer bearer-token-12345`)
	err := util.HTTPError("baidu_netdisk: list", nil, resp, body)
	msg := err.Error()
	for _, secret := range []string{"deadbeef", "bearer-token-12345"} {
		if strings.Contains(msg, secret) {
			t.Errorf("message leaks %q: %s", secret, msg)
		}
	}
}

func TestHTTPErrorOmitsOptionalParts(t *testing.T) {
	// Transport failure: no request, no response, no body.
	err := util.HTTPError("s3: put", nil, nil, nil)
	if err == nil || err.Error() != "s3: put" {
		t.Errorf("transport failure = %q, want bare prefix", err)
	}

	// Request present, no response/body.
	req, _ := http.NewRequest(http.MethodPost, "https://x.example.com/upload", nil)
	err = util.HTTPError("yun139: upload", req, nil, nil)
	if !strings.Contains(err.Error(), "POST https://x.example.com/upload") {
		t.Errorf("request-only = %q", err)
	}
}

func TestHTTPErrorTruncatesLongBodies(t *testing.T) {
	resp := &http.Response{Status: "500 Internal Server Error"}
	long := bytes.Repeat([]byte("a"), 5000)
	err := util.HTTPError("p189: read", nil, resp, long)
	msg := err.Error()
	if len(msg) > 400 {
		t.Errorf("message too long after truncation: %d bytes", len(msg))
	}
	if !strings.Contains(msg, "500 Internal Server Error") {
		t.Errorf("status lost: %s", msg)
	}
}

func TestHTTPErrorMasksUserinfoCredentialsInURL(t *testing.T) {
	// webdav-style URLs can embed user:pass@; the URL redaction must not
	// leak the password even if the userinfo has no query token.
	req, err := http.NewRequest(http.MethodGet, "https://user-secret@dav.example.com/remote.php/dav/files/u/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{Status: "401 Unauthorized"}
	msg := util.HTTPError("webdav: list", req, resp, nil).Error()
	if strings.Contains(msg, "user-secret") {
		t.Errorf("URL userinfo leaks password: %s", msg)
	}
}

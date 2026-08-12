package drive_test

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestHTTPErrorRedactsAllSensitiveFields(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/files?access_token=tok-abc123&token=t1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: 401, Status: "401 Unauthorized"}
	body := []byte(`{"error":"unauthorized","refresh_token":"rt-xyz789","downloadUrl":"https://cdn/private?token=u2"}`)

	err = drive.HTTPError("quark: list", req, resp, body)

	msg := err.Error()
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
	resp := &http.Response{Status: "403 Forbidden"}
	body := []byte(`Cookie: SESSIONID=deadbeef; Authorization: Bearer bearer-token-12345`)
	err := drive.HTTPError("baidu_netdisk: list", nil, resp, body)
	msg := err.Error()
	for _, secret := range []string{"deadbeef", "bearer-token-12345"} {
		if strings.Contains(msg, secret) {
			t.Errorf("message leaks %q: %s", secret, msg)
		}
	}
}

func TestHTTPErrorOmitsOptionalParts(t *testing.T) {
	err := drive.HTTPError("s3: put", nil, nil, nil)
	if err == nil || err.Error() != "s3: put" {
		t.Errorf("transport failure = %q, want bare prefix", err)
	}

	req, _ := http.NewRequest(http.MethodPost, "https://x.example.com/upload", nil)
	err = drive.HTTPError("yun139: upload", req, nil, nil)
	if !strings.Contains(err.Error(), "POST https://x.example.com/upload") {
		t.Errorf("request-only = %q", err)
	}
}

func TestHTTPErrorTruncatesLongBodies(t *testing.T) {
	resp := &http.Response{Status: "500 Internal Server Error"}
	long := bytes.Repeat([]byte("a"), 5000)
	err := drive.HTTPError("p189: read", nil, resp, long)
	msg := err.Error()
	if len(msg) > 400 {
		t.Errorf("message too long after truncation: %d bytes", len(msg))
	}
	if !strings.Contains(msg, "500 Internal Server Error") {
		t.Errorf("status lost: %s", msg)
	}
}

func TestHTTPErrorMasksUserinfoCredentialsInURL(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://user-secret@dav.example.com/remote.php/dav/files/u/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{Status: "401 Unauthorized"}
	msg := drive.HTTPError("webdav: list", req, resp, nil).Error()
	if strings.Contains(msg, "user-secret") {
		t.Errorf("URL userinfo leaks password: %s", msg)
	}
}

func TestHTTPErrorWrapsStableSentinelsByStatus(t *testing.T) {
	cases := []struct {
		code  int
		class error
	}{
		{http.StatusUnauthorized, drive.ErrAuth},
		{http.StatusNotFound, drive.ErrNotFound},
		{http.StatusTooManyRequests, drive.ErrRateLimit},
		{http.StatusBadRequest, drive.ErrInvalidInput},
	}
	for _, tc := range cases {
		resp := &http.Response{StatusCode: tc.code, Status: http.StatusText(tc.code)}
		err := drive.HTTPError("quark: list", nil, resp, []byte(`{}`))
		if !errors.Is(err, tc.class) {
			t.Errorf("status %d: errors.Is(%q) = false, want sentinel %v", tc.code, err, tc.class)
		}
	}
	resp := &http.Response{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error"}
	err := drive.HTTPError("quark: list", nil, resp, nil)
	for _, sentinel := range []error{drive.ErrAuth, drive.ErrNotFound, drive.ErrRateLimit, drive.ErrInvalidInput} {
		if errors.Is(err, sentinel) {
			t.Errorf("500 error unexpectedly wraps %v: %v", sentinel, err)
		}
	}
}

func TestSnippetMasksSensitiveValues(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"query access_token", `{"error":"bad","url":"https://x/a?access_token=abc123&k=v"}`, "abc123"},
		{"query refresh_token", `url?refresh_token=refreshsecret99`, "refreshsecret99"},
		{"sessionKey json", `{"sessionKey":"sess-abcdef","ok":true}`, "sess-abcdef"},
		{"access_token json", `{"access_token":"tok-abc123","expires":3600}`, "tok-abc123"},
		{"refresh_token json", `{"refresh_token":"rt-xyz789"}`, "rt-xyz789"},
		{"downloadUrl json", `{"downloadUrl":"https://cdn.example.com/private/file?sign=zzz"}`, "https://cdn.example.com/private/file"},
		{"fileDownloadUrl json", `{"fileDownloadUrl":"https://cdn2.example.com/secret.bin"}`, "https://cdn2.example.com/secret.bin"},
		{"requestURL json", `{"requestURL":"https://api.example.com/private-path"}`, "https://api.example.com/private-path"},
		{"cookie header", `Cookie: SESSIONID=deadbeef; Path=/`, "deadbeef"},
		{"authorization bearer header", `Authorization: Bearer bearer-token-12345`, "bearer-token-12345"},
		{"authorization plain header", `Authorization: apikey-xyz`, "apikey-xyz"},
		{"query token", `url?token=query-token-val`, "query-token-val"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := drive.Snippet([]byte(tc.input))
			if strings.Contains(out, tc.want) {
				t.Errorf("Snippet(%q) = %q, still contains %q", tc.input, out, tc.want)
			}
		})
	}
}

func TestSnippetMasksValuesInsideLargerErrorMessage(t *testing.T) {
	input := `POST https://api.example.com/upload failed: 401 {"error":"unauthorized","access_token":"tok-abc123","refresh_token":"rt-xyz789","downloadUrl":"https://cdn/private?token=t1"}`
	out := drive.Snippet([]byte(input))
	for _, secret := range []string{"tok-abc123", "rt-xyz789", "https://cdn/private", "t1"} {
		if strings.Contains(out, secret) {
			t.Errorf("output %q still contains %q", out, secret)
		}
	}
	for _, keep := range []string{"POST", "upload failed", "401", "unauthorized"} {
		if !strings.Contains(out, keep) {
			t.Errorf("output %q lost non-sensitive text %q", out, keep)
		}
	}
}

func TestSnippetLeavesPlainTextUntouched(t *testing.T) {
	input := `upload failed: permission denied (code=403, reason=quota_exceeded)`
	out := drive.Snippet([]byte(input))
	if out != input {
		t.Errorf("Snippet mangled plain text: %q", out)
	}
}

func TestSnippetTruncatesLongBodies(t *testing.T) {
	long := strings.Repeat("a", 5000)
	out := drive.Snippet([]byte(long))
	if len(out) > 300 {
		t.Errorf("Snippet did not truncate: len=%d", len(out))
	}
}

func TestSnippetHandlesBinaryAndEmptyInput(t *testing.T) {
	if out := drive.Snippet([]byte{0x00, 0xff, 0xfe, 'x'}); !strings.Contains(out, "x") {
		t.Errorf("binary input mangled: %q", out)
	}
	if out := drive.Snippet(nil); out != "" {
		t.Errorf("empty input = %q, want empty", out)
	}
	if out := drive.Snippet([]byte("   \n\t ")); out != "" {
		t.Errorf("whitespace-only input = %q, want empty", out)
	}
}

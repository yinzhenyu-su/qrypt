package util_test

import (
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
)

// Each sensitive pattern must be masked wherever it appears, while plain
// error text without credentials must pass through unchanged. This is the
// single source of truth for "what may appear in error strings / logs" -
// drivers do not get to roll their own redaction rules.
func TestSnippetMasksSensitiveValues(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string // substring that must NOT appear after redaction
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
			out := util.Snippet([]byte(tc.input))
			if strings.Contains(out, tc.want) {
				t.Errorf("Snippet(%q) = %q, still contains %q", tc.input, out, tc.want)
			}
		})
	}
}

func TestSnippetMasksValuesInsideLargerErrorMessage(t *testing.T) {
	// The realistic shape: an HTTP failure message with the response body
	// appended. Every credential in it must be masked, other text kept.
	input := `POST https://api.example.com/upload failed: 401 {"error":"unauthorized","access_token":"tok-abc123","refresh_token":"rt-xyz789","downloadUrl":"https://cdn/private?token=t1"}`
	out := util.Snippet([]byte(input))
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
	// False-positive guard: ordinary error messages must not be mangled.
	input := `upload failed: permission denied (code=403, reason=quota_exceeded)`
	out := util.Snippet([]byte(input))
	if out != input {
		t.Errorf("Snippet mangled plain text: %q", out)
	}
}

func TestSnippetTruncatesLongBodies(t *testing.T) {
	long := strings.Repeat("a", 5000)
	out := util.Snippet([]byte(long))
	if len(out) > 300 {
		t.Errorf("Snippet did not truncate: len=%d", len(out))
	}
}

func TestSnippetHandlesBinaryAndEmptyInput(t *testing.T) {
	if out := util.Snippet([]byte{0x00, 0xff, 0xfe, 'x'}); !strings.Contains(out, "x") {
		t.Errorf("binary input mangled: %q", out)
	}
	if out := util.Snippet(nil); out != "" {
		t.Errorf("empty input = %q, want empty", out)
	}
	if out := util.Snippet([]byte("   \n\t ")); out != "" {
		t.Errorf("whitespace-only input = %q, want empty", out)
	}
}

package drive_test

import (
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// FuzzErrorCategoryMessage pins the error taxonomy: arbitrary message text
// must classify without panicking and only ever produce known categories.
func FuzzErrorCategoryMessage(f *testing.F) {
	seeds := []string{
		"", "not found", "timeout", "permission denied", "connection refused",
		"boom", "context canceled", "401 unauthorized", "rate limit exceeded",
		"internal server error", "disk full", "unsupported operation",
		"hash mismatch", "already exists", "garbage %s input \x00",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	valid := map[string]bool{
		"":     true,
		"auth": true, "permission": true, "rate_limit": true, "network": true,
		"timeout": true, "remote_5xx": true, "not_found": true, "conflict": true,
		"invalid_request": true, "local_io": true, "consistency": true,
		"unsupported": true, "persistence": true, "cancelled": true, "unknown": true,
	}
	f.Fuzz(func(t *testing.T, msg string) {
		cat := drive.ErrorCategoryMessage(msg)
		if !valid[cat] {
			t.Fatalf("unknown category %q for message %q", cat, msg)
		}
		// Unsupported-marker sanity: an "unsupported" mention always lands
		// in the unsupported bucket regardless of surrounding text.
		if strings.Contains(strings.ToLower(msg), "unsupported") {
			if cat != drive.ErrorCategoryUnsupported {
				t.Fatalf("message with 'unsupported' classified as %q", cat)
			}
		}
	})
}

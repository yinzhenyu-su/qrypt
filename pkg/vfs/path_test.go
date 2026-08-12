package vfs

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsNotFound covers the sentinel-only contract: only errors wrapping
// ErrNotFound are classified as missing. Bare "not found" text without the
// sentinel is deliberately not matched — drivers must wrap drive.ErrNotFound
// (drive.HTTPError does this automatically for 404 responses).
func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"sentinel itself", ErrNotFound, true},
		{"wrapped sentinel (driver style)", fmt.Errorf("%w: quark: child not found: x", ErrNotFound), true},
		{"wrapped with context", fmt.Errorf("get %q: %w", "a/b", ErrNotFound), true},
		{"bare string is not classified", fmt.Errorf("quark: not found"), false},
		{"bare string with context", fmt.Errorf("189: path \"/a\" not found"), false},
		{"nil", nil, false},
		{"unrelated error", errors.New("network timeout"), false},
		{"unrelated with size word", errors.New("found no space left"), false},
	}
	for _, tt := range tests {
		if got := IsNotFound(tt.err); got != tt.want {
			t.Errorf("IsNotFound(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

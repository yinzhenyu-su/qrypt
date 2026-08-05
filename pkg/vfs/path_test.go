package vfs

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsNotFound covers the two detection paths: the sentinel check that
// wrapped driver errors now satisfy, and the legacy string fallback that
// keeps unwrapped errors (and non-Go callers) working until every driver
// wraps ErrNotFound.
func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"sentinel itself", ErrNotFound, true},
		{"wrapped sentinel (driver style)", fmt.Errorf("%w: quark: child not found: x", ErrNotFound), true},
		{"wrapped with context", fmt.Errorf("get %q: %w", "a/b", ErrNotFound), true},
		{"legacy bare string", fmt.Errorf("quark: not found"), true},
		{"legacy string with context", fmt.Errorf("189: path \"/a\" not found"), true},
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

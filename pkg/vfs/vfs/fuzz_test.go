package vfs_test

import (
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

// FuzzCleanVirtualPath pins the path normalization contract:
//   - idempotent: clean(clean(p)) == clean(p)
//   - always rooted at "/"
//   - never escapes: no ".." component survives
//   - no trailing slash except for the root itself
func FuzzCleanVirtualPath(f *testing.F) {
	seeds := []string{
		"", "/", "a", "/a/b", "a/../b", "../etc/passwd", "//", "a/./b/",
		" ", "C:\\windows\\path", "/a//b///c/", "a/b/../c/d", "///", "a/..",
		"\x00", "中文/路径", ".", "././.",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		first := vfs.CleanVirtualPath(p)
		second := vfs.CleanVirtualPath(first)
		if second != first {
			t.Fatalf("clean not idempotent: %q -> %q -> %q", p, first, second)
		}
		if !strings.HasPrefix(first, "/") {
			t.Fatalf("clean result must be rooted: %q (from %q)", first, p)
		}
		for _, part := range strings.Split(first, "/") {
			if part == ".." {
				t.Fatalf("clean result must not contain '..': %q (from %q)", first, p)
			}
		}
		if first != "/" && strings.HasSuffix(first, "/") {
			t.Fatalf("clean result must not have a trailing slash: %q (from %q)", first, p)
		}
	})
}

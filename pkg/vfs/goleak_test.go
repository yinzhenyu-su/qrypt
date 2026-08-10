package vfs_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks after every test in this package
// (VFS lifecycle: upload engine, delete timers, cache writers).
//
// runReadCacheWriter is ignored: its lifecycle is owned by
// CloseReadCache, which the core package's cleanup always calls. The
// vfs tests create 100+ VFS instances directly via New() and most never
// close the read cache, which is a test-harness pattern rather than a
// leak in production; upload workers and delete/upload timers are still
// asserted (they must exit on context cancellation).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/yinzhenyu/qrypt/internal/vfs/readcache.(*Store).runReadCacheWriter"),
	)
}

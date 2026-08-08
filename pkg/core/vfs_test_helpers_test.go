package core

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

// stopTestVFS synchronously drops a started VFS's cache files so a test
// TempDir cleanup never races the asynchronous shutdown. Pair it with the
// deferred context cancel (which stops upload workers) by adding this right
// after fs is created:
//
//	defer stopTestVFS(t, fs)
func stopTestVFS(t testing.TB, fs vfs.FileSystem) {
	t.Helper()
	if closer, ok := fs.(interface{ CloseReadCache() error }); ok {
		if err := closer.CloseReadCache(); err != nil {
			t.Logf("close read cache: %v", err)
		}
	}
	if clearer, ok := fs.(interface{ ClearReadCache() error }); ok {
		if err := clearer.ClearReadCache(); err != nil {
			t.Logf("clear read cache: %v", err)
		}
	}
}

// newTestCore builds a Core over a filesystem and registers cleanup that
// closes it, so the lazily-created task manager (whose runner goroutines
// derive from the manager context, not the caller's) always shuts down.
func newTestCore(t *testing.T, fs BuiltFileSystem) *Core {
	t.Helper()
	c := &Core{fs: fs}
	t.Cleanup(func() {
		if err := c.Close(context.Background()); err != nil {
			t.Logf("core close: %v", err)
		}
	})
	return c
}

package vfs_test

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"strings"
	"testing"
	"time"
)

func waitNoPending(t *testing.T, fs vfs.FileSystem) {
	t.Helper()
	inspector, ok := fs.(vfs.UploadInspector)
	if !ok {
		t.Fatal("filesystem does not expose upload state")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(inspector.PendingUploads()) == 0 && activeUploadCount(fs) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending uploads did not drain: %+v", inspector.PendingUploads())
}
func activeUploadCount(fs vfs.FileSystem) int {
	snapshotter, ok := fs.(interface {
		DebugSnapshot() diagnostics.DebugSnapshot
	})
	if !ok {
		return 0
	}
	count := 0
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		count += len(mount.ActiveUploads())
	}
	return count
}
func waitForCondition(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition was not met before deadline")
}
func singleMountHealth(t *testing.T, fs *vfs.VFS) diagnostics.MountHealth {
	t.Helper()
	health, err := fs.MountHealth(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(health) != 1 {
		t.Fatalf("mount health count = %d, want 1: %+v", len(health), health)
	}
	return health[0]
}
func assertHealthOp(t *testing.T, health diagnostics.MountHealth, op string, success, failures int) {
	t.Helper()
	got, ok := health.Ops[op]
	if !ok {
		t.Fatalf("missing health op %q in %+v", op, health.Ops)
	}
	if got.Success != success || got.Errors != failures {
		t.Fatalf("%s health = %+v, want success=%d errors=%d", op, got, success, failures)
	}
}
func namesOf(entries []drive.Entry) string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return strings.Join(names, ",")
}

// vfsStopTimeout bounds stopVFS's wait for teardown: generous enough for a
// loaded CI box to flush queued cache writes, short enough that a teardown
// which never finishes fails the test instead of stalling the package.
const vfsStopTimeout = 5 * time.Second

// stopVFS synchronously releases a VFS -- or a Namespace over several -- so a
// test's TempDir cleanup never races the asynchronous shutdown. Close cancels
// the lifecycle context, stops the upload and delete timers, waits for upload
// workers and background enqueues to exit, and flushes the read-cache writer,
// so nothing is still writing into the test's directories once this returns.
// Add it right after the filesystem is created:
//
//	defer stopVFS(t, fs)
//
// The test's own deferred context cancel stays where it is: it releases
// whatever outlived the filesystem, and Close is idempotent alongside it.
//
// This used to only drop the read cache and leave the workers to that deferred
// cancel, which is not awaited -- so a worker could still be staging a file
// when t.TempDir's cleanup ran, and the cleanup failed with "directory not
// empty". Waiting here is what makes the shutdown ordered.
func stopVFS(t *testing.T, fs vfs.FileSystem) {
	t.Helper()
	if closer, ok := fs.(interface{ Close(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(context.Background(), vfsStopTimeout)
		defer cancel()
		if err := closer.Close(ctx); err != nil {
			t.Errorf("close filesystem: %v", err)
		}
		return
	}
	// Filesystems without Close: drop the cache by hand, as before.
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

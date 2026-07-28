package vfs_test

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
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
		DebugSnapshot() vfs.DebugSnapshot
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
func singleMountHealth(t *testing.T, fs *vfs.VFS) vfs.MountHealth {
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
func assertHealthOp(t *testing.T, health vfs.MountHealth, op string, success, failures int) {
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

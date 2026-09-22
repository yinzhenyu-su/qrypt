package vfs_test

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// Mount write-path bookkeeping records are Internal scope (glossary: 挂载写入
// 路径的内部记账记录), not Sync: Sync is reserved for the syncer.
func TestVFSUploadBookkeepingTasksAreInternalScope(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/scope.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/scope.txt"); err != nil {
		t.Fatal(err)
	}
	tasks := fs.Tasks(task.Filter{Scope: task.ScopeInternal})
	if len(tasks) != 1 || tasks[0].Scope != task.ScopeInternal {
		t.Fatalf("internal scope tasks = %+v, want one internal bookkeeping task", tasks)
	}
	if tasks[0].Visibility != task.VisibilityHidden {
		t.Fatalf("bookkeeping visibility = %q, want hidden (stays out of the app default list)", tasks[0].Visibility)
	}
	if got := fs.Tasks(task.Filter{Visibility: task.VisibilityVisible}); len(got) != 0 {
		t.Fatalf("visible tasks = %+v, want none (bookkeeping is hidden)", got)
	}
	if got := fs.Tasks(task.Filter{Scope: task.ScopeUser}); len(got) != 0 {
		t.Fatalf("user scope tasks = %+v, want none", got)
	}
	assertNoSyncScopeTasks(t, fs)
}

// Persisted pending-upload records carry no scope of their own; replaying an
// existing record must normalize it to the Internal origin.
func TestVFSReplayedUploadBookkeepingNormalizesToInternalScope(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first, err := vfs.New(&countingUploadDriver{}, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, first)
	first.Start(firstCtx)
	if _, err := first.WriteAt(firstCtx, "/replay-scope.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := first.Flush(firstCtx, "/replay-scope.txt"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool { return len(first.PendingUploads()) == 1 })
	cancelFirst()

	second, err := vfs.New(&countingUploadDriver{}, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	defer stopVFS(t, second)
	second.Start(secondCtx)

	waitForCondition(t, func() bool { return len(second.Tasks(task.Filter{})) == 1 })
	tasks := second.Tasks(task.Filter{Scope: task.ScopeInternal})
	if len(tasks) != 1 || tasks[0].Scope != task.ScopeInternal {
		t.Fatalf("replayed bookkeeping tasks = %+v, want one internal-scope task", tasks)
	}
	if tasks[0].Visibility != task.VisibilityHidden {
		t.Fatalf("replayed bookkeeping visibility = %q, want hidden", tasks[0].Visibility)
	}
	assertNoSyncScopeTasks(t, second)
}

// assertNoSyncScopeTasks pins Sync as syncer-only: the mount write path must
// never surface a sync-scope bookkeeping record.
func assertNoSyncScopeTasks(t *testing.T, fs *vfs.VFS) {
	t.Helper()
	if got := fs.Tasks(task.Filter{Scope: task.ScopeSync}); len(got) != 0 {
		t.Fatalf("sync scope tasks = %+v, want none (sync origin is syncer-only)", got)
	}
}

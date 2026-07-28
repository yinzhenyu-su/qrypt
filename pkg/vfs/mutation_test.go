package vfs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestVFSCoalescesChildDeletesIntoDirectoryDelete(t *testing.T) {
	ctx := context.Background()
	drv := newCountingRemoveDriver()
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, DeleteDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if err := fs.Remove(ctx, "/dir/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(ctx, "/dir/sub/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveDir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, func() bool { return len(drv.removedIDs()) == 1 })
	removed := drv.removedIDs()
	if len(removed) != 1 || removed[0] != "dir" {
		t.Fatalf("removed ids = %v, want [dir]", removed)
	}
	if _, err := fs.Stat(ctx, "/dir"); err == nil {
		t.Fatal("deleted directory should be hidden from stat")
	}
}

func TestVFSDeleteTasksExposeScheduledDeletes(t *testing.T) {
	ctx := context.Background()
	drv := newCountingRemoveDriver()
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, DeleteDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if err := fs.Remove(ctx, "/dir/a.txt"); err != nil {
		t.Fatal(err)
	}
	tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeDeleteRemote}})
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v, want one delete task", tasks)
	}
	item := tasks[0]
	if item.Scope != task.ScopeSync || item.State != task.StateScheduled || item.Path != "/dir/a.txt" || !item.Capabilities.Cancelable || item.Capabilities.Retryable {
		t.Fatalf("delete task = %+v, want scheduled cancelable background task", item)
	}
}

func TestVFSDeleteTaskCancelRestoresPendingDelete(t *testing.T) {
	ctx := context.Background()
	drv := newCountingRemoveDriver()
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, DeleteDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if err := fs.Remove(ctx, "/dir/a.txt"); err != nil {
		t.Fatal(err)
	}
	tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeDeleteRemote}})
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v, want one delete task", tasks)
	}
	if err := fs.CancelTask(ctx, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(ctx, "/dir/a.txt"); err != nil {
		t.Fatalf("deleted path was not restored: %v", err)
	}
	if tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeDeleteRemote}}); len(tasks) != 0 {
		t.Fatalf("tasks after cancel = %+v, want none", tasks)
	}
	if removed := drv.removedIDs(); len(removed) != 0 {
		t.Fatalf("remote deletes = %v, want none", removed)
	}
}

func TestVFSDeleteTaskRetryFailedDelete(t *testing.T) {
	ctx := context.Background()
	drv := newCountingRemoveDriver()
	drv.failRemoves = 1
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, DeleteDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if err := fs.Remove(ctx, "/dir/a.txt"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeDeleteRemote}})
		return len(tasks) == 1 && tasks[0].State == task.StateFailed && tasks[0].Capabilities.Retryable
	})
	tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeDeleteRemote}})
	if err := fs.RetryTask(ctx, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool { return len(drv.removedIDs()) == 1 })
	if tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeDeleteRemote}}); len(tasks) != 0 {
		t.Fatalf("tasks after retry success = %+v, want none", tasks)
	}
}

func TestVFSMkdirRestoresPendingDeletedDirectory(t *testing.T) {
	ctx := context.Background()
	drv := newCountingRemoveDriver()
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, DeleteDelay: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	if err := fs.RemoveDir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Mkdir(ctx, "/dir/sub"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)

	if got := drv.mkdirCount(); got != 0 {
		t.Fatalf("remote mkdir count = %d, want 0", got)
	}
	if removed := drv.removedIDs(); len(removed) != 0 {
		t.Fatalf("remote deletes = %v, want none", removed)
	}
	if _, err := fs.Stat(ctx, "/dir/sub"); err != nil {
		t.Fatalf("restored child directory should remain visible: %v", err)
	}
}

func TestVFSRemoveDirDropsPendingChildren(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.Mkdir(filepath.Join(remote, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   time.Hour,
		DeleteDelay:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := fs.Create(ctx, "/dir/pending.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/dir/pending.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/dir/pending.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.RemoveDir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(ctx, "/dir/pending.txt"); err == nil {
		t.Fatal("pending child should not survive directory removal")
	}
	entries, err := fs.List(ctx, "/dir")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == "pending.txt" {
			t.Fatalf("pending child leaked into restored directory list: %v", entries)
		}
	}
	if pending := fs.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending files = %v, want none", pending)
	}
}

func TestVFSMkdirStaysVisibleWhenBackendListIsStale(t *testing.T) {
	ctx := context.Background()
	fs, err := vfs.New(&staleMkdirListDriver{}, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fs.Mkdir(ctx, "/new-folder"); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "new-folder" || !entries[0].IsDir {
		t.Fatalf("created directory should remain visible, got %+v", entries)
	}
	if _, err := fs.Stat(ctx, "/new-folder"); err != nil {
		t.Fatalf("created directory should remain stat-able: %v", err)
	}
}

func TestVFSMkdirReusesExistingDirectoryOnConflict(t *testing.T) {
	ctx := context.Background()
	drv := &existingMkdirDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	entry, err := fs.Mkdir(ctx, "/dir")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "existing-dir" || !entry.IsDir {
		t.Fatalf("unexpected reused directory: %+v", entry)
	}
	if drv.mkdirs != 1 {
		t.Fatalf("mkdir count = %d, want 1", drv.mkdirs)
	}
	if _, err := fs.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if drv.mkdirs != 1 {
		t.Fatalf("cached mkdir count = %d, want 1", drv.mkdirs)
	}
}

func TestVFSUploadsFileInsideLocallyKnownStaleDirectory(t *testing.T) {
	ctx := context.Background()
	drv := &staleMkdirListDriver{failFirstPut: true}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.Mkdir(ctx, "/new-folder"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/new-folder/file.txt", []byte("content"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/new-folder/file.txt"); err != nil {
		t.Fatal(err)
	}

	waitNoPending(t, fs)
	attempts, parent, name, data := drv.lastPut()
	if attempts != 2 {
		t.Fatalf("put attempts = %d, want 2", attempts)
	}
	if parent != "dir-id" || name != "file.txt" || data != "content" {
		t.Fatalf("unexpected put parent=%q name=%q data=%q", parent, name, data)
	}
}

func TestVFSStatMissingChildrenInNewLocalDirectoryDoesNotRelistRemote(t *testing.T) {
	ctx := context.Background()
	drv := &staleMkdirListDriver{listCalls: map[string]int{}}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fs.Mkdir(ctx, "/admin"); err != nil {
		t.Fatal(err)
	}
	before := drv.listCount("dir-id")
	for _, path := range []string{"/admin/index.html", "/admin/content", "/admin/system"} {
		if _, err := fs.Stat(ctx, path); err == nil {
			t.Fatalf("Stat(%q) unexpectedly succeeded", path)
		}
	}
	if got := drv.listCount("dir-id"); got != before {
		t.Fatalf("new local directory child stat listed remote %d times, want %d", got, before)
	}
}

func TestVFSPrepareDirectoryCopyClearsPendingChildren(t *testing.T) {
	ctx := context.Background()
	drv := &staleMkdirListDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fs.Mkdir(ctx, "/_nuxt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/_nuxt/LimitGroup.B_5XwyXE.css", []byte("body"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/_nuxt/LimitGroup.B_5XwyXE.css"); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending child should be visible before copy prepare, got %+v", entries)
	}

	if err := fs.PrepareDirectoryCopy(ctx, "/_nuxt"); err != nil {
		t.Fatal(err)
	}
	entries, err = fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pending children should be cleared, got %+v", entries)
	}
	if pending := fs.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending should be empty, got %+v", pending)
	}
}

func TestVFSPrepareDirectoryCopyHidesExistingRemoteChildrenUntilRecreated(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.Mkdir(filepath.Join(remote, "_nuxt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "_nuxt", "LimitGroup.B_5XwyXE.css"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := localfs.New(remote)
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected existing remote child, got %+v", entries)
	}
	if err := fs.PrepareDirectoryCopy(ctx, "/_nuxt"); err != nil {
		t.Fatal(err)
	}
	entries, err = fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("existing children should be hidden during copy, got %+v", entries)
	}
	if _, err := fs.Stat(ctx, "/_nuxt/LimitGroup.B_5XwyXE.css"); err == nil {
		t.Fatal("hidden child should not stat before recreate")
	}

	if _, err := fs.WriteAt(ctx, "/_nuxt/LimitGroup.B_5XwyXE.css", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	entries, err = fs.List(ctx, "/_nuxt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "LimitGroup.B_5XwyXE.css" || entries[0].Size != 3 {
		t.Fatalf("recreated pending child should be visible, got %+v", entries)
	}
}

func TestVFSRenameMoveOverlayHidesStaleBackendEntries(t *testing.T) {
	ctx := context.Background()
	drv := &staleMoveListDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if err := fs.Rename(ctx, "/新建文件夹", "/video"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(ctx, "/movie.mp4", "/video/movie.mp4"); err != nil {
		t.Fatal(err)
	}

	rootEntries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if namesOf(rootEntries) != "video" {
		t.Fatalf("root entries = %q, want video", namesOf(rootEntries))
	}
	videoEntries, err := fs.List(ctx, "/video")
	if err != nil {
		t.Fatal(err)
	}
	if namesOf(videoEntries) != "movie.mp4" {
		t.Fatalf("video entries = %q, want movie.mp4", namesOf(videoEntries))
	}
	if _, err := fs.Stat(ctx, "/movie.mp4"); err == nil {
		t.Fatal("old moved file path should be hidden")
	}
}

func TestVFSRenameMoveOverlayConfirmsRemoteConvergence(t *testing.T) {
	ctx := context.Background()
	drv := &staleMoveListDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if err := fs.Rename(ctx, "/新建文件夹", "/video"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(ctx, "/movie.mp4", "/video/movie.mp4"); err != nil {
		t.Fatal(err)
	}
	drv.converged = true

	rootEntries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if namesOf(rootEntries) != "video" {
		t.Fatalf("root entries = %q, want video", namesOf(rootEntries))
	}
	videoEntries, err := fs.List(ctx, "/video")
	if err != nil {
		t.Fatal(err)
	}
	if namesOf(videoEntries) != "movie.mp4" {
		t.Fatalf("video entries = %q, want movie.mp4", namesOf(videoEntries))
	}
}

func TestVFSRenameUploadedFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if _, err := fs.WriteAt(ctx, "/old.txt", []byte("rename me"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/old.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	if err := fs.Rename(ctx, "/old.txt", "/new.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remote + "/old.txt"); !os.IsNotExist(err) {
		t.Fatalf("old file should not exist, err=%v", err)
	}
	data, err := os.ReadFile(remote + "/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rename me" {
		t.Fatalf("unexpected renamed data: %q", data)
	}
}

func TestVFSRenameUploadUpload(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("pending rename"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(ctx, "/draft.txt", "/final.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/final.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	data, err := os.ReadFile(remote + "/final.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "pending rename" {
		t.Fatalf("unexpected pending renamed data: %q", data)
	}
}

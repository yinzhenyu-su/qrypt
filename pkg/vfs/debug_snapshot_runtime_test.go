package vfs

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSDebugSnapshotRuntimeCollectsSortedOverlayState(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDebugSnapshotRuntime(fs)
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	newVFSDeleteScheduler(fs).Schedule("/z.txt", drive.Entry{ID: "z"}, time.Hour, func() {})
	newVFSDeleteScheduler(fs).Schedule("/a.txt", drive.Entry{ID: "a"}, time.Hour, func() {})
	fs.view.overlay.mu.Lock()
	fs.view.overlay.deleted["/z.txt"] = drive.Entry{ID: "z", Name: "z.txt"}
	fs.view.overlay.deleted["/a.txt"] = drive.Entry{ID: "a", Name: "a.txt"}
	fs.view.overlay.renameOverlays["/old"] = overlayOp{oldPath: "/old", newPath: "/new", entryID: "id"}
	fs.view.overlay.restoredDirs["/restored"] = future
	fs.view.overlay.restoredDirs["/expired"] = past
	fs.view.overlay.copyHiddenChildren["/dir"] = map[string]time.Time{"b.txt": future, "a.txt": future, "old.txt": past}
	fs.view.overlay.mu.Unlock()
	defer runtime.StopAllDeleteTimersForTest()

	overlay := runtime.Overlay()
	if len(overlay.DeleteTimers) != 2 || overlay.DeleteTimers[0].Path != "/a.txt" || overlay.DeleteTimers[1].Path != "/z.txt" {
		t.Fatalf("delete timers = %+v", overlay.DeleteTimers)
	}
	if len(overlay.Deleted) != 2 || overlay.Deleted[0].Path != "/a.txt" || overlay.Deleted[1].Path != "/z.txt" {
		t.Fatalf("deleted = %+v", overlay.Deleted)
	}
	if len(overlay.RestoredDirs) != 1 || overlay.RestoredDirs[0].Path != "/restored" {
		t.Fatalf("restored dirs = %+v", overlay.RestoredDirs)
	}
	if len(overlay.CopyHidden) != 1 || len(overlay.CopyHidden[0].Names) != 2 || overlay.CopyHidden[0].Names[0].Path != "a.txt" || overlay.CopyHidden[0].Names[1].Path != "b.txt" {
		t.Fatalf("copy hidden = %+v", overlay.CopyHidden)
	}
}

func TestVFSDebugSnapshotRuntimeCollectsIdentityQueuesAndPending(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{
		Name:          "cloud",
		StorageDir:    t.TempDir(),
		RootID:        "root",
		UploadWorkers: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDebugSnapshotRuntime(fs)
	if err := fs.uploads.Store().SaveUpload(PendingUpload{Path: "/pending.txt", FID: "pending", Name: "pending.txt"}); err != nil {
		t.Fatal(err)
	}

	identity := runtime.Identity("cloud")
	if identity.Name != "cloud" || identity.RootID != "root" {
		t.Fatalf("identity = %+v", identity)
	}
	queues := runtime.Queues()
	if queues.UploadWorkers != 3 || queues.UploadCap == 0 {
		t.Fatalf("queues = %+v", queues)
	}
	pending := runtime.PendingUploads()
	if len(pending) != 1 || pending[0].FID != "pending" {
		t.Fatalf("pending = %+v", pending)
	}
}

func (r vfsDebugSnapshotRuntime) StopAllDeleteTimersForTest() {
	newVFSDeleteScheduler(r.v).StopAll()
}

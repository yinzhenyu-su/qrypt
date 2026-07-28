package vfs

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSUploadSchedulerTracksAndCancelsTimers(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newVFSUploadScheduler(fs)
	scheduler.Schedule(PendingUpload{Path: "/dir/a.txt", FID: "a"}, time.Hour)
	scheduler.Schedule(PendingUpload{Path: "/dir/sub/b.txt", FID: "b"}, time.Hour)
	scheduler.Schedule(PendingUpload{Path: "/other.txt", FID: "other"}, time.Hour)
	defer scheduler.StopAll()

	paths := scheduler.TimerPaths()
	if !paths["/dir/a.txt"] || !paths["/dir/sub/b.txt"] || !paths["/other.txt"] {
		t.Fatalf("timer paths = %+v", paths)
	}

	scheduler.CancelChildren("/dir")
	paths = scheduler.TimerPaths()
	if paths["/dir/a.txt"] || paths["/dir/sub/b.txt"] || !paths["/other.txt"] {
		t.Fatalf("timer paths after cancel children = %+v", paths)
	}

	scheduler.Cancel("/other.txt")
	if paths = scheduler.TimerPaths(); len(paths) != 0 {
		t.Fatalf("timer paths after cancel = %+v", paths)
	}
}

func TestVFSUploadSchedulerReschedulesExistingTimer(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newVFSUploadScheduler(fs)
	scheduler.Schedule(PendingUpload{Path: "/file.txt", FID: "old"}, time.Hour)
	scheduler.Schedule(PendingUpload{Path: "/file.txt", FID: "new"}, time.Hour)
	defer scheduler.StopAll()

	paths := scheduler.TimerPaths()
	if len(paths) != 1 || !paths["/file.txt"] {
		t.Fatalf("timer paths = %+v", paths)
	}
}

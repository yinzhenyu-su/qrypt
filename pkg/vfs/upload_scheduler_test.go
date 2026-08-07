package vfs

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestVFSUploadSchedulerTracksAndCancelsTimers(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := fs.uploads
	s.EnqueueAfter(PendingUpload{Path: "/dir/a.txt", FID: "a"}, time.Hour)
	s.EnqueueAfter(PendingUpload{Path: "/dir/sub/b.txt", FID: "b"}, time.Hour)
	s.EnqueueAfter(PendingUpload{Path: "/other.txt", FID: "other"}, time.Hour)
	defer s.Close()

	paths := s.TimerPaths()
	if !paths["/dir/a.txt"] || !paths["/dir/sub/b.txt"] || !paths["/other.txt"] {
		t.Fatalf("timer paths = %+v", paths)
	}

	s.CancelChildUploads("/dir")
	paths = s.TimerPaths()
	if paths["/dir/a.txt"] || paths["/dir/sub/b.txt"] || !paths["/other.txt"] {
		t.Fatalf("timer paths after cancel children = %+v", paths)
	}

	s.CancelUpload("/other.txt")
	if paths = s.TimerPaths(); len(paths) != 0 {
		t.Fatalf("timer paths after cancel = %+v", paths)
	}
}

func TestVFSUploadSchedulerReschedulesExistingTimer(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := fs.uploads
	s.EnqueueAfter(PendingUpload{Path: "/file.txt", FID: "old"}, time.Hour)
	s.EnqueueAfter(PendingUpload{Path: "/file.txt", FID: "new"}, time.Hour)
	defer s.Close()

	paths := s.TimerPaths()
	if len(paths) != 1 || !paths["/file.txt"] {
		t.Fatalf("timer paths = %+v", paths)
	}
}

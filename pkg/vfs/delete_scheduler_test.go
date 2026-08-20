package vfs

import (
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestVFSDeleteSchedulerSchedulesAndClearsFailure(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newVFSDeleteScheduler(fs)
	fs.view.overlay.mu.Lock()
	fs.deletes.tasks.failures["/file.txt"] = "old failure"
	fs.view.overlay.mu.Unlock()

	fired := make(chan struct{}, 1)
	scheduler.Schedule("/file.txt", drive.Entry{ID: "file"}, time.Hour, func() { fired <- struct{}{} })
	fs.view.overlay.mu.Lock()
	_, timerExists := fs.deletes.tasks.scheduler.Keys()["/file.txt"]
	_, failureExists := fs.deletes.tasks.failures["/file.txt"]
	fs.view.overlay.mu.Unlock()
	if !timerExists || failureExists {
		t.Fatalf("timerExists=%v failureExists=%v, want scheduled without failure", timerExists, failureExists)
	}
	select {
	case <-fired:
		t.Fatal("delete timer fired too early")
	default:
	}
	scheduler.StopAll()
}

func TestVFSDeleteSchedulerCancelsChildDeletes(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newVFSDeleteScheduler(fs)
	scheduler.Schedule("/dir/a.txt", drive.Entry{ID: "a"}, time.Hour, func() {})
	scheduler.Schedule("/other.txt", drive.Entry{ID: "other"}, time.Hour, func() {})
	fs.view.overlay.mu.Lock()
	fs.view.overlay.setDeleted("/dir/a.txt", drive.Entry{ID: "a"})
	fs.view.overlay.setDeleted("/other.txt", drive.Entry{ID: "other"})
	fs.deletes.tasks.failures["/dir/a.txt"] = "failed"
	fs.view.overlay.mu.Unlock()

	scheduler.CancelChildren("/dir")
	fs.view.overlay.mu.Lock()
	_, childTimer := fs.deletes.tasks.scheduler.Keys()["/dir/a.txt"]
	_, otherTimer := fs.deletes.tasks.scheduler.Keys()["/other.txt"]
	_, childDeleted := fs.view.overlay.deleted["/dir/a.txt"]
	_, otherDeleted := fs.view.overlay.deleted["/other.txt"]
	_, childFailure := fs.deletes.tasks.failures["/dir/a.txt"]
	fs.view.overlay.mu.Unlock()
	if childTimer || childDeleted || childFailure {
		t.Fatalf("child timer=%v deleted=%v failure=%v, want cleared", childTimer, childDeleted, childFailure)
	}
	if !otherTimer || !otherDeleted {
		t.Fatalf("other timer=%v deleted=%v, want preserved", otherTimer, otherDeleted)
	}
	scheduler.StopAll()
}

func TestVFSDeleteSchedulerStopsAllTimers(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newVFSDeleteScheduler(fs)
	scheduler.Schedule("/a.txt", drive.Entry{ID: "a"}, time.Hour, func() {})
	scheduler.Schedule("/b.txt", drive.Entry{ID: "b"}, time.Hour, func() {})
	scheduler.StopAll()
	fs.view.overlay.mu.Lock()
	timerCount := len(fs.deletes.tasks.scheduler.Keys())
	fs.view.overlay.mu.Unlock()
	if timerCount != 0 {
		t.Fatalf("timer count = %d, want 0", timerCount)
	}
}

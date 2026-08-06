package vfs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestVFSListSchedulerCoalescesListLoads(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newVFSListScheduler(fs)

	load, owner := scheduler.BeginListLoad("/dir", false)
	if !owner {
		t.Fatal("first load should own")
	}
	waiter, owner := scheduler.BeginListLoad("/dir", true)
	if owner {
		t.Fatal("second load should wait")
	}
	if waiter != load {
		t.Fatal("waiter did not receive existing load")
	}

	wantErr := errors.New("list failed")
	scheduler.FinishListLoad("/dir", load, []drive.Entry{{ID: "child", Name: "child.txt"}}, wantErr)
	<-waiter.done
	if !errors.Is(waiter.err, wantErr) {
		t.Fatalf("waiter err = %v, want %v", waiter.err, wantErr)
	}

	next, owner := scheduler.BeginListLoad("/dir", false)
	if !owner || next == load {
		t.Fatalf("next load owner=%v next==load=%v", owner, next == load)
	}
	scheduler.FinishListLoad("/dir", next, nil, nil)
}

func TestVFSListSchedulerTracksDirPrefetchState(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newVFSListScheduler(fs)

	if !scheduler.MarkDirPrefetch("/dir") {
		t.Fatal("first mark should schedule prefetch")
	}
	if scheduler.MarkDirPrefetch("/dir") {
		t.Fatal("in-flight mark should be rejected")
	}
	scheduler.FinishDirPrefetch("/dir")
	scheduler.MarkDirPrefetchComplete("/dir")
	if scheduler.MarkDirPrefetch("/dir") {
		t.Fatal("recently completed prefetch should be suppressed")
	}
}

func TestVFSListSchedulerUsesStartedPrefetchContext(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newVFSListScheduler(fs)
	fallback := context.Background()
	started, cancel := context.WithCancel(context.Background())

	if !scheduler.StartDirPrefetch(started) {
		t.Fatal("first prefetch start should win")
	}
	if scheduler.StartDirPrefetch(context.Background()) {
		t.Fatal("second prefetch start should be ignored")
	}
	if got := scheduler.DirPrefetchContext(fallback); got != started {
		t.Fatal("expected started prefetch context")
	}
	cancel()
	if got := scheduler.DirPrefetchContext(fallback); got != fallback {
		t.Fatal("expected fallback after started context is canceled")
	}
}

func TestVFSListSchedulerDetectsFreshListCache(t *testing.T) {
	fs, err := New(drive.NewFakeDriver(), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newVFSListScheduler(fs)
	fs.view.mu.Lock()
	fs.view.lists["/dir"] = listCacheEntry{expires: time.Now().Add(time.Minute)}
	fs.view.lists["/old"] = listCacheEntry{expires: time.Now().Add(-time.Minute)}
	fs.view.mu.Unlock()

	if !scheduler.HasFreshListCache("/dir") {
		t.Fatal("expected fresh cache")
	}
	if scheduler.HasFreshListCache("/old") {
		t.Fatal("expected stale cache")
	}
}

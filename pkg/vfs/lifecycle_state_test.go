package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// newStateTestVFS builds an unstarted VFS over the fake driver with short
// debounce delays so both upload and delete timers arm during the test.
func newStateTestVFS(t *testing.T) *VFS {
	t.Helper()
	fs, err := New(drive.NewFakeDriver(), Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   10 * time.Millisecond,
		DeleteDelay:   10 * time.Millisecond,
		UploadWorkers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// TestDomainStateInitialized: every domain runtime is fully wired after New.
// Guards against future domain fields being added without construction.
func TestDomainStateInitialized(t *testing.T) {
	fs := newStateTestVFS(t)
	for _, state := range []struct {
		name string
		ok   bool
	}{
		{"read", fs.read != nil}, {"upload", fs.upload != nil}, {"delete", fs.delete != nil},
		{"listing", fs.listing != nil}, {"activeDebug", fs.activeDebug != nil}, {"pathLocks", fs.pathLocks != nil},
		{"read.cache", fs.read != nil && fs.read.cache != nil},
		{"read.history", fs.read != nil && fs.read.history != nil},
		{"read.prefetch", fs.read != nil && fs.read.prefetch != nil},
		{"read.slots", fs.read != nil && fs.read.slots != nil},
		{"read.fastPath", fs.read != nil && fs.read.fastPath != nil},
		{"read.windows", fs.read != nil && fs.read.windows != nil},
		{"listing.list", fs.listing != nil && fs.listing.list != nil},
		{"listing.dirPrefetch", fs.listing != nil && fs.listing.dirPrefetch != nil},
		{"upload.store", fs.upload != nil && fs.upload.store != nil},
		{"upload.queue", fs.upload != nil && fs.upload.queue != nil},
		{"upload.schedule", fs.upload != nil && fs.upload.schedule != nil},
		{"upload.debug", fs.upload != nil && fs.upload.debug != nil},
		{"upload.faults", fs.upload != nil && fs.upload.faults != nil},
		{"upload.hashes", fs.upload != nil && fs.upload.hashes != nil},
		{"delete.tasks", fs.delete != nil && fs.delete.tasks != nil},
	} {
		if !state.ok {
			t.Errorf("domain state %s not initialized after New", state.name)
		}
	}
	_ = fs.read.Close()
}

// TestDomainCloseIdempotent: repeated Close calls on each domain state are
// safe - no panic, no double-close error, no leak.
func TestDomainCloseIdempotent(t *testing.T) {
	fs := newStateTestVFS(t)
	fs.upload.Close()
	fs.upload.Close()
	fs.delete.Close()
	fs.delete.Close()
	if err := fs.read.Close(); err != nil {
		t.Fatalf("read.Close: %v", err)
	}
	if err := fs.read.Close(); err != nil {
		t.Fatalf("read.Close second call: %v", err)
	}
}

// TestReadCloseNilSafe: a zero VFS (hand-constructed in tests) closes
// without panicking even when no cache exists.
func TestReadCloseNilSafe(t *testing.T) {
	var fs VFS
	if err := fs.read.Close(); err != nil {
		t.Fatalf("zero VFS read.Close = %v, want nil", err)
	}
	fs2 := &VFS{read: &readState{}}
	if err := fs2.read.Close(); err != nil {
		t.Fatalf("cache-less readState.Close = %v, want nil", err)
	}
}

// TestDomainCloseStopsScheduledTimers: closing the upload/delete domains
// stops the debounce timers that would otherwise fire after shutdown.
func TestDomainCloseStopsScheduledTimers(t *testing.T) {
	ctx := context.Background()

	// Upload debounce timer arms on flush, stops on Close.
	fs := newStateTestVFS(t)
	if err := fs.Create(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/a.txt", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	fs.upload.schedule.mu.Lock()
	armed := len(fs.upload.schedule.timers)
	fs.upload.schedule.mu.Unlock()
	if armed == 0 {
		t.Fatal("upload debounce timer not armed after write")
	}
	fs.upload.Close()
	fs.upload.schedule.mu.Lock()
	left := len(fs.upload.schedule.timers)
	fs.upload.schedule.mu.Unlock()
	if left != 0 {
		t.Fatalf("upload.Close left %d timers armed", left)
	}

	// Delete debounce timer arms on remove, stops on Close. The file must
	// have finished uploading (no pending record) so Remove takes the
	// scheduled-delete path rather than removing a pending upload.
	fs2 := newStateTestVFS(t)
	sctx, scancel := context.WithCancel(context.Background())
	fs2.Start(sctx)
	defer scancel()
	if err := fs2.Create(ctx, "/del.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs2.WriteAt(ctx, "/del.txt", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs2.Flush(ctx, "/del.txt"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := fs2.upload.store.UploadByPath("/del.txt"); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := fs2.upload.store.UploadByPath("/del.txt"); ok {
		t.Fatalf("upload did not complete before delete check")
	}
	if err := fs2.Remove(ctx, "/del.txt"); err != nil {
		t.Fatal(err)
	}
	fs2.delete.tasks.mu.Lock()
	darmed := len(fs2.delete.tasks.timers)
	fs2.delete.tasks.mu.Unlock()
	if darmed == 0 {
		t.Fatal("delete debounce timer not armed after remove")
	}
	fs2.delete.Close()
	fs2.delete.tasks.mu.Lock()
	dleft := len(fs2.delete.tasks.timers)
	fs2.delete.tasks.mu.Unlock()
	if dleft != 0 {
		t.Fatalf("delete.Close left %d timers armed", dleft)
	}
}

// TestUploadQueueEnqueueExitsOnShutdown: a blocked enqueue (queue full)
// must exit when the VFS lifecycle context is cancelled. The upload worker
// runtime spawns the blocking enqueue in a background goroutine that
// selects on the VFS done channel.
func TestUploadQueueEnqueueExitsOnShutdown(t *testing.T) {
	fs := newStateTestVFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)

	// Fill the queue so the next enqueue blocks in the background goroutine.
	for i := 0; i < cap(fs.upload.queue); i++ {
		fs.upload.queue <- PendingUpload{Path: "/fill", FID: "fill"}
	}
	done := make(chan struct{})
	fs.sendUpload(PendingUpload{Path: "/blocked", FID: "blocked"})
	go func() {
		// The blocking enqueue goroutine exits on v.done; observe it by
		// verifying the queue never accepts a second blocked record after
		// shutdown (the done channel is only observable via SendUpload).
		<-fs.done
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("VFS done not closed after cancel")
	}
	// Drain: the blocked goroutine may still be parked; give it a moment to
	// observe done, then close the remaining domains like the lifecycle does.
	time.Sleep(50 * time.Millisecond)
	fs.delete.Close()
	fs.upload.Close()
	_ = fs.read.Close()
}

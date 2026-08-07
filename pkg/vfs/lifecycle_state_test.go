package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// newStateTestVFS builds an unstarted VFS over the fake driver with stable
// debounce delays: long enough that the arming check always observes the
// timer before it can fire (10ms raced on slow -race machines), short
// enough that the started instances in this file still upload within the
// wait deadline.
func newStateTestVFS(t *testing.T) *VFS {
	t.Helper()
	fs, err := New(drive.NewFakeDriver(), Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   time.Second,
		DeleteDelay:   time.Second,
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
	deadline := time.Now().Add(5 * time.Second)
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

// TestUploadQueueEnqueueExitsOnShutdown asserts the shutdown semantics of
// a blocked enqueue directly through enqueueBlocking, using two VFS
// instances to keep the scenarios free of worker competition:
//   - unstarted VFS: done is open, so a blocked enqueue parks on the full
//     queue and delivers once space appears;
//   - started-then-cancelled VFS: workers stopped, queue stays full, so
//     the done case wins and the enqueue returns without delivering.
func TestUploadQueueEnqueueExitsOnShutdown(t *testing.T) {
	// Alive (no worker to compete for slots): blocked enqueue delivers once
	// space appears.
	alive := newStateTestVFS(t)
	aliveRuntime := newVFSUploadWorkerRuntime(alive)
	for i := 0; i < cap(alive.upload.queue); i++ {
		alive.upload.queue <- PendingUpload{Path: "/fill", FID: "fill"}
	}
	delivered := make(chan bool, 1)
	go func() { delivered <- aliveRuntime.enqueueBlocking(PendingUpload{Path: "/waited", FID: "waited"}) }()
	time.Sleep(20 * time.Millisecond) // let it park on the full queue
	<-alive.upload.queue              // make room; no worker races for it
	select {
	case ok := <-delivered:
		if !ok {
			t.Fatal("blocked enqueue reported not delivered while VFS alive")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked enqueue did not deliver after space appeared")
	}

	// Shutdown: close the lifecycle done channel (what Start's AfterFunc does
	// on cancel) while the queue is full, so only the done case is
	// satisfiable. No worker is running, so the assertion is deterministic.
	fs := newStateTestVFS(t)
	runtime := newVFSUploadWorkerRuntime(fs)
	for i := 0; i < cap(fs.upload.queue); i++ {
		fs.upload.queue <- PendingUpload{Path: "/fill", FID: "fill"}
	}
	close(fs.done)
	if runtime.enqueueBlocking(PendingUpload{Path: "/blocked", FID: "blocked"}) {
		t.Fatal("enqueue delivered after shutdown")
	}

	fs.delete.Close()
	fs.upload.Close()
	_ = fs.read.Close()
}

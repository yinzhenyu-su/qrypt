package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// newStateTestVFS builds an unstarted VFS over the fake driver with stable
// debounce delays. The only constraint on them is that the arming checks in
// this file still observe the timer before it can fire: a check runs a couple
// of calls after the flush that arms it, so the window it has to survive is a
// goroutine scheduling delay rather than microseconds - a previous 10ms value
// raced on slow -race machines. 200ms keeps 20x that margin while costing a
// fifth of the second the started instances below used to wait out.
func newStateTestVFS(t *testing.T) *VFS {
	t.Helper()
	fs, err := New(drive.NewFakeDriver(), Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 10 << 20,
		UploadDelay:   200 * time.Millisecond,
		DeleteDelay:   200 * time.Millisecond,
		UploadWorkers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for workers and journal/cache writes before TempDir cleanup.
	// Cancelling the lifecycle context alone starts asynchronous teardown.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := fs.Close(ctx); err != nil {
			t.Errorf("close filesystem: %v", err)
		}
		_ = fs.ClearReadCache()
	})
	return fs
}

// TestDomainStateInitialized: every domain runtime is fully wired after New.
// Guards against future domain fields being added without construction.
func TestDomainStateInitialized(t *testing.T) {
	t.Parallel()
	fs := newStateTestVFS(t)
	for _, state := range []struct {
		name string
		ok   bool
	}{
		{"read", fs.read != nil}, {"upload", fs.uploads != nil}, {"delete", fs.deletes != nil},
		{"listing", fs.listing != nil}, {"activeDebug", fs.activeDebug != nil}, {"pathLocks", fs.pathLocks != nil},
		{"read.cache", fs.read != nil && fs.read.Cache() != nil},
		{"read.history", fs.read != nil && fs.read.History() != nil},
		{"read.runtime", fs.read != nil && fs.read.StatesReady()},
		{"listing.states", fs.listing != nil && fs.listing.StatesReady()},
		{"upload.store", fs.uploads != nil && fs.uploads.Store() != nil},
		{"upload.queue", fs.uploads != nil && fs.uploads.Queue() != nil},
		{"upload.schedule", fs.uploads != nil && fs.uploads.ScheduledDeadlines() != nil},
		{"upload.debug", fs.uploads != nil && fs.uploads.DebugState() != nil},
		{"upload.faults", fs.faults != nil},
		{"upload.hashes", fs.uploads != nil && fs.uploads.hashes != nil},
		{"delete.state", fs.deletes != nil && fs.deletes.deleteState != nil},
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
	t.Parallel()
	fs := newStateTestVFS(t)
	fs.uploads.Close()
	fs.uploads.Close()
	fs.deletes.Close()
	fs.deletes.Close()
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
	t.Parallel()
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
	t.Parallel()
	ctx := context.Background()

	// Upload debounce timer arms on flush, stops on Close.
	fs := newStateTestVFS(t)
	uploadCtx, uploadCancel := context.WithCancel(context.Background())
	fs.Start(uploadCtx)
	defer func() {
		uploadCancel()
		_ = fs.Close(context.Background())
	}()
	if err := fs.Create(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/a.txt", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	armed := len(fs.uploads.ScheduledDeadlines())
	if armed == 0 {
		t.Fatal("upload debounce timer not armed after write")
	}
	fs.uploads.Close()
	left := len(fs.uploads.ScheduledDeadlines())
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
		if _, ok := fs2.uploads.Store().UploadByPath("/del.txt"); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := fs2.uploads.Store().UploadByPath("/del.txt"); ok {
		t.Fatalf("upload did not complete before delete check")
	}
	if err := fs2.Remove(ctx, "/del.txt"); err != nil {
		t.Fatal(err)
	}
	darmed := len(fs2.deletes.deleteState.ScheduledPaths())
	if darmed == 0 {
		t.Fatal("delete debounce timer not armed after remove")
	}
	fs2.deletes.Close()
	dleft := len(fs2.deletes.deleteState.ScheduledPaths())
	if dleft != 0 {
		t.Fatalf("delete.Close left %d timers armed", dleft)
	}
}

func TestFlushBeforeStartDefersUploadSchedulingToResume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := newStateTestVFS(t)
	if err := fs.Create(ctx, "/pending.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/pending.txt", []byte("pending"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/pending.txt"); err != nil {
		t.Fatal(err)
	}
	if got := len(fs.uploads.ScheduledDeadlines()); got != 0 {
		t.Fatalf("Flush before Start armed %d upload timers, want 0", got)
	}
	if got := len(fs.uploads.Queue()); got != 0 {
		t.Fatalf("Flush before Start enqueued %d uploads, want 0", got)
	}

	startCtx, cancel := context.WithCancel(context.Background())
	fs.Start(startCtx)
	defer func() {
		cancel()
		_ = fs.Close(context.Background())
	}()
	if got := len(fs.uploads.ScheduledDeadlines()); got != 1 {
		t.Fatalf("Start Resume armed %d upload timers, want 1", got)
	}
}

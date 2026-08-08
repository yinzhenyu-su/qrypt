package vfs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// newLifecycleVFS builds a VFS over the fake driver. The fake never starts
// background work on its own, so every goroutine observed afterwards is
// owned by the VFS lifecycle (upload workers, delete/upload timers, read
// cache writer), which is exactly the surface these tests exercise.
func newLifecycleVFS(t *testing.T, opts ...vfs.Options) *vfs.VFS {
	t.Helper()
	options := vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 10 * time.Millisecond, UploadWorkers: 1}
	if len(opts) > 0 {
		options = opts[0]
	}
	fs, err := vfs.New(drive.NewFakeDriver(), options)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// TestStartTwiceIsIdempotent: a second Start must not start a second set of
// workers, and cancelling the context must not double-close internal
// channels (which would panic). The test would fail with a panic on the
// second close(v.done) before the idempotency guard exists.
func TestStartTwiceIsIdempotent(t *testing.T) {
	fs := newLifecycleVFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	fs.Start(ctx) // no-op; must not double-start workers or double-close done
	cancel()
	stopVFS(t, fs)
}

// TestStartCancelStopsWorkers: after cancel, upload workers and timers must
// exit. goleak.VerifyTestMain runs after this test and fails the package if
// any worker outlives the cancel.
func TestStartCancelStopsWorkers(t *testing.T) {
	fs := newLifecycleVFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	// Drive some scheduled work so the timer path is warm, then cancel.
	if err := fs.Create(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/a.txt", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	cancel()
	stopVFS(t, fs)
}

// TestStartImmediateCancelNoLeak: starting and cancelling immediately must
// not leak the upload workers or the read-cache writer. The AfterFunc must
// run CloseReadCache, which waits for the cache writer to exit.
func TestStartImmediateCancelNoLeak(t *testing.T) {
	fs := newLifecycleVFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	cancel()
	stopVFS(t, fs)
}

// TestCancelIsIdempotent: cancelling the lifecycle context twice, and
// calling CloseReadCache both explicitly and through the cancel hook, must
// be safe (no double close, no panic, no leak).
func TestCancelIsIdempotent(t *testing.T) {
	fs := newLifecycleVFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	cancel()
	cancel() // context cancels are idempotent by contract
	if err := fs.CloseReadCache(); err != nil {
		t.Fatalf("CloseReadCache after cancel: %v", err)
	}
	if err := fs.CloseReadCache(); err != nil {
		t.Fatalf("CloseReadCache second call: %v", err)
	}
}

// TestNamespaceStartPropagatesLifecycle: Namespace.Start must start every
// mount's workers, and one context cancellation must stop them all. A leak
// in any mount fails goleak.VerifyTestMain.
func TestNamespaceStartPropagatesLifecycle(t *testing.T) {
	mounts := []vfs.Mount{
		{Name: "one", FS: newLifecycleVFS(t)},
		{Name: "two", FS: newLifecycleVFS(t)},
	}
	ns, err := vfs.NewNamespace(mounts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ns.Start(ctx)
	// Touch both mounts so each has live timer/upload state.
	for _, m := range []string{"/one", "/two"} {
		if err := ns.Create(ctx, m+"/f.txt"); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	stopVFS(t, ns)
	stopVFS(t, mounts[0].FS)
	stopVFS(t, mounts[1].FS)
}

// TestLifecycleStartResumeOnce: a pending upload persisted before Start must
// be resumed exactly once, even when Start is invoked twice. First, write
// and flush through an unstarted VFS so a frozen pending upload lands in the
// journal; then open a fresh VFS over the same storage, Start it twice and
// verify the fake driver sees exactly one PutSource.
func TestLifecycleStartResumeOnce(t *testing.T) {
	cache := t.TempDir()

	// Generation 1: persist a frozen pending upload without starting workers.
	first, err := vfs.New(drive.NewFakeDriver(), vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := first.Create(ctx, "/resume.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAt(ctx, "/resume.txt", []byte("resume me"), 0); err != nil {
		t.Fatal(err)
	}
	if err := first.Flush(ctx, "/resume.txt"); err != nil {
		t.Fatal(err)
	}
	stopVFS(t, first)

	// Generation 2: the same storage dir resumes the frozen pending.
	driver := drive.NewFakeDriver()
	second, err := vfs.New(driver, vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second.Start(sctx)
	second.Start(sctx) // must not resume the pending a second time
	waitNoPending(t, second)

	puts := 0
	for _, call := range driver.FakeCalls() {
		if strings.HasPrefix(call, "PutSource:") {
			puts++
		}
	}
	if puts != 1 {
		t.Fatalf("uploaded %d time(s), want exactly 1", puts)
	}
	stopVFS(t, second)
}

// TestStartContextOwnership: the first context passed to Start owns the VFS
// lifecycle. A second Start is a no-op, so cancelling the second context must
// not stop the workers the first context started. If the second Start ever
// replaced the lifecycle, the upload below would never drain.
func TestStartContextOwnership(t *testing.T) {
	fs := newLifecycleVFS(t)
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	fs.Start(ctx1)
	fs.Start(ctx2) // ignored; ctx1 owns the lifecycle
	cancel2()      // must not stop workers started under ctx1

	if err := fs.Create(ctx1, "/alive.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx1, "/alive.txt", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx1, "/alive.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs) // drains only if the ctx1 workers are still running

	cancel1()
	stopVFS(t, fs)
}

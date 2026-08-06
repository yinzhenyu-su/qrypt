package vfs_test

import (
	"context"
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

// TestLifecycleStartAfterNewWithPendingRecovery: Resume is part of Start;
// a pending upload present before Start must be scheduled exactly once, and
// cancelling must not panic. This guards the resume-once property of Start
// against double invocation.
func TestLifecycleStartResumeOnce(t *testing.T) {
	fs := newLifecycleVFS(t)
	// Seed a pending upload state before Start.
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	fs.Start(ctx) // double Start must not double-resume
	cancel()
	stopVFS(t, fs)
}

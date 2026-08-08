package vfs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// newCloseTestVFS builds a VFS over the fake driver for lifecycle tests.
func newCloseTestVFS(t *testing.T, opts ...Options) *VFS {
	t.Helper()
	options := Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 5 * time.Millisecond, UploadWorkers: 2}
	if len(opts) > 0 {
		options = opts[0]
	}
	fs, err := New(drive.NewFakeDriver(), options)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// TestNamespaceCloseWaitsForAllMounts: Namespace.Close must shut down every
// mount (workers, read-cache writer) before returning, not just cancel.
func TestNamespaceCloseWaitsForAllMounts(t *testing.T) {
	fsA := newCloseTestVFS(t)
	fsB := newCloseTestVFS(t)
	ns, err := NewNamespace([]Mount{{Name: "a", FS: fsA}, {Name: "b", FS: fsB}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ns.Start(ctx)

	// Warm the upload and read-cache paths so workers have something to tear
	// down (upload workers only start if Start ran, which it did above).
	if err := ns.Create(ctx, "/a/f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := ns.WriteAt(ctx, "/a/f.txt", []byte("payload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := ns.Flush(ctx, "/a/f.txt"); err != nil {
		t.Fatal(err)
	}
	// Let a real upload schedule fire so the worker pool is busy.
	time.Sleep(20 * time.Millisecond)

	if err := ns.Close(context.Background()); err != nil {
		t.Fatalf("Namespace.Close: %v", err)
	}
	// Both mounts must have completed teardown (closeDone closed) and have
	// no workers left.
	for name, fs := range map[string]*VFS{"a": fsA, "b": fsB} {
		select {
		case <-fs.closeDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("mount %s teardown did not complete", name)
		}
	}
	// A second Close stays idempotent.
	if err := ns.Close(context.Background()); err != nil {
		t.Fatalf("second Namespace.Close: %v", err)
	}
}

// TestNamespaceCloseCollectsErrorsAndKeepsTeardown: when Close is called
// with an already-cancelled context, every mount's Close returns ctx.Err()
// early (teardown keeps running in the background), the errors are joined,
// and a later Close with a live context still reports the completed
// teardown.
func TestNamespaceCloseCollectsErrorsAndKeepsTeardown(t *testing.T) {
	fsA := newCloseTestVFS(t)
	fsB := newCloseTestVFS(t)
	ns, err := NewNamespace([]Mount{{Name: "a", FS: fsA}, {Name: "b", FS: fsB}})
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = ns.Close(cctx)
	if err == nil {
		t.Fatal("Close with cancelled ctx: want error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with cancelled ctx: want context.Canceled, got %v", err)
	}
	// Teardown still ran to completion for both mounts.
	for name, fs := range map[string]*VFS{"a": fsA, "b": fsB} {
		select {
		case <-fs.closeDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("mount %s teardown did not complete after early-return Close", name)
		}
	}
	if err := ns.Close(context.Background()); err != nil {
		t.Fatalf("Close after completed teardown: %v", err)
	}
}

// TestStartAfterCloseIsNoop: a torn-down VFS must never start workers via a
// later Start call (the closeOnce/startOnce interaction leaves cancel nil
// and the worker group empty).
func TestStartAfterCloseIsNoop(t *testing.T) {
	fs := newCloseTestVFS(t)
	if err := fs.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	fs.Start(context.Background())
	if fs.cancel != nil {
		t.Fatal("Start after Close must not initialize the lifecycle context")
	}
	// Close stays idempotent and the instance has no workers.
	if err := fs.Close(context.Background()); err != nil {
		t.Fatalf("Close after Start-after-Close: %v", err)
	}
}

// TestCloseRacesContextCancel: an explicit Close racing the owning
// context's cancel (which triggers Close through the Start hook) must be
// safe: no double-close panics, every call returns cleanly.
func TestCloseRacesContextCancel(t *testing.T) {
	for i := 0; i < 20; i++ {
		fs := newCloseTestVFS(t)
		ctx, cancel := context.WithCancel(context.Background())
		fs.Start(ctx)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			cancel()
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_ = fs.Close(context.Background())
			}
		}()
		wg.Wait()
		// Teardown must have completed.
		select {
		case <-fs.closeDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: teardown did not complete", i)
		}
	}
}

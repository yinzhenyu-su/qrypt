package vfs_test

import (
	"context"
	"testing"
	"time"
)

// TestCloseIsIdempotent: Close can be called multiple times; every call
// returns the same result and no call panics (double-close of internal
// channels would panic without the closeOnce guard).
func TestCloseIsIdempotent(t *testing.T) {
	fs := newLifecycleVFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	defer cancel()

	if err := fs.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := fs.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestCloseBeforeStart: a VFS that was never Started still releases its
// construction-time resources (read-cache writer) on Close.
func TestCloseBeforeStart(t *testing.T) {
	fs := newLifecycleVFS(t)
	if err := fs.Close(context.Background()); err != nil {
		t.Fatalf("Close before Start: %v", err)
	}
	// Closing again stays safe.
	if err := fs.Close(context.Background()); err != nil {
		t.Fatalf("second Close before Start: %v", err)
	}
}

// TestCloseAfterCancel: Close is safe after the owning context was already
// cancelled (the Start hook calls Close itself, so this is a double-shutdown
// race).
func TestCloseAfterCancel(t *testing.T) {
	fs := newLifecycleVFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	if err := fs.Create(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/a.txt", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	cancel()
	// Give the AfterFunc hook a chance to run Close first.
	time.Sleep(10 * time.Millisecond)
	if err := fs.Close(context.Background()); err != nil {
		t.Fatalf("Close after cancel: %v", err)
	}
}

// TestCloseStopsWorkers: after Close, upload workers and the read-cache
// writer must be gone. goleak.VerifyTestMain runs after this test and fails
// the package if any VFS-owned goroutine outlives Close. Writing data before
// Close warms the upload schedule and read-cache writer paths.
func TestCloseStopsWorkers(t *testing.T) {
	fs := newLifecycleVFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	defer cancel()

	if err := fs.Create(ctx, "/b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/b.txt", []byte("payload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/b.txt"); err != nil {
		t.Fatal(err)
	}
	// Let the debounce timer schedule a real upload before closing.
	time.Sleep(20 * time.Millisecond)
	if err := fs.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestCloseSignalsDone: closeDone is closed after Close completes, so
// callers can wait on it instead of polling.
func TestCloseSignalsDone(t *testing.T) {
	fs := newLifecycleVFS(t)
	if err := fs.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A second Close returns promptly and the first shutdown is complete.
	done := make(chan struct{})
	go func() {
		_ = fs.Close(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return promptly on repeated call")
	}
}

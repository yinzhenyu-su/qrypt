package vfs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

// TestVFSDeleteCancelsPrefetchButNotForeground pins the lifecycle boundary
// behind the delete/close traffic report: invalidating a file must stop its
// speculative prefetch, while an unrelated foreground read keeps its own
// context and is only canceled by its caller.
func TestVFSDeleteCancelsPrefetchButNotForeground(t *testing.T) {
	driver := drive.NewFakeDriver()
	content := make([]byte, 4*vfsread.ChunkSize)
	// Gate only the two Ranges under test: the lookahead window of the first
	// file and the foreground Range of the second file.
	prefetchGate := make(chan struct{})
	foregroundGate := make(chan struct{})
	var prefetchOnce, foregroundOnce sync.Once
	releasePrefetch := func() { prefetchOnce.Do(func() { close(prefetchGate) }) }
	releaseForeground := func() { foregroundOnce.Do(func() { close(foregroundGate) }) }
	defer releasePrefetch()
	defer releaseForeground()
	driver.ReadBehavior.GateFunc = func(offset, _ int64) <-chan struct{} {
		switch offset {
		case vfsread.ChunkSize:
			return prefetchGate
		case 2 * vfsread.ChunkSize:
			return foregroundGate
		}
		return nil
	}
	if err := driver.Seed(map[string]string{
		"file.bin":  string(content),
		"other.bin": string(content),
	}); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	fs, err := vfs.New(driver, vfs.Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer stopVFS(t, fs)

	// The first file's read completes and leaves its speculative window gated.
	if rc, err := fs.Read(ctx, "/file.bin", 0, 1); err != nil {
		t.Fatalf("Read: %v", err)
	} else {
		_ = rc.Close()
	}
	waitForReadCalls(t, driver, 2)

	// An unrelated file's foreground read is held by its own gate.
	readCtx, readCancel := context.WithCancel(ctx)
	defer readCancel()
	readDone := make(chan error, 1)
	go func() {
		rc, err := fs.Read(readCtx, "/other.bin", 2*vfsread.ChunkSize, 1)
		if rc != nil {
			rc.Close()
		}
		readDone <- err
	}()
	waitForReadCalls(t, driver, 3)

	if err := fs.Remove(context.Background(), "/file.bin"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// The prefetch body is canceled by invalidation; the unrelated foreground
	// read stays blocked on its own context.
	deadline := time.Now().Add(2 * time.Second)
	for {
		canceled := false
		for _, call := range driver.ReadCalls() {
			if call.Offset == vfsread.ChunkSize && call.Err != nil && errors.Is(call.Err, context.Canceled) {
				canceled = true
				break
			}
		}
		if canceled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delete did not cancel the prefetch read")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-readDone:
		t.Fatalf("foreground read returned before its caller canceled it: %v", err)
	default:
	}

	readCancel()
	select {
	case err := <-readDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("foreground read error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("foreground read did not return after caller cancel")
	}
}

func waitForReadCalls(t *testing.T, driver *drive.FakeDriver, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for driver.ReadStats().Calls < want {
		if time.Now().After(deadline) {
			t.Fatalf("driver read calls = %d, want at least %d", driver.ReadStats().Calls, want)
		}
		time.Sleep(time.Millisecond)
	}
}

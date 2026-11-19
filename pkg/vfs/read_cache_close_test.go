package vfs

// Closing the read cache must not close the read path. A caller (debug
// commands, tests, mobile) may drop the cache writer and keep reading; the
// next cache-miss read starts a fresh lookahead scope and fetches from the
// provider.

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// gatedReadDriver blocks provider reads until the test releases them, so the
// lookahead scope's lifetime is observable instead of racing a local read
// that completes immediately.
type gatedReadDriver struct {
	drive.Driver
	armed   atomic.Bool
	started chan struct{}
	release chan struct{}
}

func (d *gatedReadDriver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if !d.armed.Load() {
		return d.Driver.Read(ctx, entry, offset, size)
	}
	select {
	case <-d.started:
	default:
		close(d.started)
	}
	select {
	case <-d.release:
		return d.Driver.Read(ctx, entry, offset, size)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestVFSReadsAfterCloseReadCache(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	driver := &gatedReadDriver{Driver: localfs.New(t.TempDir()), started: make(chan struct{}), release: make(chan struct{})}
	fs, err := New(driver, Options{Name: "m", StorageDir: t.TempDir(), TestEnabled: true, UploadDelay: time.Millisecond})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	fs.Start(ctx)
	t.Cleanup(func() {
		cancel()
		_ = fs.Close(context.Background())
	})

	if _, err := fs.WriteAt(ctx, "/f.txt", []byte("hello world"), 0); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := fs.Flush(ctx, "/f.txt"); err != nil {
		t.Fatalf("flush: %v", err)
	}
	waitCoverageDrained(t, fs)
	// Drop every warm chunk before closing the writer: the next read must go
	// to the provider through a fresh window fetch instead of a hot hit.
	if err := fs.ClearReadCache(); err != nil {
		t.Fatalf("clear read cache: %v", err)
	}
	if err := fs.CloseReadCache(); err != nil {
		t.Fatalf("close read cache: %v", err)
	}

	type readResult struct {
		data []byte
		err  error
	}
	results := make(chan readResult, 1)
	driver.armed.Store(true)
	go func() {
		rc, err := fs.Read(ctx, "/f.txt", 0, 0)
		if err != nil {
			results <- readResult{err: err}
			return
		}
		data, readErr := io.ReadAll(rc)
		rc.Close()
		results <- readResult{data: data, err: readErr}
	}()
	select {
	case <-driver.started:
	case res := <-results:
		t.Fatalf("read finished before reaching the provider: data=%q err=%v", res.data, res.err)
	case <-time.After(5 * time.Second):
		t.Fatal("read never reached the provider after CloseReadCache")
	}
	close(driver.release)
	res := <-results
	if res.err != nil {
		t.Fatalf("read after CloseReadCache: %v", res.err)
	}
	if string(res.data) != "hello world" {
		t.Fatalf("read after CloseReadCache = %q, want %q", res.data, "hello world")
	}
}

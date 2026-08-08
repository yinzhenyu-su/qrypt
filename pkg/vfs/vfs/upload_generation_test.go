package vfs_test

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVFSWriteAfterFlushPreservesStagedContent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &fileUploadDriver{blockFirst: make(chan struct{}), firstEntered: make(chan struct{})}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("hello world"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drv.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first upload did not start")
	}
	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("qrypt"), 6); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	close(drv.blockFirst)
	waitNoPending(t, fs)

	drv.mu.Lock()
	defer drv.mu.Unlock()
	if got := string(drv.lastData); got != "hello qrypt" {
		t.Fatalf("final upload data = %q, want %q", got, "hello qrypt")
	}
}
func TestVFSMutableGenerationIsNotUploadedWithoutFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &fileUploadDriver{blockFirst: make(chan struct{}), firstEntered: make(chan struct{})}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("hello world"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drv.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first upload did not start")
	}
	if _, err := fs.WriteAt(ctx, "/doc.txt", []byte("qrypt"), 6); err != nil {
		t.Fatal(err)
	}
	close(drv.blockFirst)

	waitForCondition(t, func() bool {
		drv.mu.Lock()
		defer drv.mu.Unlock()
		return drv.putSourceCalls == 1
	})
	time.Sleep(150 * time.Millisecond)
	drv.mu.Lock()
	calls := drv.putSourceStart
	first := string(drv.allData[0])
	drv.mu.Unlock()
	if calls != 1 {
		t.Fatalf("mutable generation uploaded without flush: putSource calls = %d, want 1", calls)
	}
	if first != "hello world" {
		t.Fatalf("first upload data = %q, want %q", first, "hello world")
	}

	if err := fs.Flush(ctx, "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	drv.mu.Lock()
	defer drv.mu.Unlock()
	if drv.putSourceStart != 2 {
		t.Fatalf("putSource calls after flush = %d, want 2", drv.putSourceStart)
	}
	if got := string(drv.lastData); got != "hello qrypt" {
		t.Fatalf("last upload data = %q, want %q", got, "hello qrypt")
	}
}
func TestVFSUploadUsesStableSnapshotWhenFileChangesDuringUpload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cacheDir := t.TempDir()
	drv := &fileUploadDriver{blockFirst: make(chan struct{}), firstEntered: make(chan struct{})}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/fast.txt", []byte("first version"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/fast.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drv.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first upload did not start")
	}
	if err := fs.Truncate(ctx, "/fast.txt", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/fast.txt", []byte("second"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/fast.txt"); err != nil {
		t.Fatal(err)
	}
	close(drv.blockFirst)
	waitNoPending(t, fs)

	drv.mu.Lock()
	if len(drv.allData) < 2 {
		t.Fatalf("uploads = %q, want superseded upload and latest upload", drv.allData)
	}
	if string(drv.allData[0]) != "first version" {
		t.Fatalf("first upload data = %q, want stable snapshot", drv.allData[0])
	}
	if string(drv.lastData) != "second" {
		t.Fatalf("last upload data = %q, want second", drv.lastData)
	}
	drv.mu.Unlock()

	stagingEntries, err := os.ReadDir(filepath.Join(cacheDir, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range stagingEntries {
		if strings.HasSuffix(entry.Name(), ".staging") {
			t.Fatalf("staging file leaked after superseded upload: %s", entry.Name())
		}
	}
}
func TestVFSUploadDoesNotClearNewerPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	drv := &countingUploadDriver{entered: entered, blockReturn: release}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("one"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	close(release)

	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 2 {
		t.Fatalf("upload count = %d, want 2", got)
	}
	if got := drv.lastUpload(); got != "two" {
		t.Fatalf("last upload = %q, want two", got)
	}
	if removed := drv.removedIDs(); len(removed) != 1 || removed[0] != "draft.txt-1" {
		t.Fatalf("removed stale uploads = %v, want [draft.txt-1]", removed)
	}
}

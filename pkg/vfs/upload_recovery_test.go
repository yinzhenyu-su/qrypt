package vfs_test

import (
	"bytes"
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVFSRecoversPendingUploads(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()

	first, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAt(ctx, "/resume.txt", []byte("resume me"), 0); err != nil {
		t.Fatal(err)
	}
	if len(first.PendingUploads()) != 1 {
		t.Fatalf("expected one pending file, got %d", len(first.PendingUploads()))
	}

	second, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	second.Start(ctx)

	// The recovered generation was never flushed, so it must stay local
	// until an explicit flush after restart.
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(remote, "resume.txt")); !os.IsNotExist(err) {
		t.Fatalf("unflushed pending uploaded after restart, stat err=%v", err)
	}
	if got := len(second.PendingUploads()); got != 1 {
		t.Fatalf("recovered pending count = %d, want 1", got)
	}

	if err := second.Flush(ctx, "/resume.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, second)

	data, err := os.ReadFile(remote + "/resume.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "resume me" {
		t.Fatalf("unexpected recovered data: %q", data)
	}
}
func TestVFSRecoversUnflushedPendingUploadSizeFromStaging(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()
	content := bytes.Repeat([]byte("x"), 2*1024*1024+123)

	first, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for off := 0; off < len(content); off += 16 * 1024 {
		end := off + 16*1024
		if end > len(content) {
			end = len(content)
		}
		if _, err := first.WriteAt(ctx, "/resume-large.bin", content[off:end], int64(off)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(first.PendingUploads()); got != 1 {
		t.Fatalf("expected one pending file, got %d", got)
	}
	journal, err := os.ReadFile(filepath.Join(cache, "pending.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(journal), "\n"); lines != 1 {
		t.Fatalf("pending journal lines after unflushed writes = %d, want 1", lines)
	}

	second, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	pending := second.PendingUploads()
	if len(pending) != 1 {
		t.Fatalf("expected one recovered pending file, got %d", len(pending))
	}
	if pending[0].Size != 0 {
		t.Fatalf("pending size before resume repair = %d, want stale journal size 0", pending[0].Size)
	}

	second.Start(ctx)

	// Size repair runs on resume, but the unflushed generation must stay
	// local until an explicit flush after restart.
	waitForCondition(t, func() bool {
		pending := second.PendingUploads()
		return len(pending) == 1 && pending[0].Size == int64(len(content))
	})
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(remote, "resume-large.bin")); !os.IsNotExist(err) {
		t.Fatalf("unflushed pending uploaded after restart, stat err=%v", err)
	}

	if err := second.Flush(ctx, "/resume-large.bin"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, second)
	data, err := os.ReadFile(filepath.Join(remote, "resume-large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("recovered data mismatch: got %d bytes, want %d", len(data), len(content))
	}
}
func TestVFSDropsPendingWhenStagingMissingOnRecovery(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()

	first, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAt(ctx, "/lost.txt", []byte("lost data"), 0); err != nil {
		t.Fatal(err)
	}
	pending := first.PendingUploads()
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	if err := os.Remove(pending[0].LocalPath); err != nil {
		t.Fatal(err)
	}

	second, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if pending := second.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending with missing staging should not recover: %+v", pending)
	}
}

package vfs_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

func newStreamTestVFS(t *testing.T) vfs.FileSystem {
	t.Helper()
	fs, err := vfs.New(drive.NewFakeDriver(), vfs.Options{
		StorageDir:    t.TempDir(),
		CacheMaxBytes: 32 << 20,
		UploadDelay:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	t.Cleanup(func() {
		cancel()
		_ = fs.CloseReadCache()
	})
	return fs
}

func writeFlushedFile(t *testing.T, fs vfs.FileSystem, path string, data []byte) {
	t.Helper()
	if err := fs.Create(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(context.Background(), path, data, 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}

func readAllStream(t *testing.T, fs vfs.FileSystem, path string) []byte {
	t.Helper()
	streamer, ok := fs.(vfs.StreamReader)
	if !ok {
		t.Fatal("filesystem does not implement StreamReader")
	}
	rc, err := streamer.ReadStream(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestReadStreamMatchesRead: streaming read returns exactly what Read
// returns for the same committed file.
func TestReadStreamMatchesRead(t *testing.T) {
	fs := newStreamTestVFS(t)
	content := strings.Repeat("stream-data-", 1000) // ~12 KiB, spans 2 chunks
	writeFlushedFile(t, fs, "/a.txt", []byte(content))

	want, err := fs.Read(context.Background(), "/a.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantData, err := io.ReadAll(want)
	want.Close()
	if err != nil {
		t.Fatal(err)
	}

	got := readAllStream(t, fs, "/a.txt")
	if string(got) != string(wantData) {
		t.Fatalf("stream mismatch: got %d bytes, want %d", len(got), len(wantData))
	}
}

// TestReadStreamLargeFile: multi-window file (several read prefetch windows)
// reads completely through the streaming path.
func TestReadStreamLargeFile(t *testing.T) {
	fs := newStreamTestVFS(t)
	// 5 MiB spans 5 chunks / 1 window; push past several windows to exercise
	// refill across window boundaries.
	content := make([]byte, 20<<20)
	for i := range content {
		content[i] = byte(i)
	}
	writeFlushedFile(t, fs, "/big.bin", content)

	got := readAllStream(t, fs, "/big.bin")
	if len(got) != len(content) {
		t.Fatalf("stream length: got %d, want %d", len(got), len(content))
	}
	for i := 0; i < len(content); i += 1 << 20 {
		if got[i] != content[i] {
			t.Fatalf("content mismatch at offset %d", i)
		}
	}
}

// TestReadStreamChunkedReads: the stream serves reads in bounded pieces; a
// caller using small buffers still receives the full content.
func TestReadStreamChunkedReads(t *testing.T) {
	fs := newStreamTestVFS(t)
	content := strings.Repeat("0123456789abcdef", 2048) // 32 KiB
	writeFlushedFile(t, fs, "/chunked.txt", []byte(content))

	streamer := fs.(vfs.StreamReader)
	rc, err := streamer.ReadStream(context.Background(), "/chunked.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	buf := make([]byte, 4096)
	var got strings.Builder
	for {
		n, err := rc.Read(buf)
		got.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got.String() != content {
		t.Fatalf("chunked read mismatch: got %d bytes, want %d", got.Len(), len(content))
	}
}

// TestReadStreamStaging: unflushed writes are served from staging, matching
// Read's behavior.
func TestReadStreamStaging(t *testing.T) {
	fs := newStreamTestVFS(t)
	if err := fs.Create(context.Background(), "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(context.Background(), "/draft.txt", []byte("draft content"), 0); err != nil {
		t.Fatal(err)
	}
	got := readAllStream(t, fs, "/draft.txt")
	if string(got) != "draft content" {
		t.Fatalf("staging stream: got %q", got)
	}
}

// TestReadStreamMissing: reading a nonexistent path reports not found.
func TestReadStreamMissing(t *testing.T) {
	fs := newStreamTestVFS(t)
	streamer := fs.(vfs.StreamReader)
	_, err := streamer.ReadStream(context.Background(), "/nope.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestReadStreamEarlyClose: closing before reading the whole file is safe
// and idempotent on the remote path (committed file; the staging path wraps
// an *os.File whose second Close errors, matching Read).
func TestReadStreamEarlyClose(t *testing.T) {
	fs := newStreamTestVFS(t)
	content := make([]byte, 4<<20)
	writeFlushedFile(t, fs, "/big.bin", content)
	waitNoPending(t, fs)

	streamer := fs.(vfs.StreamReader)
	rc, err := streamer.ReadStream(context.Background(), "/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<20)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal("double close not idempotent")
	}
}

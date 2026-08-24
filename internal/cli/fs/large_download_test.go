package fs

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestGetLargeLocalFilePreservesEveryChunk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	want := make([]byte, 8<<20)
	if _, err := rand.Read(want); err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(t.TempDir(), "cache")
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:    storage,
		CacheMaxBytes: 2 << 30,
		UploadDelay:   time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if err := fs.Create(ctx, "/large.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/large.bin", want, 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/large.bin"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, statErr := os.Stat(filepath.Join(remote, "large.bin"))
		if statErr == nil && info.Size() == int64(len(want)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if info, err := os.Stat(filepath.Join(remote, "large.bin")); err != nil || info.Size() != int64(len(want)) {
		t.Fatalf("uploaded file not ready: info=%+v err=%v", info, err)
	}
	if err := fs.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	fs, err = vfs.New(localfs.New(remote), vfs.Options{StorageDir: storage, CacheMaxBytes: 2 << 30})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.CloseReadCache()
	fs.Start(ctx)

	dest := filepath.Join(t.TempDir(), "large.downloaded.bin")
	if err := get(ctx, fs, "/large.bin", dest, false, true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("downloaded length mismatch: got %d, want %d", len(got), len(want))
	}
	if !bytes.Equal(got, want) {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("downloaded data checksum mismatch at offset %d: got=%02x want=%02x", i, got[i], want[i])
			}
		}
		t.Fatalf("downloaded data checksum mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

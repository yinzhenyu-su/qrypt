package vfs_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestEncryptedDriverRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	cache := t.TempDir()
	raw := localfs.New(remote)
	cp, err := crypt.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	drv := crypt.NewDriver(raw, cp, crypt.DriverOptions{})
	fs, err := vfs.New(drv, vfs.Options{StorageDir: cache, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/secret.txt", bytes.Repeat([]byte("a"), 80*1024), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/secret.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	rawEntries, err := raw.List(ctx, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(rawEntries) != 1 {
		t.Fatalf("expected one raw encrypted entry, got %d", len(rawEntries))
	}
	if rawEntries[0].Name == "secret.txt" {
		t.Fatal("expected encrypted filename on raw backend")
	}
	info, err := fs.Stat(ctx, "/secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 80*1024 {
		t.Fatalf("expected plaintext size, got %d", info.Size)
	}

	rc, err := fs.Read(ctx, "/secret.txt", 64*1024, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected encrypted read data: %q", data)
	}

	rawRC, err := fs.ReadRaw(ctx, "/secret.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rawRC.Close()
	rawData, err := io.ReadAll(rawRC)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(rawData, []byte(crypt.FileMagic)) {
		t.Fatal("raw read did not return encrypted file header")
	}
	if bytes.Contains(rawData, bytes.Repeat([]byte("a"), 16)) {
		t.Fatal("raw read returned plaintext")
	}
}

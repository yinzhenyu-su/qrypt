package vfs

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fakeReadWindowBackend struct {
	body       string
	readOff    int64
	readSize   int64
	cacheKey   string
	storedKeys []int64
	storedData []string
}

func (b *fakeReadWindowBackend) Read(_ context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
	b.readOff = offset
	b.readSize = size
	return io.NopCloser(strings.NewReader(b.body)), nil
}

func (b *fakeReadWindowBackend) CacheKey(drive.Entry) string {
	return b.cacheKey
}

func (b *fakeReadWindowBackend) StoreChunk(_ string, _ drive.Entry, index int64, chunk []byte) {
	b.storedKeys = append(b.storedKeys, index)
	b.storedData = append(b.storedData, string(chunk))
}

func TestFetchChunkWindowUsesBackendAndStoresChunks(t *testing.T) {
	backend := &fakeReadWindowBackend{body: "abcdefghijkl", cacheKey: "cache"}
	entry := drive.Entry{ID: "id", Size: 12, ModTime: time.Now()}
	chunks, extra, err := fetchChunkWindow(context.Background(), entry, 0, 1, backend)
	if err != nil {
		t.Fatal(err)
	}
	if backend.readOff != 0 || backend.readSize != 12 {
		t.Fatalf("read off=%d size=%d, want off=0 size=12", backend.readOff, backend.readSize)
	}
	if string(chunks[0]) != "abcdefghijkl" {
		t.Fatalf("chunk[0] = %q, want body", chunks[0])
	}
	if len(backend.storedKeys) != 1 || backend.storedKeys[0] != 0 || backend.storedData[0] != "abcdefghijkl" {
		t.Fatalf("stored keys=%v data=%v", backend.storedKeys, backend.storedData)
	}
	if extra["driver_open_ms"] == nil || extra["driver_body_ms"] == nil || extra["driver_close_ms"] == nil {
		t.Fatalf("missing timing extra: %+v", extra)
	}
}

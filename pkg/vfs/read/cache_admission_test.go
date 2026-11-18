package read

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestBulkReadSkipsDurableCache(t *testing.T) {
	cache := &recordingCache{}
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: 1, ModTime: time.Now()},
		data:     []byte("x"),
		cacheKey: "cache",
	}
	reader := newTestReader(testReaderDeps{Host: host, State: NewState(cache)})

	rc, err := reader.Read(WithReadIntent(context.Background(), ReadIntentBulk), "/file", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if cache.putCount() != 0 {
		t.Fatalf("durable cache puts = %d, want 0", cache.putCount())
	}
	if _, ok := reader.hotChunk("cache", 0); !ok {
		t.Fatal("hot chunk was not retained for the current read")
	}
}

func TestInteractiveReadWritesDurableCache(t *testing.T) {
	cache := &recordingCache{}
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: 1, ModTime: time.Now()},
		data:     []byte("x"),
		cacheKey: "cache",
	}
	reader := newTestReader(testReaderDeps{Host: host, State: NewState(cache)})

	rc, err := reader.Read(WithReadIntent(context.Background(), ReadIntentInteractive), "/file", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if cache.putCount() == 0 {
		t.Fatal("interactive read did not write the durable cache")
	}
}

func TestReadStreamBulkSkipsDurableCache(t *testing.T) {
	cache := &recordingCache{}
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: 1, ModTime: time.Now()},
		data:     []byte("x"),
		cacheKey: "cache",
	}
	reader := newTestReader(testReaderDeps{Host: host, State: NewState(cache)})

	rc, err := reader.ReadStream(WithReadIntent(context.Background(), ReadIntentBulk), "/file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if cache.putCount() != 0 {
		t.Fatalf("durable cache puts = %d, want 0", cache.putCount())
	}
}

func TestReadStreamInteractiveWritesDurableCache(t *testing.T) {
	cache := &recordingCache{}
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: 1, ModTime: time.Now()},
		data:     []byte("x"),
		cacheKey: "cache",
	}
	reader := newTestReader(testReaderDeps{Host: host, State: NewState(cache)})

	rc, err := reader.ReadStream(WithReadIntent(context.Background(), ReadIntentInteractive), "/file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if cache.putCount() == 0 {
		t.Fatal("interactive stream did not write the durable cache")
	}
}

func TestStoreChunkHonorsCacheWritePlan(t *testing.T) {
	cache := &recordingCache{}
	reader := newTestReader(testReaderDeps{Host: stubHost{}, State: NewState(cache)})
	backend := reader.newBackend()

	ctx := WithReadPlan(context.Background(), ReadPlan{Intent: ReadIntentBulk})
	backend.StoreChunk(ctx, "cache-key", drive.Entry{ID: "id"}, 0, []byte("chunk"))
	if cache.putCount() != 0 {
		t.Fatalf("durable cache puts = %d, want 0", cache.putCount())
	}
	if _, ok := reader.hotChunk("cache-key", 0); !ok {
		t.Fatal("hot chunk missing after a no-durable write")
	}

	backend.StoreChunk(context.Background(), "cache-key", drive.Entry{ID: "id"}, 1, []byte("chunk"))
	if cache.putCount() != 1 {
		t.Fatalf("durable cache puts = %d, want 1", cache.putCount())
	}
}

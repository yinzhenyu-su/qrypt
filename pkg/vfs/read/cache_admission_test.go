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
	reader := NewReader(ReaderDeps{Host: host, State: NewState(cache)})

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
	if len(cache.puts) != 0 {
		t.Fatalf("durable cache puts = %d, want 0", len(cache.puts))
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
	reader := NewReader(ReaderDeps{Host: host, State: NewState(cache)})

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
	if len(cache.puts) == 0 {
		t.Fatal("interactive read did not write the durable cache")
	}
}

func TestProbeReadSkipsDurableCache(t *testing.T) {
	cache := &recordingCache{}
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: 1, ModTime: time.Now()},
		data:     []byte("x"),
		cacheKey: "cache",
	}
	reader := NewReader(ReaderDeps{Host: host, State: NewState(cache)})

	rc, err := reader.Read(WithReadIntent(context.Background(), ReadIntentProbe), "/file", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if len(cache.puts) != 0 {
		t.Fatalf("durable cache puts = %d, want 0", len(cache.puts))
	}
}

func TestReadStreamBulkSkipsDurableCache(t *testing.T) {
	cache := &recordingCache{}
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: 1, ModTime: time.Now()},
		data:     []byte("x"),
		cacheKey: "cache",
	}
	reader := NewReader(ReaderDeps{Host: host, State: NewState(cache)})

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
	if len(cache.puts) != 0 {
		t.Fatalf("durable cache puts = %d, want 0", len(cache.puts))
	}
}

func TestStoreChunkHonorsCacheWritePlan(t *testing.T) {
	cache := &recordingCache{}
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(cache)})
	backend := reader.newBackend()

	ctx := WithReadPlan(context.Background(), readPlan{cacheWrites: false})
	backend.StoreChunk(ctx, "cache-key", drive.Entry{ID: "id"}, 0, []byte("chunk"))
	if len(cache.puts) != 0 {
		t.Fatalf("durable cache puts = %d, want 0", len(cache.puts))
	}
	if _, ok := reader.hotChunk("cache-key", 0); !ok {
		t.Fatal("hot chunk missing after a no-durable write")
	}

	backend.StoreChunk(context.Background(), "cache-key", drive.Entry{ID: "id"}, 1, []byte("chunk"))
	if len(cache.puts) != 1 {
		t.Fatalf("durable cache puts = %d, want 1", len(cache.puts))
	}
}

package read

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestCachePolicyAdmit(t *testing.T) {
	cache := &recordingCache{}
	state := NewState(cache)
	policy := newCachePolicy(state)
	entry := drive.Entry{ID: "id", Size: 1}

	policy.Admit(WithReadPlan(context.Background(), ReadPlan{CacheWrites: false}), entry, "cache", 0, []byte("chunk"))
	if len(cache.puts) != 0 {
		t.Fatalf("durable puts = %d, want 0", len(cache.puts))
	}
	if _, ok := state.hotChunk("cache", 0); !ok {
		t.Fatal("hot chunk missing for a no-durable admit")
	}

	policy.Admit(WithReadPlan(context.Background(), ReadPlan{CacheWrites: true}), entry, "cache", 1, []byte("chunk"))
	if len(cache.puts) != 1 {
		t.Fatalf("durable puts = %d, want 1", len(cache.puts))
	}

	policy.Admit(context.Background(), entry, "cache", 2, []byte("chunk"))
	if len(cache.puts) != 2 {
		t.Fatalf("durable puts without a plan = %d, want 2", len(cache.puts))
	}

	policy.Admit(WithReadPlan(context.Background(), ReadPlan{CacheWrites: true}), entry, "", 3, []byte("chunk"))
	if len(cache.puts) != 2 {
		t.Fatalf("empty cache key wrote durable cache: %d", len(cache.puts))
	}
}

func TestParallelCacheAdmissionKeepsHotOnly(t *testing.T) {
	data := bytes.Repeat([]byte("a"), ChunkSize*4)
	cache := &recordingCache{}
	host := &planCaptureHost{
		entry:    drive.Entry{ID: "id", Size: int64(len(data)), ModTime: time.Now()},
		data:     data,
		cacheKey: "cache",
	}
	profile := drive.DefaultReadProfile()
	profile.BulkStreams = 2
	profile.ParallelThreshold = 1
	reader := NewReader(ReaderDeps{Host: host, State: NewState(cache), Profile: profile})

	ctx := WithoutReadPrefetch(WithReadIntent(context.Background(), ReadIntentBulk))
	rc, err := reader.Read(ctx, "/file", 0, int64(len(data)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if len(cache.puts) != 0 {
		t.Fatalf("parallel bulk wrote durable cache %d times, want 0", len(cache.puts))
	}
	for index := int64(0); index < 4; index++ {
		if _, ok := reader.hotChunk("cache", index); !ok {
			t.Fatalf("hot chunk %d missing after a parallel bulk read", index)
		}
	}
}

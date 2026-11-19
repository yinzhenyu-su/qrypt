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

	policy.Admit(WithReadPlan(context.Background(), ReadPlan{Intent: ReadIntentBulk}), entry, "cache", 0, []byte("chunk"))
	if cache.putCount() != 0 {
		t.Fatalf("durable puts = %d, want 0", cache.putCount())
	}
	if _, ok := state.hotChunk("cache", 0); !ok {
		t.Fatal("hot chunk missing for a no-durable admit")
	}

	policy.Admit(WithReadPlan(context.Background(), ReadPlan{Intent: ReadIntentInteractive}), entry, "cache", 1, []byte("chunk"))
	if cache.putCount() != 1 {
		t.Fatalf("durable puts = %d, want 1", cache.putCount())
	}

	policy.Admit(context.Background(), entry, "cache", 2, []byte("chunk"))
	if cache.putCount() != 2 {
		t.Fatalf("durable puts without a plan = %d, want 2", cache.putCount())
	}

	policy.Admit(WithReadPlan(context.Background(), ReadPlan{Intent: ReadIntentInteractive}), entry, "", 3, []byte("chunk"))
	if cache.putCount() != 2 {
		t.Fatalf("empty cache key wrote durable cache: %d", cache.putCount())
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
	profile := drive.DefaultReadPolicy()
	profile.Bulk.Streams = 2
	profile.Bulk.MinParallelBytes = 0
	reader := newTestReader(testReaderDeps{Host: host, State: NewState(cache), Profile: profile})

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

	if cache.putCount() != 0 {
		t.Fatalf("parallel bulk wrote durable cache %d times, want 0", cache.putCount())
	}
	for index := int64(0); index < 4; index++ {
		if _, ok := reader.hotChunk("cache", index); !ok {
			t.Fatalf("hot chunk %d missing after a parallel bulk read", index)
		}
	}
}

package fs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunDownloadWorkersBoundsConcurrency(t *testing.T) {
	items := make([]downloadItem, 12)
	var active atomic.Int32
	var maxActive atomic.Int32
	var mu sync.Mutex

	err := runDownloadWorkers(context.Background(), items, 4, func(context.Context, downloadItem) error {
		current := active.Add(1)
		defer active.Add(-1)
		mu.Lock()
		if current > maxActive.Load() {
			maxActive.Store(current)
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := maxActive.Load(); got > 4 {
		t.Fatalf("max concurrency = %d, want at most 4", got)
	}
}

func TestRunDownloadWorkersStopsAfterCancellation(t *testing.T) {
	items := make([]downloadItem, 20)
	ctx, cancel := context.WithCancel(context.Background())
	var started atomic.Int32

	err := runDownloadWorkers(ctx, items, 4, func(ctx context.Context, _ downloadItem) error {
		if started.Add(1) == 1 {
			cancel()
		}
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if got := started.Load(); got > 4 {
		t.Fatalf("started = %d, want at most one worker pool", got)
	}
}

package read

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestReadSlotReleaseMatchesAcquiredSlot(t *testing.T) {
	state := NewState(nil)
	state.slots = &slotState{
		normal: make(chan struct{}, 1),
		high:   make(chan struct{}, 1),
	}
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: state})

	releaseHighNormalSlot, err := reader.acquireReadSlot(WithPriority(context.Background(), PriorityHigh))
	if err != nil {
		t.Fatal(err)
	}
	releaseHighReserveSlot, err := reader.acquireReadSlot(WithPriority(context.Background(), PriorityHigh))
	if err != nil {
		t.Fatal(err)
	}

	releaseHighNormalSlot()
	releaseNormalSlot, err := reader.acquireReadSlot(context.Background())
	if err != nil {
		t.Fatalf("normal read could not reuse released normal slot: %v", err)
	}
	defer releaseNormalSlot()

	releaseHighReserveSlot()
	releaseHighSlot, err := reader.acquireReadSlot(WithPriority(context.Background(), PriorityHigh))
	if err != nil {
		t.Fatalf("high-priority read could not reuse released high slot: %v", err)
	}
	defer releaseHighSlot()
}

func TestLoadWindowSlotFailurePropagatesToWaiter(t *testing.T) {
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(nil)})
	for i := 0; i < cap(reader.state.slots.normal); i++ {
		reader.state.slots.normal <- struct{}{}
	}

	ctx := WithPriority(context.Background(), PriorityLow)
	entry := drive.Entry{ID: "file", Name: "file.bin", Size: ChunkSize, ModTime: time.Unix(1, 0)}
	if _, err := reader.loadWindow(ctx, entry, 0, 1); err == nil || err.Error() != "vfs: read slots full" {
		t.Fatalf("loadWindow err = %v, want read slots full", err)
	}
}

func TestWaitWindowTreatsCanceledLoadAsRetryableMiss(t *testing.T) {
	state := NewState(nil)
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: state})
	load := &windowLoad{
		fid:  "file",
		done: make(chan struct{}),
		err:  context.Canceled,
	}
	close(load.done)
	state.windows.loads[WindowKey("file", 0, 0)] = load

	data, ok, err := reader.waitWindow(context.Background(), "file", 0)
	if err != nil {
		t.Fatalf("waitWindow err = %v, want nil so caller retries canceled load", err)
	}
	if ok {
		t.Fatal("waitWindow ok = true, want false for canceled in-flight load")
	}
	if data != nil {
		t.Fatalf("waitWindow data len = %d, want nil", len(data))
	}
}

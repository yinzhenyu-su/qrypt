package read

import (
	"context"
	"testing"
	"time"
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

func TestLowPriorityReadWaitsForSlotAndHonorsCancellation(t *testing.T) {
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(nil)})
	for i := 0; i < cap(reader.state.slots.normal); i++ {
		reader.state.slots.normal <- struct{}{}
	}

	ctx, cancel := context.WithCancel(WithPriority(context.Background(), PriorityLow))
	result := make(chan error, 1)
	go func() {
		_, err := reader.acquireReadSlot(ctx)
		result <- err
	}()

	select {
	case err := <-result:
		t.Fatalf("low-priority read returned before slot release: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	if err := <-result; err != context.Canceled {
		t.Fatalf("acquireReadSlot err = %v, want context canceled", err)
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

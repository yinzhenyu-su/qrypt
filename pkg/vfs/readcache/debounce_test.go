package readcache

import (
	"context"
	"crypto/sha256"
	"strings"
	"sync"
	"testing"
	"time"

	testclock "github.com/yinzhenyu/qrypt/pkg/clock"
)

// fakeDebouncer is a deterministic Debouncer for tests: it records arms
// and lets tests fire the pending callback manually (no real time).
type fakeDebouncer struct {
	mu       sync.Mutex
	armed    bool
	fn       func()
	armCount int
}

func (f *fakeDebouncer) Arm(delay time.Duration, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.armed {
		return
	}
	f.armCount++
	f.armed = true
	f.fn = fn
}

func (f *fakeDebouncer) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.armed = false
	f.fn = nil
}

func (f *fakeDebouncer) Armed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.armed
}

func (f *fakeDebouncer) fire(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	fn := f.fn
	f.armed = false
	f.fn = nil
	f.mu.Unlock()
	if fn == nil {
		t.Fatal("no pending debounce fire")
	}
	fn()
}

// TestStoreScheduleReadIndexSaveDebounces verifies the debouncer seam:
// repeated schedule calls arm once, and the fired callback flushes the
// dirty index without any real-time wait.
func TestStoreScheduleReadIndexSaveDebounces(t *testing.T) {
	t.Parallel()
	store, err := NewStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeDebouncer{}
	store.debounce = fake

	store.scheduleReadIndexSave()
	store.scheduleReadIndexSave()
	store.scheduleReadIndexSave()
	if fake.armCount != 1 {
		t.Fatalf("arm count = %d, want 1 (debounced)", fake.armCount)
	}
	if !store.readIndexDirty {
		t.Fatal("index should be dirty before flush")
	}

	fake.fire(t)
	// The fire runs FlushReadIndex; the debouncer is unarmed afterwards.
	if fake.Armed() {
		t.Fatal("debouncer still armed after fire")
	}

	store.debounce.Cancel()
	if fake.Armed() {
		t.Fatal("debouncer armed after cancel")
	}
}

// TestStoreDebouncerSurvivesRepeatedFlush: a flush during an armed state
// cancels the pending fire (no double flush).
func TestStoreDebouncerSurvivesRepeatedFlush(t *testing.T) {
	t.Parallel()
	store, err := NewStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeDebouncer{}
	store.debounce = fake

	store.scheduleReadIndexSave()
	if err := store.FlushReadIndex(); err != nil {
		t.Fatal(err)
	}
	if fake.Armed() {
		t.Fatal("flush left debouncer armed")
	}
}

// TestClockDebouncerFiresOnAdvance proves the clock seam used by the index
// save: arming schedules one fake-clock timer, and advancing to the delay runs
// the callback without real time passing.
func TestClockDebouncerFiresOnAdvance(t *testing.T) {
	t.Parallel()
	clk := testclock.NewFake(time.Unix(1_700_000_000, 0))
	d := newClockDebouncer(clk)
	fired := make(chan struct{})
	d.Arm(readCacheIndexSaveDelay, func() { close(fired) })
	if !d.Armed() {
		t.Fatal("debouncer not armed after Arm")
	}
	if got := clk.Pending(); got != 1 {
		t.Fatalf("fake clock pending timers = %d, want 1", got)
	}
	clk.Advance(readCacheIndexSaveDelay)
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("armed callback did not fire after Advance")
	}
	if d.Armed() {
		t.Fatal("debouncer still armed after firing")
	}
}

// TestStoreUseClockForTestWiresFakeDebouncer checks tests can swap the index
// save debouncer for the clock-driven one.
func TestStoreUseClockForTestWiresFakeDebouncer(t *testing.T) {
	t.Parallel()
	store, err := NewStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.useClockForTest(testclock.NewFake(time.Unix(1_700_000_000, 0)))
	if _, ok := store.debounce.(*clockDebouncer); !ok {
		t.Fatalf("debouncer = %T, want the clock-driven implementation", store.debounce)
	}
	store.scheduleReadIndexSave()
	if !store.debounce.Armed() {
		t.Fatal("index save was not armed on the clock debouncer")
	}
}

// TestStoreFlushPersistsPendingAsyncWrite covers Flush: it waits for the async
// chunk write and saves the dirty index, so a reopened store sees the chunk.
func TestStoreFlushPersistsPendingAsyncWrite(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	key := strings.Repeat("a", sha256.Size*2)
	cache := newTestStore(t, cacheDir, 10<<20)
	cache.PutChunkAsync(key, int64(len("flushed")), 0, []byte("flushed"), testAccess)
	if err := cache.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	reopened := newTestStore(t, cacheDir, 10<<20)
	got, ok, err := reopened.GetChunk(key, 0, testAccess)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(got) != "flushed" {
		t.Fatalf("flushed chunk = %q ok=%v, want flushed", got, ok)
	}
}

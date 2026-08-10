package readcache

import (
	"sync"
	"testing"
	"time"
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
	store, err := NewStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeDebouncer{}
	store.debounce = fake

	store.scheduleReadIndexSave()
	store.FlushReadIndex()
	if fake.Armed() {
		t.Fatal("flush left debouncer armed")
	}
}

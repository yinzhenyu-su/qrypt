package upload

import (
	"sync"
	"testing"
	"time"
)

// fakeKeyedScheduler is a deterministic KeyedScheduler for tests: it
// records schedules/cancels and fires callbacks manually, so scheduling
// tests need no real time and no time.Sleep.
type fakeKeyedScheduler struct {
	mu      sync.Mutex
	sched   map[string]time.Time
	callers map[string]func()
	fired   []string
	order   []string
}

func newFakeKeyedScheduler() *fakeKeyedScheduler {
	return &fakeKeyedScheduler{sched: map[string]time.Time{}, callers: map[string]func(){}}
}

func (f *fakeKeyedScheduler) Schedule(key string, delay time.Duration, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sched[key] = time.Now().Add(delay)
	f.callers[key] = fn
	f.order = append(f.order, "schedule:"+key)
}

func (f *fakeKeyedScheduler) Cancel(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sched, key)
	delete(f.callers, key)
	f.order = append(f.order, "cancel:"+key)
}

func (f *fakeKeyedScheduler) CancelUnder(prefix string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.sched {
		if k == prefix || isPathUnder(k, prefix) {
			delete(f.sched, k)
			delete(f.callers, k)
			f.order = append(f.order, "cancel-under:"+k)
		}
	}
}

func (f *fakeKeyedScheduler) CancelAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.sched {
		delete(f.sched, k)
		delete(f.callers, k)
	}
	f.order = append(f.order, "cancel-all")
}

func (f *fakeKeyedScheduler) Keys() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for k := range f.sched {
		out[k] = true
	}
	return out
}

func (f *fakeKeyedScheduler) Deadlines() map[string]time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]time.Time{}
	for k, d := range f.sched {
		out[k] = d
	}
	return out
}

// fire runs the callback scheduled for key and removes it (deterministic
// fire, no real timers).
func (f *fakeKeyedScheduler) fire(t *testing.T, key string) {
	t.Helper()
	f.mu.Lock()
	fn, ok := f.callers[key]
	if !ok {
		f.mu.Unlock()
		t.Fatalf("no callback scheduled for %q", key)
	}
	delete(f.sched, key)
	delete(f.callers, key)
	f.fired = append(f.fired, key)
	f.order = append(f.order, "fire:"+key)
	f.mu.Unlock()
	fn()
}

func TestServiceSchedulesAndCancelsUploads(t *testing.T) {
	store := newPendingStoreFixture(t)
	fake := newFakeKeyedScheduler()
	svc := NewService(ServiceOptions{
		UploadDelay: time.Hour,
		Store:       store,
		Done:        make(chan struct{}),
		Scheduler:   fake,
	})

	svc.EnqueueAfter(PendingUpload{Path: "/a.txt", FID: "a"}, time.Hour)
	svc.EnqueueAfter(PendingUpload{Path: "/b.txt", FID: "b"}, time.Hour)
	if got := fake.Keys(); len(got) != 2 {
		t.Fatalf("scheduled keys = %+v", got)
	}

	// Re-enqueue of an existing path reschedules (one key, still 2).
	svc.EnqueueAfter(PendingUpload{Path: "/a.txt", FID: "a2"}, time.Hour)
	if got := fake.Keys(); len(got) != 2 {
		t.Fatalf("keys after reschedule = %+v", got)
	}

	// CancelUpload removes the key.
	svc.CancelUpload("/b.txt")
	if _, ok := fake.Keys()["/b.txt"]; ok {
		t.Fatal("b.txt still scheduled after cancel")
	}

	// CancelChildUploads removes subtree keys.
	svc.EnqueueAfter(PendingUpload{Path: "/dir/x.txt", FID: "x"}, time.Hour)
	svc.CancelChildUploads("/dir")
	if _, ok := fake.Keys()["/dir/x.txt"]; ok {
		t.Fatal("dir/x.txt still scheduled after cancel children")
	}

	svc.Close()
}

func TestServiceScheduleFiresUpload(t *testing.T) {
	store := newPendingStoreFixture(t)
	fake := newFakeKeyedScheduler()
	svc := NewService(ServiceOptions{
		UploadDelay: time.Hour,
		Store:       store,
		Done:        make(chan struct{}),
		Scheduler:   fake,
	})
	defer svc.Close()

	svc.EnqueueAfter(PendingUpload{Path: "/a.txt", FID: "a"}, time.Hour)
	fake.fire(t, "/a.txt")

	// The fired callback enqueues the record on the worker queue.
	select {
	case got := <-svc.Queue():
		if got.Path != "/a.txt" {
			t.Fatalf("enqueued path = %q", got.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("upload never enqueued after scheduler fire")
	}
	if got := fake.Keys(); len(got) != 0 {
		t.Fatalf("keys after fire = %+v", got)
	}
}

func TestServiceCancelAllOnClose(t *testing.T) {
	store := newPendingStoreFixture(t)
	fake := newFakeKeyedScheduler()
	svc := NewService(ServiceOptions{
		UploadDelay: time.Hour,
		Store:       store,
		Done:        make(chan struct{}),
		Scheduler:   fake,
	})
	svc.EnqueueAfter(PendingUpload{Path: "/a.txt", FID: "a"}, time.Hour)
	svc.EnqueueAfter(PendingUpload{Path: "/b.txt", FID: "b"}, time.Hour)
	svc.Close()
	if got := fake.Keys(); len(got) != 0 {
		t.Fatalf("keys after close = %+v", got)
	}
	if got := fake.fired; len(got) != 0 {
		t.Fatalf("callbacks fired after close = %+v", got)
	}
}

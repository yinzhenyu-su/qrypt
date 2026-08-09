package upload

import (
	"sync"
	"time"
)

// KeyedScheduler schedules per-key debounce callbacks. Scheduling a key
// replaces any pending callback for that key (no fire for the old one);
// Cancel removes a key; CancelAll stops everything. The interface is the
// consumer-side seam: production uses the real timer implementation, tests
// inject a fake to make scheduling deterministic without real time.
type KeyedScheduler interface {
	// Schedule replaces any pending callback for key and fires fn after
	// delay.
	Schedule(key string, delay time.Duration, fn func())
	// Cancel removes the pending callback for key (no-op when none).
	Cancel(key string)
	// CancelUnder removes all keys equal to key or under it.
	CancelUnder(key string)
	// CancelAll removes all pending callbacks and stops their timers.
	CancelAll()
	// Keys returns the currently scheduled keys.
	Keys() map[string]bool
	// Deadlines returns the scheduled fire deadline per key.
	Deadlines() map[string]time.Time
}

// NewTimeKeyedScheduler returns a real timer-backed KeyedScheduler.
// pkg/vfs uses it for the delete-domain task scheduler.
func NewTimeKeyedScheduler() KeyedScheduler { return newTimeKeyedScheduler() }

// timeKeyedScheduler is the real-time implementation.
type timeKeyedScheduler struct {
	mu        sync.Mutex
	timers    map[string]*time.Timer
	deadlines map[string]time.Time
}

func newTimeKeyedScheduler() *timeKeyedScheduler {
	return &timeKeyedScheduler{
		timers:    map[string]*time.Timer{},
		deadlines: map[string]time.Time{},
	}
}

func (s *timeKeyedScheduler) Schedule(key string, delay time.Duration, fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.timers[key]; t != nil {
		t.Stop()
	}
	s.deadlines[key] = time.Now().Add(delay)
	s.timers[key] = time.AfterFunc(delay, func() {
		s.mu.Lock()
		delete(s.timers, key)
		delete(s.deadlines, key)
		s.mu.Unlock()
		fn()
	})
}

func (s *timeKeyedScheduler) Cancel(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.timers[key]; t != nil {
		t.Stop()
		delete(s.timers, key)
	}
	delete(s.deadlines, key)
}

func (s *timeKeyedScheduler) CancelUnder(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.timers {
		if k == prefix || isPathUnder(k, prefix) {
			s.timers[k].Stop()
			delete(s.timers, k)
			delete(s.deadlines, k)
		}
	}
}

func (s *timeKeyedScheduler) CancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.timers {
		t.Stop()
		delete(s.timers, k)
	}
	s.deadlines = map[string]time.Time{}
}

func (s *timeKeyedScheduler) Keys() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.timers))
	for k := range s.timers {
		out[k] = true
	}
	return out
}

func (s *timeKeyedScheduler) Deadlines() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]time.Time, len(s.deadlines))
	for k, d := range s.deadlines {
		out[k] = d
	}
	return out
}

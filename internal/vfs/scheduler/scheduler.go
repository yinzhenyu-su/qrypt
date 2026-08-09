// Package scheduler provides the shared real-time keyed scheduler used by
// the upload and delete domains. Each domain keeps its own consumer-side
// interface; this package owns only the neutral timer-backed
// implementation plus the interface shape both domains share.
//
// Concurrency contract: Schedule(key) replaces any pending callback for
// key, and a replaced/cancelled callback is guaranteed never to run.
// Cancel only guarantees that callbacks which have NOT started running
// will not run; a callback already past the generation check may still be
// executing when Cancel returns (same contract as time.Timer).
package scheduler

import (
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
)

// KeyedScheduler schedules per-key debounce callbacks. Scheduling a key
// replaces any pending callback for that key (the old callback is
// guaranteed not to fire); Cancel removes a key; CancelAll stops
// everything.
type KeyedScheduler interface {
	// Schedule replaces any pending callback for key and fires fn after
	// delay. A callback that was replaced or cancelled is guaranteed not
	// to run (generation-checked).
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

// scheduledTask is one generation of a key's callback. The task pointer
// itself is the generation token: a fired callback only deletes state and
// runs fn when it is still the current task for its key, so a stale
// callback from a replaced/cancelled generation can never delete the new
// task's state or execute its own superseded work.
type scheduledTask struct {
	timer      *time.Timer
	generation uint64
	deadline   time.Time
}

// timeKeyedScheduler is the real-time implementation.
type timeKeyedScheduler struct {
	mu      sync.Mutex
	tasks   map[string]*scheduledTask
	nextGen uint64
}

// NewTimeKeyedScheduler returns a real timer-backed KeyedScheduler.
func NewTimeKeyedScheduler() KeyedScheduler {
	return &timeKeyedScheduler{tasks: map[string]*scheduledTask{}}
}

func (s *timeKeyedScheduler) Schedule(key string, delay time.Duration, fn func()) {
	s.mu.Lock()
	gen := s.nextGen
	s.nextGen++
	if t := s.tasks[key]; t != nil {
		t.timer.Stop()
	}
	task := &scheduledTask{generation: gen, deadline: time.Now().Add(delay)}
	// timer is assigned under the lock so a concurrent Cancel can never
	// observe a task whose timer is still nil.
	task.timer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if s.tasks[key] != task {
			// This generation was replaced or cancelled: never run.
			s.mu.Unlock()
			return
		}
		delete(s.tasks, key)
		s.mu.Unlock()
		fn()
	})
	s.tasks[key] = task
	s.mu.Unlock()
}

func (s *timeKeyedScheduler) Cancel(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.tasks[key]; t != nil {
		t.timer.Stop()
		delete(s.tasks, key)
	}
}

func (s *timeKeyedScheduler) CancelUnder(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.tasks {
		if vfstypes.IsPathUnder(k, prefix) {
			s.tasks[k].timer.Stop()
			delete(s.tasks, k)
		}
	}
}

func (s *timeKeyedScheduler) CancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.tasks {
		t.timer.Stop()
		delete(s.tasks, k)
	}
}

func (s *timeKeyedScheduler) Keys() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.tasks))
	for k := range s.tasks {
		out[k] = true
	}
	return out
}

func (s *timeKeyedScheduler) Deadlines() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]time.Time, len(s.tasks))
	for k, t := range s.tasks {
		out[k] = t.deadline
	}
	return out
}

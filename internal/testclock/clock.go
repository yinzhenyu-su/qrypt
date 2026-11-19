// Package testclock provides the time seam used by code with real timeouts,
// backoffs and debounces.
//
// Production code defaults to Real, which is time.Now/time.After. Tests that
// exercise a timeout path install a Fake and advance it instead of sleeping, so
// a 30s fetch timeout or a 200ms retry backoff costs microseconds. The seam is
// deliberately tiny: callers only need Now and After, which keeps the
// production behavior identical to using the time package directly.
package testclock

import (
	"sync"
	"time"
)

// Clock is the subset of the time package the seam exposes.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// After waits for the duration to elapse and then sends the current time
	// on the returned channel. It is the fake-clock-aware equivalent of
	// time.After.
	After(time.Duration) <-chan time.Time
}

// Real is the production clock. It behaves exactly like the time package.
var Real Clock = realClock{}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Fake is a manually advanced clock. Its zero value is ready to use and starts
// at the zero time; use NewFake to start at a concrete instant.
//
// Timers are fired synchronously by Advance in deadline order. Each returned
// channel is buffered, so a timer that fires while nobody is receiving is still
// observed by the next receive; that mirrors time.After's delivery semantics
// closely enough for tests that assert on timeout paths.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	nextID int64
	timers map[int64]*fakeTimer
}

type fakeTimer struct {
	id    int64
	when  time.Time
	ch    chan time.Time
	fired bool
}

// NewFake returns a fake clock positioned at start.
func NewFake(start time.Time) *Fake {
	f := &Fake{now: start, timers: map[int64]*fakeTimer{}}
	return f
}

// Now reports the fake clock's current instant.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After returns a channel that fires when the fake clock reaches now+d.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timers == nil {
		f.timers = map[int64]*fakeTimer{}
	}
	f.nextID++
	t := &fakeTimer{id: f.nextID, when: f.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.fired = true
		t.ch <- f.now
		return t.ch
	}
	f.timers[t.id] = t
	return t.ch
}

// Advance moves the fake clock forward by d, firing every timer that becomes
// due, in deadline order.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	target := f.now
	if d > 0 {
		target = f.now.Add(d)
	}
	for {
		var next *fakeTimer
		for _, t := range f.timers {
			if !t.when.After(target) && (next == nil || t.when.Before(next.when) || t.when.Equal(next.when) && t.id < next.id) {
				next = t
			}
		}
		if next == nil {
			break
		}
		delete(f.timers, next.id)
		next.fired = true
		if next.when.After(f.now) {
			f.now = next.when
		}
		next.ch <- f.now
	}
	if target.After(f.now) {
		f.now = target
	}
	f.mu.Unlock()
}

// Pending reports how many timers have not fired yet.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

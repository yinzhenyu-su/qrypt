package readcache

import (
	"sync"
	"time"
)

// Debouncer schedules a single delayed callback. Arm while already armed
// is a no-op (one pending fire per debouncer); the fire clears the armed
// state before running fn. Cancel stops a pending fire. This is the
// consumer-side seam for the read-cache index flush: production uses the
// real timer implementation, tests inject a fake for deterministic
// flush-scheduling without real time.
type Debouncer interface {
	// Arm schedules fn after delay unless already armed.
	Arm(delay time.Duration, fn func())
	// Cancel stops a pending fire (no-op when none).
	Cancel()
	// Armed reports whether a fire is pending.
	Armed() bool
}

// timeDebouncer is the real-time implementation.
type timeDebouncer struct {
	mu    sync.Mutex
	timer *time.Timer
}

func newTimeDebouncer() *timeDebouncer {
	return &timeDebouncer{}
}

func (d *timeDebouncer) Arm(delay time.Duration, fn func()) {
	d.mu.Lock()
	if d.timer != nil {
		d.mu.Unlock()
		return
	}
	d.timer = time.AfterFunc(delay, func() {
		d.mu.Lock()
		d.timer = nil
		d.mu.Unlock()
		fn()
	})
	d.mu.Unlock()
}

func (d *timeDebouncer) Cancel() {
	d.mu.Lock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.mu.Unlock()
}

func (d *timeDebouncer) Armed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.timer != nil
}

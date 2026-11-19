package readcache

import (
	"context"
	"sync"
	"time"

	testclock "github.com/yinzhenyu/qrypt/pkg/clock"
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

// clockDebouncer schedules through the controllable clock seam. Tests install
// one so a pending index save fires on Advance instead of after a real delay.
// Canceling cannot stop an already-scheduled fake timer; the generation guard
// makes the stale fire a no-op.
type clockDebouncer struct {
	mu    sync.Mutex
	clk   testclock.Clock
	armed bool
	gen   uint64
}

func newClockDebouncer(clk testclock.Clock) *clockDebouncer {
	return &clockDebouncer{clk: clk}
}

func (d *clockDebouncer) Arm(delay time.Duration, fn func()) {
	d.mu.Lock()
	if d.armed {
		d.mu.Unlock()
		return
	}
	d.armed = true
	d.gen++
	gen := d.gen
	d.mu.Unlock()

	fire := d.clk.After(delay)
	go func() {
		<-fire
		d.mu.Lock()
		if !d.armed || d.gen != gen {
			d.mu.Unlock()
			return
		}
		d.armed = false
		d.mu.Unlock()
		fn()
	}()
}

func (d *clockDebouncer) Cancel() {
	d.mu.Lock()
	d.armed = false
	d.gen++
	d.mu.Unlock()
}

func (d *clockDebouncer) Armed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.armed
}

// useClockForTest replaces the production timer debouncer with a clock-driven
// one so tests can advance the pending index save deterministically. It is a
// test seam; callers pass a fake clock.
func (c *Store) useClockForTest(clk testclock.Clock) {
	if clk == nil {
		return
	}
	c.debounce.Cancel()
	c.debounce = newClockDebouncer(clk)
}

// Flush waits for every queued chunk write and for the debounced read-index
// save to reach the disk, bounded by ctx. After Flush returns no async cache
// work remains, which lets a test assert on the on-disk state without sleeping.
func (c *Store) Flush(ctx context.Context) error {
	if !c.enabled() {
		return nil
	}
	if err := c.waitReadCacheWrites(ctx); err != nil {
		return err
	}
	return c.FlushReadIndex()
}

func (c *Store) waitReadCacheWrites(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		c.cacheWriteWGMu.Lock()
		c.cacheWriteWG.Wait()
		c.cacheWriteWGMu.Unlock()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

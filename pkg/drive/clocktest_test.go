package drive_test

import (
	"time"

	testclock "github.com/yinzhenyu/qrypt/pkg/clock"
)

// pumpFakeClock advances the fake clock in small steps while a drive operation
// runs on a context installed with drive.WithClock, so retry backoffs and close
// grace periods elapse without real sleeps. The advance budget bounds the loop:
// stale timers (for example an idle watch whose body already finished) stay
// pending in the fake clock and must not be allowed to fire.
func pumpFakeClock(clk *testclock.Fake, step time.Duration, maxSteps int, done <-chan struct{}) {
	for i := 0; i < maxSteps; i++ {
		select {
		case <-done:
			return
		default:
		}
		if clk.Pending() > 0 {
			clk.Advance(step)
		}
		time.Sleep(50 * time.Microsecond)
	}
}

package contracttest

import "time"

func DurationMillis(d time.Duration) int64 {
	if d <= 0 {
		// A coarse clock (Windows ~1ms tick) can measure real work as a zero
		// delta; any recorded duration is at least one quantum.
		d = time.Nanosecond
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

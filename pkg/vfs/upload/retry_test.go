package upload

import (
	"testing"
	"time"
)

func TestRetryDelayAtHighCountsRemainsCapped(t *testing.T) {
	for _, retry := range []int{40, 64, 32410, 32616} {
		if got := RetryDelay(retry, 2*time.Second); got != 15*time.Minute {
			t.Fatalf("RetryDelay(%d) = %s, want 15m", retry, got)
		}
	}
}

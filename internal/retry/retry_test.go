package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLinearDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{-1, time.Second},
		{0, time.Second},
		{1, 2 * time.Second},
		{2, 3 * time.Second},
	}
	for _, tt := range tests {
		if got := LinearDelay(tt.attempt); got != tt.want {
			t.Fatalf("LinearDelay(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestWaitHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Wait(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context canceled", err)
	}
}

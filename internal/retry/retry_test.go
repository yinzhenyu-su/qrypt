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

func TestExponentialBackoffWithOptions(t *testing.T) {
	tests := []struct {
		attempt int
		base    time.Duration
		max     time.Duration
		want    time.Duration
	}{
		{attempt: -1, base: 30 * time.Second, max: 2 * time.Minute, want: 30 * time.Second},
		{attempt: 0, base: 30 * time.Second, max: 2 * time.Minute, want: 30 * time.Second},
		{attempt: 1, base: 30 * time.Second, max: 2 * time.Minute, want: time.Minute},
		{attempt: 2, base: 30 * time.Second, max: 2 * time.Minute, want: 2 * time.Minute},
		{attempt: 3, base: 30 * time.Second, max: 2 * time.Minute, want: 2 * time.Minute},
	}
	for _, tt := range tests {
		got := ExponentialBackoffWithOptions(tt.attempt, tt.base, tt.max, false)
		if got != tt.want {
			t.Fatalf("ExponentialBackoffWithOptions(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestExponentialBackoffKeepsLegacyJitteredValues(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: 375 * time.Millisecond},
		{attempt: 0, want: 375 * time.Millisecond},
		{attempt: 1, want: 820 * time.Millisecond},
		{attempt: 2, want: 1_780 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := ExponentialBackoff(tt.attempt); got != tt.want {
			t.Fatalf("ExponentialBackoff(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

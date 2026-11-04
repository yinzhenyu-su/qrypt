package driverutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestUploadBudget(t *testing.T) {
	const (
		minRate = int64(256 << 10)
		min     = 15 * time.Minute
		max     = 24 * time.Hour
	)
	for _, tc := range []struct {
		name string
		size int64
		want time.Duration
	}{
		{name: "empty", size: 0, want: min},
		{name: "small", size: 100 << 20, want: min},
		{name: "scales with size", size: 5142406828, want: time.Duration(5142406828/minRate) * time.Second},
		{name: "capped", size: 1 << 50, want: max},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := UploadBudget(tc.size, minRate, min, max); got != tc.want {
				t.Fatalf("UploadBudget(%d) = %s, want %s", tc.size, got, tc.want)
			}
		})
	}
}

func TestUploadWatchdogCancelsStalledTransfer(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	watchdog := NewUploadWatchdog(ctx, bytes.NewReader(make([]byte, 1)), 10*time.Millisecond, cancel)
	defer watchdog.Stop()

	if _, err := watchdog.Read(make([]byte, 1)); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !errors.Is(context.Cause(ctx), ErrUploadStalled) {
		t.Fatalf("cause = %v, want ErrUploadStalled", context.Cause(ctx))
	}
}

func TestUploadWatchdogKeepsSlowProgressAlive(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	watchdog := NewUploadWatchdog(ctx, bytes.NewReader(make([]byte, 128)), 20*time.Millisecond, cancel)
	defer watchdog.Stop()

	buf := make([]byte, 1)
	for {
		n, err := watchdog.Read(buf)
		if n == 1 {
			time.Sleep(5 * time.Millisecond)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("slow but progressing transfer was canceled: %v", context.Cause(ctx))
	}
}

package mount

import (
	"context"
	"errors"
	"testing"
)

func TestWaitForMountStopReturnsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForMountStop(ctx, make(chan struct{})); err != nil {
		t.Fatalf("waitForMountStop() error = %v, want nil", err)
	}
}

func TestWaitForMountStopReportsSessionEnd(t *testing.T) {
	done := make(chan struct{})
	close(done)
	err := waitForMountStop(context.Background(), done)
	if !errors.Is(err, errMountSessionEnded) {
		t.Fatalf("waitForMountStop() error = %v, want errMountSessionEnded", err)
	}
}

func TestWaitForMountStopPrefersRequestedShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	close(done)
	if err := waitForMountStop(ctx, done); err != nil {
		t.Fatalf("waitForMountStop() error = %v, want nil when shutdown was requested", err)
	}
}

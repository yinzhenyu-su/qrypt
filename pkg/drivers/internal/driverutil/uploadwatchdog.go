package driverutil

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrUploadStalled reports that an upload stopped consuming its request body.
// Callers wrap it with provider context; it stays a retryable failure.
var ErrUploadStalled = errors.New("upload stalled")

// UploadWatchdog cancels a request when the HTTP transport stops consuming the
// body. A blocked write stops the transport from reading the body, so the idle
// timer is only reset by actual forward progress.
type UploadWatchdog struct {
	reader io.Reader
	timer  *time.Timer
	idle   time.Duration
}

// NewUploadWatchdog wraps reader and calls cancel with ErrUploadStalled when no
// bytes are read for idle. Stop must be called when the request completes.
func NewUploadWatchdog(ctx context.Context, reader io.Reader, idle time.Duration, cancel context.CancelCauseFunc) *UploadWatchdog {
	w := &UploadWatchdog{reader: reader, idle: idle}
	w.timer = time.AfterFunc(idle, func() {
		if ctx.Err() == nil {
			cancel(ErrUploadStalled)
		}
	})
	return w
}

func (w *UploadWatchdog) Read(p []byte) (int, error) {
	n, err := w.reader.Read(p)
	if n > 0 {
		w.timer.Reset(w.idle)
	}
	return n, err
}

// Stop releases the watchdog timer.
func (w *UploadWatchdog) Stop() { w.timer.Stop() }

// UploadBudget returns the total time budget for one upload: the time size
// needs at minRate (bytes per second), clamped to [min, max]. A non-positive
// max means no upper bound.
func UploadBudget(size, minRate int64, min, max time.Duration) time.Duration {
	if size <= 0 || minRate <= 0 {
		return min
	}
	budget := time.Duration(size/minRate) * time.Second
	if budget < min {
		return min
	}
	if max > 0 && budget > max {
		return max
	}
	return budget
}

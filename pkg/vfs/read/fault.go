package read

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
)

// ReadFaultInjector matches a fault rule against one driver range read. The
// probe server arms rules; production mounts have no registry and pay only a
// nil check.
type ReadFaultInjector interface {
	MatchReadFault(entryID string, offset, size int64) (faultinject.DebugReadFault, bool)
	ConsumeReadFault(id string)
}

type prefetchReadContextKey struct{}

// WithPrefetchRead marks a background prefetch read so foreground-only fault
// rules skip it.
func WithPrefetchRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, prefetchReadContextKey{}, true)
}

func isPrefetchRead(ctx context.Context) bool {
	value, _ := ctx.Value(prefetchReadContextKey{}).(bool)
	return value
}

// applyReadFault injects the matched fault into a driver range read. It runs
// below the shared window fetch, so the injected stall/truncate/status is
// handled by the same timeout, replay, and backoff paths as a real provider.
func applyReadFault(ctx context.Context, faults ReadFaultInjector, entry drive.Entry, offset, size int64, body io.ReadCloser) (io.ReadCloser, error) {
	if faults == nil || body == nil {
		return body, nil
	}
	fault, ok := faults.MatchReadFault(entry.ID, offset, size)
	if !ok {
		return body, nil
	}
	if isPrefetchRead(ctx) && !fault.IncludePrefetch {
		return body, nil
	}
	logging.L.Debugf("[READFAULT] inject kind=%s entry=%s offset=%d size=%d after=%d prefetch=%v", fault.Kind, entry.ID, offset, size, fault.AfterBytes, isPrefetchRead(ctx))
	switch fault.Kind {
	case faultinject.ReadFaultDelay:
		faults.ConsumeReadFault(fault.ID)
		delay := time.Duration(fault.DelayMS) * time.Millisecond
		if err := sleepContext(ctx, delay); err != nil {
			_ = body.Close()
			return nil, err
		}
		return body, nil
	case faultinject.ReadFaultStatus:
		faults.ConsumeReadFault(fault.ID)
		_ = body.Close()
		status := fault.Status
		if status == 0 {
			status = 500
		}
		return nil, &drive.HTTPStatusError{
			Status: status,
			Err:    fmt.Errorf("injected read fault: status %d", status),
		}
	case faultinject.ReadFaultStall, faultinject.ReadFaultTruncate:
		return &faultReadCloser{
			ctx:        ctx,
			inner:      body,
			faults:     faults,
			faultID:    fault.ID,
			kind:       fault.Kind,
			afterBytes: fault.AfterBytes,
		}, nil
	default:
		return body, nil
	}
}

// faultReadCloser stops a body at AfterBytes: stall blocks until the read
// context ends, truncate reports a clean EOF. Either way the shared read layer
// sees the same failure it would see from a bad provider response.
type faultReadCloser struct {
	ctx        context.Context
	inner      io.ReadCloser
	faults     ReadFaultInjector
	faultID    string
	kind       string
	afterBytes int64
	read       int64
	consumed   bool
}

func (f *faultReadCloser) Read(p []byte) (int, error) {
	if f.afterBytes >= 0 && f.read >= f.afterBytes {
		if !f.consumed {
			f.consumed = true
			logging.L.Debugf("[READFAULT] fire kind=%s after=%d read=%d", f.kind, f.afterBytes, f.read)
			f.faults.ConsumeReadFault(f.faultID)
		}
		switch f.kind {
		case faultinject.ReadFaultStall:
			<-f.ctx.Done()
			return 0, f.ctx.Err()
		case faultinject.ReadFaultTruncate:
			return 0, io.EOF
		}
	}
	if f.afterBytes > 0 {
		remaining := f.afterBytes - f.read
		if remaining < int64(len(p)) {
			p = p[:remaining]
		}
	}
	n, err := f.inner.Read(p)
	f.read += int64(n)
	return n, err
}

func (f *faultReadCloser) Close() error {
	return f.inner.Close()
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

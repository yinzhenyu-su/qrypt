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
	case faultinject.ReadFaultRate:
		return &rateReadCloser{
			ctx:     ctx,
			inner:   body,
			faults:  faults,
			faultID: fault.ID,
			rate:    fault.RateBytesPerSec,
			start:   time.Now(),
		}, nil
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

// rateReadCloser paces a provider body at a fixed byte rate. Bytes are read
// from the inner body and then delayed so the consumer observes a throttled
// connection, which is what a per-connection CDN limit looks like to the shared
// read layer. The rule is consumed on the first delivered read so a body that
// is closed before any byte cannot swallow a rule.
type rateReadCloser struct {
	ctx      context.Context
	inner    io.ReadCloser
	faults   ReadFaultInjector
	faultID  string
	rate     int64
	start    time.Time
	read     int64
	consumed bool
}

func (r *rateReadCloser) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 {
		r.read += int64(n)
		if !r.consumed {
			r.consumed = true
			r.faults.ConsumeReadFault(r.faultID)
			logging.L.Debugf("[READFAULT] fire kind=rate rate=%dB/s", r.rate)
		}
		if r.rate > 0 {
			want := time.Duration(float64(r.read) / float64(r.rate) * float64(time.Second))
			if elapsed := time.Since(r.start); want > elapsed {
				if sleepErr := sleepContext(r.ctx, want-elapsed); sleepErr != nil {
					return n, sleepErr
				}
			}
		}
	}
	return n, err
}

func (r *rateReadCloser) Close() error {
	return r.inner.Close()
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

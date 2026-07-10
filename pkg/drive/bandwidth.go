package drive

import (
	"context"
	"io"
	"sync"
	"time"
)

// BandwidthLimits configures aggregate driver bandwidth limits in bytes per second.
// A zero or negative value disables that direction.
type BandwidthLimits struct {
	DownloadBytesPerSecond int64
	UploadBytesPerSecond   int64
}

type BandwidthLimiter struct {
	download *byteLimiter
	upload   *byteLimiter
}

type BandwidthLimitDirection uint8

const (
	BandwidthLimitDownload BandwidthLimitDirection = 1 << iota
	BandwidthLimitUpload
)

const bandwidthLimitMaxChunk = 64 * 1024

// BandwidthLimitInstaller lets a driver apply the shared limiter closer to its
// transport layer. The return value declares which directions the driver now
// handles natively so the outer wrapper does not charge the same bytes twice.
type BandwidthLimitInstaller interface {
	InstallBandwidthLimiter(limiter *BandwidthLimiter) BandwidthLimitDirection
}

func NewBandwidthLimiter(limits BandwidthLimits) *BandwidthLimiter {
	if limits.DownloadBytesPerSecond <= 0 && limits.UploadBytesPerSecond <= 0 {
		return nil
	}
	return &BandwidthLimiter{
		download: newByteLimiter(limits.DownloadBytesPerSecond),
		upload:   newByteLimiter(limits.UploadBytesPerSecond),
	}
}

// NewBandwidthLimitedDriver installs download and upload limits into drivers
// that support BandwidthLimitInstaller.
func NewBandwidthLimitedDriver(raw Driver, limits BandwidthLimits) Driver {
	return WrapBandwidthLimitedDriver(raw, NewBandwidthLimiter(limits))
}

// WrapBandwidthLimitedDriver installs a shared limiter into the concrete driver.
// It intentionally does not wrap Read or PutSource at this layer: bandwidth
// limiting belongs in driver network upload/download code, not in local staging
// file reads.
func WrapBandwidthLimitedDriver(raw Driver, limiter *BandwidthLimiter) Driver {
	if raw == nil || limiter == nil {
		return raw
	}
	if installer, ok := raw.(BandwidthLimitInstaller); ok {
		installer.InstallBandwidthLimiter(limiter)
	}
	return raw
}

func (l *BandwidthLimiter) without(handled BandwidthLimitDirection) *BandwidthLimiter {
	if l == nil {
		return nil
	}
	next := &BandwidthLimiter{
		download: l.download,
		upload:   l.upload,
	}
	if handled&BandwidthLimitDownload != 0 {
		next.download = nil
	}
	if handled&BandwidthLimitUpload != 0 {
		next.upload = nil
	}
	if next.download == nil && next.upload == nil {
		return nil
	}
	return next
}

func (l *BandwidthLimiter) LimitDownload(ctx context.Context, rc io.ReadCloser) io.ReadCloser {
	if l == nil || l.download == nil || rc == nil {
		return rc
	}
	return &limitedReadCloser{ctx: ctx, rc: rc, limiter: l.download}
}

func (l *BandwidthLimiter) LimitUpload(ctx context.Context, reader io.Reader) io.Reader {
	if l == nil || l.upload == nil || reader == nil {
		return reader
	}
	if seeker, ok := reader.(io.ReadSeeker); ok {
		return &limitedReadSeeker{ctx: ctx, reader: seeker, limiter: l.upload}
	}
	return &limitedReader{ctx: ctx, reader: reader, limiter: l.upload}
}

type limitedReader struct {
	ctx     context.Context
	reader  io.Reader
	limiter *byteLimiter
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if len(p) > bandwidthLimitMaxChunk {
		p = p[:bandwidthLimitMaxChunk]
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		if waitErr := r.limiter.WaitN(r.ctx, n); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

type limitedReadSeeker struct {
	ctx     context.Context
	reader  io.ReadSeeker
	limiter *byteLimiter
}

func (r *limitedReadSeeker) Read(p []byte) (int, error) {
	if len(p) > bandwidthLimitMaxChunk {
		p = p[:bandwidthLimitMaxChunk]
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		if waitErr := r.limiter.WaitN(r.ctx, n); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

func (r *limitedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return r.reader.Seek(offset, whence)
}

type limitedReadCloser struct {
	ctx     context.Context
	rc      io.ReadCloser
	limiter *byteLimiter
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	if len(p) > bandwidthLimitMaxChunk {
		p = p[:bandwidthLimitMaxChunk]
	}
	n, err := r.rc.Read(p)
	if n > 0 {
		if waitErr := r.limiter.WaitN(r.ctx, n); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

func (r *limitedReadCloser) Close() error {
	return r.rc.Close()
}

type byteLimiter struct {
	bytesPerSecond int64
	clock          limiterClock
	mu             sync.Mutex
	next           time.Time
}

type limiterClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type realLimiterClock struct{}

func (realLimiterClock) Now() time.Time { return time.Now() }

func (realLimiterClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func newByteLimiter(bytesPerSecond int64) *byteLimiter {
	if bytesPerSecond <= 0 {
		return nil
	}
	return &byteLimiter{
		bytesPerSecond: bytesPerSecond,
		clock:          realLimiterClock{},
	}
}

func (l *byteLimiter) WaitN(ctx context.Context, n int) error {
	if l == nil || l.bytesPerSecond <= 0 || n <= 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	if l.next.Before(now) {
		l.next = now
	}
	next := l.next.Add(durationForBytes(n, l.bytesPerSecond))
	wait := next.Sub(now)
	if wait <= 0 {
		l.next = next
		return nil
	}
	timer := l.clock.After(wait)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer:
		l.next = next
		return nil
	}
}

func durationForBytes(n int, bytesPerSecond int64) time.Duration {
	if n <= 0 || bytesPerSecond <= 0 {
		return 0
	}
	return time.Duration(float64(n) * float64(time.Second) / float64(bytesPerSecond))
}

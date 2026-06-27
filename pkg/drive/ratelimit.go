package drive

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// RateLimits configures aggregate driver bandwidth limits in bytes per second.
// A zero or negative value disables that direction.
type RateLimits struct {
	DownloadBytesPerSecond int64
	UploadBytesPerSecond   int64
}

type RateLimiter struct {
	download *byteLimiter
	upload   *byteLimiter
}

type RateLimitDirection uint8

const (
	RateLimitDownload RateLimitDirection = 1 << iota
	RateLimitUpload
)

const rateLimitMaxChunk = 64 * 1024

// RateLimitInstaller lets a driver apply the shared limiter closer to its
// transport layer. The return value declares which directions the driver now
// handles natively so the outer wrapper does not charge the same bytes twice.
type RateLimitInstaller interface {
	InstallRateLimiter(limiter *RateLimiter) RateLimitDirection
}

func NewRateLimiter(limits RateLimits) *RateLimiter {
	if limits.DownloadBytesPerSecond <= 0 && limits.UploadBytesPerSecond <= 0 {
		return nil
	}
	return &RateLimiter{
		download: newByteLimiter(limits.DownloadBytesPerSecond),
		upload:   newByteLimiter(limits.UploadBytesPerSecond),
	}
}

// NewRateLimitedDriver wraps a driver with download and upload limits.
func NewRateLimitedDriver(raw Driver, limits RateLimits) Driver {
	return WrapRateLimitedDriver(raw, NewRateLimiter(limits))
}

// WrapRateLimitedDriver wraps a driver with a shared limiter.
func WrapRateLimitedDriver(raw Driver, limiter *RateLimiter) Driver {
	if raw == nil || limiter == nil {
		return raw
	}
	handled := RateLimitDirection(0)
	if installer, ok := raw.(RateLimitInstaller); ok {
		handled = installer.InstallRateLimiter(limiter)
	}
	wrapperLimiter := limiter.without(handled)
	if wrapperLimiter == nil {
		return raw
	}
	base := &rateLimitedDriver{
		raw:     raw,
		limiter: wrapperLimiter,
	}
	_, hasWriter := raw.(Writer)
	_, hasUploader := raw.(Uploader)
	_, hasFileUploader := raw.(FileUploader)
	switch {
	case hasWriter && hasUploader && hasFileUploader:
		return &rateLimitedWriterFileUploader{rateLimitedWriterUploader: &rateLimitedWriterUploader{rateLimitedDriver: base}}
	case hasWriter && hasUploader:
		return &rateLimitedWriterUploader{rateLimitedDriver: base}
	case hasWriter:
		return &rateLimitedWriter{rateLimitedDriver: base}
	case hasUploader && hasFileUploader:
		return &rateLimitedFileUploader{rateLimitedUploader: &rateLimitedUploader{rateLimitedDriver: base}}
	case hasUploader:
		return &rateLimitedUploader{rateLimitedDriver: base}
	default:
		return base
	}
}

func (l *RateLimiter) without(handled RateLimitDirection) *RateLimiter {
	if l == nil {
		return nil
	}
	next := &RateLimiter{
		download: l.download,
		upload:   l.upload,
	}
	if handled&RateLimitDownload != 0 {
		next.download = nil
	}
	if handled&RateLimitUpload != 0 {
		next.upload = nil
	}
	if next.download == nil && next.upload == nil {
		return nil
	}
	return next
}

func (l *RateLimiter) LimitDownload(ctx context.Context, rc io.ReadCloser) io.ReadCloser {
	if l == nil || l.download == nil || rc == nil {
		return rc
	}
	return &limitedReadCloser{ctx: ctx, rc: rc, limiter: l.download}
}

func (l *RateLimiter) LimitUpload(ctx context.Context, reader io.Reader) io.Reader {
	if l == nil || l.upload == nil || reader == nil {
		return reader
	}
	return &limitedReader{ctx: ctx, reader: reader, limiter: l.upload}
}

type rateLimitedDriver struct {
	raw     Driver
	limiter *RateLimiter
}

func (d *rateLimitedDriver) Init(ctx context.Context) error { return d.raw.Init(ctx) }

func (d *rateLimitedDriver) Drop(ctx context.Context) error { return d.raw.Drop(ctx) }

func (d *rateLimitedDriver) List(ctx context.Context, parentID string) ([]Entry, error) {
	return d.raw.List(ctx, parentID)
}

func (d *rateLimitedDriver) Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := d.raw.Read(ctx, entry, offset, size)
	if err != nil || d.limiter.download == nil {
		return rc, err
	}
	return &limitedReadCloser{ctx: ctx, rc: rc, limiter: d.limiter.download}, nil
}

type rateLimitedWriter struct {
	*rateLimitedDriver
}

func (d *rateLimitedWriter) Mkdir(ctx context.Context, parentID, name string) (Entry, error) {
	return d.raw.(Writer).Mkdir(ctx, parentID, name)
}

func (d *rateLimitedWriter) Move(ctx context.Context, entry Entry, dstParentID string) error {
	return d.raw.(Writer).Move(ctx, entry, dstParentID)
}

func (d *rateLimitedWriter) Rename(ctx context.Context, entry Entry, newName string) error {
	return d.raw.(Writer).Rename(ctx, entry, newName)
}

func (d *rateLimitedWriter) Remove(ctx context.Context, entry Entry) error {
	return d.raw.(Writer).Remove(ctx, entry)
}

type rateLimitedUploader struct {
	*rateLimitedDriver
}

func (d *rateLimitedUploader) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (Entry, error) {
	if d.limiter.upload != nil {
		body = &limitedReader{ctx: ctx, reader: body, limiter: d.limiter.upload}
	}
	return d.raw.(Uploader).Put(ctx, parentID, name, size, body)
}

type rateLimitedFileUploader struct {
	*rateLimitedUploader
}

func (d *rateLimitedFileUploader) PutFile(ctx context.Context, parentID, name string, size int64, localPath string) (Entry, error) {
	return d.putFile(ctx, parentID, name, size, localPath)
}

type rateLimitedWriterUploader struct {
	*rateLimitedDriver
}

func (d *rateLimitedWriterUploader) Mkdir(ctx context.Context, parentID, name string) (Entry, error) {
	return d.raw.(Writer).Mkdir(ctx, parentID, name)
}

func (d *rateLimitedWriterUploader) Move(ctx context.Context, entry Entry, dstParentID string) error {
	return d.raw.(Writer).Move(ctx, entry, dstParentID)
}

func (d *rateLimitedWriterUploader) Rename(ctx context.Context, entry Entry, newName string) error {
	return d.raw.(Writer).Rename(ctx, entry, newName)
}

func (d *rateLimitedWriterUploader) Remove(ctx context.Context, entry Entry) error {
	return d.raw.(Writer).Remove(ctx, entry)
}

func (d *rateLimitedWriterUploader) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (Entry, error) {
	if d.limiter.upload != nil {
		body = &limitedReader{ctx: ctx, reader: body, limiter: d.limiter.upload}
	}
	return d.raw.(Uploader).Put(ctx, parentID, name, size, body)
}

type rateLimitedWriterFileUploader struct {
	*rateLimitedWriterUploader
}

func (d *rateLimitedWriterFileUploader) PutFile(ctx context.Context, parentID, name string, size int64, localPath string) (Entry, error) {
	return d.putFile(ctx, parentID, name, size, localPath)
}

func (d *rateLimitedDriver) putFile(ctx context.Context, parentID, name string, size int64, localPath string) (Entry, error) {
	if d.limiter.upload == nil {
		return d.raw.(FileUploader).PutFile(ctx, parentID, name, size, localPath)
	}
	f, err := os.Open(localPath)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	body := &limitedReader{ctx: ctx, reader: f, limiter: d.limiter.upload}
	return d.raw.(Uploader).Put(ctx, parentID, name, size, body)
}

func (d *rateLimitedDriver) Space(ctx context.Context) (Space, error) {
	querier, ok := d.raw.(SpaceQuerier)
	if !ok {
		return Space{}, errors.New("drive: raw driver does not support space query")
	}
	return querier.Space(ctx)
}

func (d *rateLimitedDriver) ResolvePath(ctx context.Context, path string) (string, error) {
	resolver, ok := d.raw.(PathResolver)
	if !ok {
		return "", errors.New("drive: raw driver does not support path resolution")
	}
	return resolver.ResolvePath(ctx, path)
}

func (d *rateLimitedDriver) DebugSnapshot(ctx context.Context) (DebugSnapshot, error) {
	debugger, ok := d.raw.(Debugger)
	if !ok {
		return DebugSnapshot{}, errors.New("drive: raw driver does not support debug snapshots")
	}
	return debugger.DebugSnapshot(ctx)
}

func (d *rateLimitedDriver) HealthCheck(ctx context.Context) HealthStatus {
	checker, ok := d.raw.(HealthChecker)
	if !ok {
		return HealthStatus{Driver: "unknown", OK: false, CheckedAt: time.Now(), Error: "drive: raw driver does not support health checks"}
	}
	return checker.HealthCheck(ctx)
}

func (d *rateLimitedDriver) ResolveRemoteName(ctx context.Context, plainName string) (RemoteNameInfo, error) {
	resolver, ok := d.raw.(RemoteNameResolver)
	if !ok {
		return RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
	}
	return resolver.ResolveRemoteName(ctx, plainName)
}

type limitedReader struct {
	ctx     context.Context
	reader  io.Reader
	limiter *byteLimiter
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if len(p) > rateLimitMaxChunk {
		p = p[:rateLimitMaxChunk]
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		if waitErr := r.limiter.WaitN(r.ctx, n); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

type limitedReadCloser struct {
	ctx     context.Context
	rc      io.ReadCloser
	limiter *byteLimiter
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	if len(p) > rateLimitMaxChunk {
		p = p[:rateLimitMaxChunk]
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

var _ Driver = (*rateLimitedDriver)(nil)
var _ Writer = (*rateLimitedWriter)(nil)
var _ Uploader = (*rateLimitedUploader)(nil)
var _ Writer = (*rateLimitedWriterUploader)(nil)
var _ Uploader = (*rateLimitedWriterUploader)(nil)
var _ SpaceQuerier = (*rateLimitedDriver)(nil)
var _ PathResolver = (*rateLimitedDriver)(nil)

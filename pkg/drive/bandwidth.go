package drive

import (
	"context"
	"errors"
	"io"
	"os"
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

// NewBandwidthLimitedDriver wraps a driver with download and upload limits.
func NewBandwidthLimitedDriver(raw Driver, limits BandwidthLimits) Driver {
	return WrapBandwidthLimitedDriver(raw, NewBandwidthLimiter(limits))
}

// WrapBandwidthLimitedDriver wraps a driver with a shared limiter.
func WrapBandwidthLimitedDriver(raw Driver, limiter *BandwidthLimiter) Driver {
	if raw == nil || limiter == nil {
		return raw
	}
	handled := BandwidthLimitDirection(0)
	if installer, ok := raw.(BandwidthLimitInstaller); ok {
		handled = installer.InstallBandwidthLimiter(limiter)
	}
	wrapperLimiter := limiter.without(handled)
	if wrapperLimiter == nil {
		return raw
	}
	base := &bandwidthLimitedDriver{
		raw:     raw,
		limiter: wrapperLimiter,
	}
	_, hasWriter := raw.(Writer)
	_, hasUploader := raw.(Uploader)
	_, hasFileUploader := raw.(FileUploader)
	switch {
	case hasWriter && hasUploader && hasFileUploader:
		return &bandwidthLimitedWriterFileUploader{bandwidthLimitedWriterUploader: &bandwidthLimitedWriterUploader{bandwidthLimitedDriver: base}}
	case hasWriter && hasUploader:
		return &bandwidthLimitedWriterUploader{bandwidthLimitedDriver: base}
	case hasWriter:
		return &bandwidthLimitedWriter{bandwidthLimitedDriver: base}
	case hasUploader && hasFileUploader:
		return &bandwidthLimitedFileUploader{bandwidthLimitedUploader: &bandwidthLimitedUploader{bandwidthLimitedDriver: base}}
	case hasUploader:
		return &bandwidthLimitedUploader{bandwidthLimitedDriver: base}
	default:
		return base
	}
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
	return &limitedReader{ctx: ctx, reader: reader, limiter: l.upload}
}

type bandwidthLimitedDriver struct {
	raw     Driver
	limiter *BandwidthLimiter
}

func (d *bandwidthLimitedDriver) Init(ctx context.Context) error { return d.raw.Init(ctx) }

func (d *bandwidthLimitedDriver) Drop(ctx context.Context) error { return d.raw.Drop(ctx) }

func (d *bandwidthLimitedDriver) List(ctx context.Context, parentID string) ([]Entry, error) {
	return d.raw.List(ctx, parentID)
}

func (d *bandwidthLimitedDriver) Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := d.raw.Read(ctx, entry, offset, size)
	if err != nil || d.limiter.download == nil {
		return rc, err
	}
	return &limitedReadCloser{ctx: ctx, rc: rc, limiter: d.limiter.download}, nil
}

type bandwidthLimitedWriter struct {
	*bandwidthLimitedDriver
}

func (d *bandwidthLimitedWriter) Mkdir(ctx context.Context, parentID, name string) (Entry, error) {
	return d.raw.(Writer).Mkdir(ctx, parentID, name)
}

func (d *bandwidthLimitedWriter) Move(ctx context.Context, entry Entry, dstParentID string) error {
	return d.raw.(Writer).Move(ctx, entry, dstParentID)
}

func (d *bandwidthLimitedWriter) Rename(ctx context.Context, entry Entry, newName string) error {
	return d.raw.(Writer).Rename(ctx, entry, newName)
}

func (d *bandwidthLimitedWriter) Remove(ctx context.Context, entry Entry) error {
	return d.raw.(Writer).Remove(ctx, entry)
}

type bandwidthLimitedUploader struct {
	*bandwidthLimitedDriver
}

func (d *bandwidthLimitedUploader) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (Entry, error) {
	if d.limiter.upload != nil {
		body = &limitedReader{ctx: ctx, reader: body, limiter: d.limiter.upload}
	}
	return d.raw.(Uploader).Put(ctx, parentID, name, size, body)
}

type bandwidthLimitedFileUploader struct {
	*bandwidthLimitedUploader
}

func (d *bandwidthLimitedFileUploader) PutFile(ctx context.Context, parentID, name string, size int64, localPath string) (Entry, error) {
	return d.putFile(ctx, parentID, name, size, localPath)
}

type bandwidthLimitedWriterUploader struct {
	*bandwidthLimitedDriver
}

func (d *bandwidthLimitedWriterUploader) Mkdir(ctx context.Context, parentID, name string) (Entry, error) {
	return d.raw.(Writer).Mkdir(ctx, parentID, name)
}

func (d *bandwidthLimitedWriterUploader) Move(ctx context.Context, entry Entry, dstParentID string) error {
	return d.raw.(Writer).Move(ctx, entry, dstParentID)
}

func (d *bandwidthLimitedWriterUploader) Rename(ctx context.Context, entry Entry, newName string) error {
	return d.raw.(Writer).Rename(ctx, entry, newName)
}

func (d *bandwidthLimitedWriterUploader) Remove(ctx context.Context, entry Entry) error {
	return d.raw.(Writer).Remove(ctx, entry)
}

func (d *bandwidthLimitedWriterUploader) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (Entry, error) {
	if d.limiter.upload != nil {
		body = &limitedReader{ctx: ctx, reader: body, limiter: d.limiter.upload}
	}
	return d.raw.(Uploader).Put(ctx, parentID, name, size, body)
}

type bandwidthLimitedWriterFileUploader struct {
	*bandwidthLimitedWriterUploader
}

func (d *bandwidthLimitedWriterFileUploader) PutFile(ctx context.Context, parentID, name string, size int64, localPath string) (Entry, error) {
	return d.putFile(ctx, parentID, name, size, localPath)
}

func (d *bandwidthLimitedDriver) putFile(ctx context.Context, parentID, name string, size int64, localPath string) (Entry, error) {
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

func (d *bandwidthLimitedDriver) Space(ctx context.Context) (Space, error) {
	querier, ok := d.raw.(SpaceQuerier)
	if !ok {
		return Space{}, errors.New("drive: raw driver does not support space query")
	}
	return querier.Space(ctx)
}

func (d *bandwidthLimitedDriver) ResolvePath(ctx context.Context, path string) (string, error) {
	resolver, ok := d.raw.(PathResolver)
	if !ok {
		return "", errors.New("drive: raw driver does not support path resolution")
	}
	return resolver.ResolvePath(ctx, path)
}

func (d *bandwidthLimitedDriver) DebugSnapshot(ctx context.Context) (DebugSnapshot, error) {
	debugger, ok := d.raw.(Debugger)
	if !ok {
		return DebugSnapshot{}, errors.New("drive: raw driver does not support debug snapshots")
	}
	return debugger.DebugSnapshot(ctx)
}

func (d *bandwidthLimitedDriver) ResolveRemoteName(ctx context.Context, plainName string) (RemoteNameInfo, error) {
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

var _ Driver = (*bandwidthLimitedDriver)(nil)
var _ Writer = (*bandwidthLimitedWriter)(nil)
var _ Uploader = (*bandwidthLimitedUploader)(nil)
var _ Writer = (*bandwidthLimitedWriterUploader)(nil)
var _ Uploader = (*bandwidthLimitedWriterUploader)(nil)
var _ SpaceQuerier = (*bandwidthLimitedDriver)(nil)
var _ PathResolver = (*bandwidthLimitedDriver)(nil)
var _ Debugger = (*bandwidthLimitedDriver)(nil)
var _ RemoteNameResolver = (*bandwidthLimitedDriver)(nil)

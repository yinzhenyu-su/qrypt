package drive

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type bandwidthLimitTestDriver struct {
	data string
}

func (d *bandwidthLimitTestDriver) Init(context.Context) error { return nil }

func (d *bandwidthLimitTestDriver) Drop(context.Context) error { return nil }

func (d *bandwidthLimitTestDriver) List(context.Context, string) ([]Entry, error) { return nil, nil }

func (d *bandwidthLimitTestDriver) Read(context.Context, Entry, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(d.data)), nil
}

func (d *bandwidthLimitTestDriver) PutSource(ctx context.Context, req UploadRequest) (Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	f, err := source.Open(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return Entry{}, err
	}
	return Entry{ID: name, ParentID: parentID, Name: name, Size: int64(len(data))}, nil
}

type nativeUploadBandwidthLimitTestDriver struct {
	bandwidthLimitTestDriver
	installed bool
}

func (d *nativeUploadBandwidthLimitTestDriver) InstallBandwidthLimiter(*BandwidthLimiter) BandwidthLimitDirection {
	d.installed = true
	return BandwidthLimitUpload
}

type stringReadOnlyFileSource struct {
	data string
}

type stringReadOnlyFile struct {
	*strings.Reader
}

func newStringReadOnlyFileSource(data string) stringReadOnlyFileSource {
	return stringReadOnlyFileSource{data: data}
}

func (s stringReadOnlyFileSource) Size() int64 {
	return int64(len(s.data))
}

func (s stringReadOnlyFileSource) Open(context.Context) (ReadOnlyFile, error) {
	return stringReadOnlyFile{Reader: strings.NewReader(s.data)}, nil
}

func (f stringReadOnlyFile) Close() error {
	return nil
}

func TestNewBandwidthLimitedDriverReturnsRawWhenDisabled(t *testing.T) {
	raw := &bandwidthLimitTestDriver{}
	got := NewBandwidthLimitedDriver(raw, BandwidthLimits{})
	if got != raw {
		t.Fatalf("disabled bandwidth limit should return raw driver")
	}
}

func TestBandwidthLimitedDriverInstallsLimiterAndReturnsRaw(t *testing.T) {
	raw := &nativeUploadBandwidthLimitTestDriver{}
	drv := NewBandwidthLimitedDriver(raw, BandwidthLimits{UploadBytesPerSecond: 1})
	if drv != raw {
		t.Fatal("bandwidth installer should not wrap driver")
	}
	if !raw.installed {
		t.Fatal("native driver should receive shared limiter")
	}
	uploader := drv.(SourceUploader)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := uploader.PutSource(ctx, UploadRequest{
		ParentID: "parent",
		Name:     "file",
		Source:   newStringReadOnlyFileSource("fast"),
	}); err != nil {
		t.Fatalf("upload should not be limited by outer wrapper: %v", err)
	}
}

func TestBandwidthLimiterLimitDownloadHonorsContext(t *testing.T) {
	limiter := NewBandwidthLimiter(BandwidthLimits{DownloadBytesPerSecond: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	rc := limiter.LimitDownload(ctx, io.NopCloser(strings.NewReader("slow")))
	defer rc.Close()

	_, err := io.ReadAll(rc)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read error = %v, want context deadline exceeded", err)
	}
}

func TestBandwidthLimiterLimitUploadHonorsContext(t *testing.T) {
	limiter := NewBandwidthLimiter(BandwidthLimits{UploadBytesPerSecond: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := io.ReadAll(limiter.LimitUpload(ctx, strings.NewReader("slow")))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("upload limit error = %v, want context deadline exceeded", err)
	}
}

func TestByteLimiterCancelDoesNotLeaveDebt(t *testing.T) {
	limiter := newByteLimiter(1000)
	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := limiter.WaitN(cancelCtx, 100); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first wait error = %v, want context deadline exceeded", err)
	}

	okCtx, okCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer okCancel()
	if err := limiter.WaitN(okCtx, 100); err != nil {
		t.Fatalf("second wait should not inherit canceled reservation: %v", err)
	}
}

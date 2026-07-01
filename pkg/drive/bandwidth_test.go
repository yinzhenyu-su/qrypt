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

func (d *bandwidthLimitTestDriver) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (Entry, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return Entry{}, err
	}
	return Entry{ID: name, ParentID: parentID, Name: name, Size: int64(len(data))}, nil
}

type readOnlyBandwidthLimitTestDriver struct{}

func (d *readOnlyBandwidthLimitTestDriver) Init(context.Context) error { return nil }

func (d *readOnlyBandwidthLimitTestDriver) Drop(context.Context) error { return nil }

func (d *readOnlyBandwidthLimitTestDriver) List(context.Context, string) ([]Entry, error) {
	return nil, nil
}

func (d *readOnlyBandwidthLimitTestDriver) Read(context.Context, Entry, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

type nativeUploadBandwidthLimitTestDriver struct {
	bandwidthLimitTestDriver
	installed bool
}

func (d *nativeUploadBandwidthLimitTestDriver) InstallBandwidthLimiter(*BandwidthLimiter) BandwidthLimitDirection {
	d.installed = true
	return BandwidthLimitUpload
}

func TestNewBandwidthLimitedDriverReturnsRawWhenDisabled(t *testing.T) {
	raw := &bandwidthLimitTestDriver{}
	got := NewBandwidthLimitedDriver(raw, BandwidthLimits{})
	if got != raw {
		t.Fatalf("disabled bandwidth limit should return raw driver")
	}
}

func TestBandwidthLimitedDriverPreservesUploaderCapability(t *testing.T) {
	writable := NewBandwidthLimitedDriver(&bandwidthLimitTestDriver{}, BandwidthLimits{UploadBytesPerSecond: 1024})
	if _, ok := writable.(Uploader); !ok {
		t.Fatal("wrapped writable driver should still support upload")
	}
	readOnly := NewBandwidthLimitedDriver(&readOnlyBandwidthLimitTestDriver{}, BandwidthLimits{UploadBytesPerSecond: 1024})
	if _, ok := readOnly.(Uploader); ok {
		t.Fatal("wrapped read-only driver should not gain upload support")
	}
}

func TestBandwidthLimitedDriverRemoteNameFallback(t *testing.T) {
	drv := NewBandwidthLimitedDriver(&readOnlyBandwidthLimitTestDriver{}, BandwidthLimits{DownloadBytesPerSecond: 1024})
	resolver, ok := drv.(RemoteNameResolver)
	if !ok {
		t.Fatal("bandwidth-limited driver should provide remote-name fallback")
	}
	info, err := resolver.ResolveRemoteName(context.Background(), "plain.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info.PlainName != "plain.txt" || info.RemoteName != "plain.txt" {
		t.Fatalf("unexpected remote-name fallback: %+v", info)
	}
}

func TestBandwidthLimitedDriverSkipsDirectionHandledByNativeDriver(t *testing.T) {
	raw := &nativeUploadBandwidthLimitTestDriver{}
	drv := NewBandwidthLimitedDriver(raw, BandwidthLimits{UploadBytesPerSecond: 1})
	if !raw.installed {
		t.Fatal("native driver should receive shared limiter")
	}
	uploader := drv.(Uploader)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := uploader.Put(ctx, "parent", "file", 4, strings.NewReader("fast")); err != nil {
		t.Fatalf("upload should not be limited by outer wrapper: %v", err)
	}
}

func TestBandwidthLimitedReadHonorsContext(t *testing.T) {
	raw := &bandwidthLimitTestDriver{data: "slow"}
	drv := NewBandwidthLimitedDriver(raw, BandwidthLimits{DownloadBytesPerSecond: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	rc, err := drv.Read(ctx, Entry{ID: "file"}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	_, err = io.ReadAll(rc)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read error = %v, want context deadline exceeded", err)
	}
}

func TestBandwidthLimitedUploadHonorsContext(t *testing.T) {
	raw := &bandwidthLimitTestDriver{}
	drv := NewBandwidthLimitedDriver(raw, BandwidthLimits{UploadBytesPerSecond: 1})
	uploader := drv.(Uploader)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := uploader.Put(ctx, "parent", "file", 4, strings.NewReader("slow"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("upload error = %v, want context deadline exceeded", err)
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

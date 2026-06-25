package drive

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type rateLimitTestDriver struct {
	data string
}

func (d *rateLimitTestDriver) Init(context.Context) error { return nil }

func (d *rateLimitTestDriver) Drop(context.Context) error { return nil }

func (d *rateLimitTestDriver) List(context.Context, string) ([]Entry, error) { return nil, nil }

func (d *rateLimitTestDriver) Read(context.Context, Entry, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(d.data)), nil
}

func (d *rateLimitTestDriver) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (Entry, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return Entry{}, err
	}
	return Entry{ID: name, ParentID: parentID, Name: name, Size: int64(len(data))}, nil
}

type readOnlyRateLimitTestDriver struct{}

func (d *readOnlyRateLimitTestDriver) Init(context.Context) error { return nil }

func (d *readOnlyRateLimitTestDriver) Drop(context.Context) error { return nil }

func (d *readOnlyRateLimitTestDriver) List(context.Context, string) ([]Entry, error) {
	return nil, nil
}

func (d *readOnlyRateLimitTestDriver) Read(context.Context, Entry, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

type nativeUploadRateLimitTestDriver struct {
	rateLimitTestDriver
	installed bool
}

func (d *nativeUploadRateLimitTestDriver) InstallRateLimiter(*RateLimiter) RateLimitDirection {
	d.installed = true
	return RateLimitUpload
}

func TestNewRateLimitedDriverReturnsRawWhenDisabled(t *testing.T) {
	raw := &rateLimitTestDriver{}
	got := NewRateLimitedDriver(raw, RateLimits{})
	if got != raw {
		t.Fatalf("disabled rate limit should return raw driver")
	}
}

func TestRateLimitedDriverPreservesUploaderCapability(t *testing.T) {
	writable := NewRateLimitedDriver(&rateLimitTestDriver{}, RateLimits{UploadBytesPerSecond: 1024})
	if _, ok := writable.(Uploader); !ok {
		t.Fatal("wrapped writable driver should still support upload")
	}
	readOnly := NewRateLimitedDriver(&readOnlyRateLimitTestDriver{}, RateLimits{UploadBytesPerSecond: 1024})
	if _, ok := readOnly.(Uploader); ok {
		t.Fatal("wrapped read-only driver should not gain upload support")
	}
}

func TestRateLimitedDriverSkipsDirectionHandledByNativeDriver(t *testing.T) {
	raw := &nativeUploadRateLimitTestDriver{}
	drv := NewRateLimitedDriver(raw, RateLimits{UploadBytesPerSecond: 1})
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

func TestRateLimitedReadHonorsContext(t *testing.T) {
	raw := &rateLimitTestDriver{data: "slow"}
	drv := NewRateLimitedDriver(raw, RateLimits{DownloadBytesPerSecond: 1})
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

func TestRateLimitedUploadHonorsContext(t *testing.T) {
	raw := &rateLimitTestDriver{}
	drv := NewRateLimitedDriver(raw, RateLimits{UploadBytesPerSecond: 1})
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

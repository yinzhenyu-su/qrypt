package drive

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
)

type countingReadDriver struct {
	*FakeDriver
	reads atomic.Int32
}

func (d *countingReadDriver) Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error) {
	d.reads.Add(1)
	return d.FakeDriver.Read(ctx, entry, offset, size)
}

func seededDriver(t *testing.T, data []byte, caps ...Capability) *countingReadDriver {
	t.Helper()
	fake := NewFakeDriver(FakeWithCapabilities(caps...))
	if err := fake.Seed(map[string]string{"/file.bin": string(data)}); err != nil {
		t.Fatal(err)
	}
	return &countingReadDriver{FakeDriver: fake}
}

func TestReadParallelSplitsLargeFile(t *testing.T) {
	data := bytes.Repeat([]byte("a"), directReadPartSize*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("parallel read returned different bytes")
	}
	if got := driver.reads.Load(); got < 2 {
		t.Fatalf("driver reads = %d, want at least 2", got)
	}
}

func TestReadParallelSmallFileUsesSingleRead(t *testing.T) {
	data := bytes.Repeat([]byte("b"), 1<<20)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("single read returned different bytes")
	}
	if got := driver.reads.Load(); got != 1 {
		t.Fatalf("driver reads = %d, want 1", got)
	}
}

func TestReadParallelInternalParallelDriverStaysSingle(t *testing.T) {
	data := bytes.Repeat([]byte("c"), directReadPartSize*2)
	driver := seededDriver(t, data, CapabilityParallelRange, CapabilityInternalParallelRead)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := driver.reads.Load(); got != 1 {
		t.Fatalf("driver reads = %d, want 1", got)
	}
}

func TestReadParallelUnknownSizeUsesSingleRead(t *testing.T) {
	data := bytes.Repeat([]byte("d"), directReadPartSize*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin"}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := driver.reads.Load(); got != 1 {
		t.Fatalf("driver reads = %d, want 1", got)
	}
}

type strictFallbackReadDriver struct {
	*FakeDriver
	strictCalls atomic.Int32
	fullReads   atomic.Int32
}

func (d *strictFallbackReadDriver) Capabilities() []Capability {
	return []Capability{CapabilityParallelRange}
}

func (d *strictFallbackReadDriver) Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error) {
	if StrictRangeFromContext(ctx) {
		d.strictCalls.Add(1)
		return nil, ErrUnsupported
	}
	d.fullReads.Add(1)
	return d.FakeDriver.Read(ctx, entry, offset, size)
}

func TestReadParallelFallsBackOnStrictUnsupported(t *testing.T) {
	data := bytes.Repeat([]byte("e"), directReadPartSize*2)
	fake := NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange))
	if err := fake.Seed(map[string]string{"/file.bin": string(data)}); err != nil {
		t.Fatal(err)
	}
	driver := &strictFallbackReadDriver{FakeDriver: fake}
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("fallback read returned different bytes")
	}
	if got := driver.fullReads.Load(); got != 1 {
		t.Fatalf("full-stream reads = %d, want 1", got)
	}
	if driver.strictCalls.Load() == 0 {
		t.Fatal("strict range preflight was not used")
	}
}

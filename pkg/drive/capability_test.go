package drive

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

type capabilityReadOnlyDriver struct {
	UnsupportedOperations
}

func (d *capabilityReadOnlyDriver) Init(context.Context) error { return nil }
func (d *capabilityReadOnlyDriver) Drop(context.Context) error { return nil }
func (d *capabilityReadOnlyDriver) List(context.Context, string) ([]Entry, error) {
	return nil, nil
}
func (d *capabilityReadOnlyDriver) Read(context.Context, Entry, int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (d *capabilityReadOnlyDriver) Mkdir(context.Context, string, string) (Entry, error) {
	return Entry{}, ErrUnsupported
}
func (d *capabilityReadOnlyDriver) Move(context.Context, Entry, string) error {
	return ErrUnsupported
}
func (d *capabilityReadOnlyDriver) Rename(context.Context, Entry, string) error {
	return ErrUnsupported
}
func (d *capabilityReadOnlyDriver) Remove(context.Context, Entry) error {
	return ErrUnsupported
}
func (d *capabilityReadOnlyDriver) PutSource(context.Context, UploadRequest) (Entry, error) {
	return Entry{}, ErrUnsupported
}
func (d *capabilityReadOnlyDriver) ResolvePath(context.Context, string) (string, error) {
	return "", ErrUnsupported
}
func (d *capabilityReadOnlyDriver) Space(context.Context) (Space, error) {
	return Space{}, ErrSpaceUnsupported
}
func (d *capabilityReadOnlyDriver) Capabilities() []Capability { return nil }
func (d *capabilityReadOnlyDriver) DebugSnapshot(context.Context) (DebugSnapshot, error) {
	return DebugSnapshot{}, nil
}
func (d *capabilityReadOnlyDriver) Metrics(context.Context, time.Time) ([]MetricEvent, error) {
	return nil, nil
}

type capabilityFullDriver struct {
	capabilityReadOnlyDriver
}

func (d *capabilityFullDriver) Mkdir(context.Context, string, string) (Entry, error) {
	return Entry{}, nil
}
func (d *capabilityFullDriver) Move(context.Context, Entry, string) error { return nil }
func (d *capabilityFullDriver) Rename(context.Context, Entry, string) error {
	return nil
}
func (d *capabilityFullDriver) Remove(context.Context, Entry) error { return nil }
func (d *capabilityFullDriver) PutSource(context.Context, UploadRequest) (Entry, error) {
	return Entry{}, nil
}
func (d *capabilityFullDriver) Space(context.Context) (Space, error) { return Space{}, nil }
func (d *capabilityFullDriver) ResolvePath(context.Context, string) (string, error) {
	return "", nil
}
func (d *capabilityFullDriver) DebugSnapshot(context.Context) (DebugSnapshot, error) {
	return DebugSnapshot{}, nil
}
func (d *capabilityFullDriver) ResolveRemoteName(context.Context, string) (RemoteNameInfo, error) {
	return RemoteNameInfo{}, nil
}
func (d *capabilityFullDriver) ForeignEntries(context.Context, string) ([]ForeignEntry, error) {
	return nil, nil
}
func (d *capabilityFullDriver) Capabilities() []Capability {
	return []Capability{
		CapabilityForeignEntries,
		CapabilityPathResolver,
		CapabilityRemoteNameResolver,
		CapabilityResumableUploader,
		CapabilitySourceUploader,
		CapabilitySpace,
		CapabilityWriter,
	}
}

func TestCapabilitiesNilDriver(t *testing.T) {
	if got := Capabilities(nil); got != nil {
		t.Fatalf("Capabilities(nil) = %+v, want nil", got)
	}
	if HasCapability(nil, CapabilityWriter) {
		t.Fatal("nil driver should not report writer capability")
	}
}

func TestCapabilitiesReadOnlyDriver(t *testing.T) {
	if got := Capabilities(&capabilityReadOnlyDriver{}); len(got) != 0 {
		t.Fatalf("read-only capabilities = %+v, want none", got)
	}
	if violations := CheckUnsupportedCapabilities(context.Background(), &capabilityReadOnlyDriver{}); len(violations) != 0 {
		t.Fatalf("negative capability contract violations = %+v", violations)
	}
}

func TestCapabilitiesFullDriver(t *testing.T) {
	got := Capabilities(&capabilityFullDriver{})
	want := []Capability{
		CapabilityForeignEntries,
		CapabilityPathResolver,
		CapabilityRemoteNameResolver,
		CapabilityResumableUploader,
		CapabilitySourceUploader,
		CapabilitySpace,
		CapabilityWriter,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %+v, want %+v", got, want)
	}
	if !HasCapability(&capabilityFullDriver{}, CapabilitySourceUploader) {
		t.Fatal("full driver should report source uploader capability")
	}
	if !HasCapability(&capabilityFullDriver{}, CapabilityResumableUploader) {
		t.Fatal("full driver should report resumable uploader capability")
	}
}

func TestCapabilitiesBandwidthWrapperPreservesRuntimeCapabilities(t *testing.T) {
	wrapped := NewBandwidthLimitedDriver(&capabilityFullDriver{}, BandwidthLimits{
		DownloadBytesPerSecond: 1024,
		UploadBytesPerSecond:   1024,
	})
	got := Capabilities(wrapped)
	want := []Capability{
		CapabilityForeignEntries,
		CapabilityPathResolver,
		CapabilityRemoteNameResolver,
		CapabilityResumableUploader,
		CapabilitySourceUploader,
		CapabilitySpace,
		CapabilityWriter,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapped capabilities = %+v, want %+v", got, want)
	}
}

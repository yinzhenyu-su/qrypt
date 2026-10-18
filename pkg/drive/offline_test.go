package drive

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// TestOfflineRequestJSONRoundTrip pins the provider-neutral request wire shape:
// source kind, source, title, mount-relative destination, and selection must
// survive a round trip so mount-aware creation can persist them.
func TestOfflineRequestJSONRoundTrip(t *testing.T) {
	t.Parallel()
	req := OfflineDownloadRequest{
		SourceKind:  OfflineSourceMagnet,
		Source:      "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10",
		Title:       "Sintel",
		Destination: "/downloads",
		Selection: OfflineSelection{
			Mode:    OfflineSelectionLargest,
			Largest: 2,
			Include: []string{"*.mkv"},
			Exclude: []string{"*sample*"},
		},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var got OfflineDownloadRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if !reflect.DeepEqual(got, req) {
		t.Fatalf("request round trip = %+v, want %+v", got, req)
	}
}

// TestOfflineResultJSONRoundTrip pins the normalized observation wire shape:
// provider task id, canonical state, progress, result files, remote id, and
// provider error must survive a round trip.
func TestOfflineResultJSONRoundTrip(t *testing.T) {
	t.Parallel()
	result := OfflineDownload{
		ID:    "6e93880c0f074058bc54723e39c2c52e",
		State: OfflineStateSucceeded,
		Progress: OfflineProgress{
			Percent:    100,
			BytesDone:  276134947,
			BytesTotal: 276134947,
			FilesDone:  1,
			FilesTotal: 1,
		},
		Destination: "/downloads",
		RemoteID:    "5bcf5c5d550f40c296353b5960bb5287",
		Files: []OfflineFile{
			{Index: 0, Name: "Sintel.mp4", Size: 129241752, RemoteID: "5bcf5c5d550f40c296353b5960bb5287"},
		},
		ErrorCode: "offline_source_invalid",
		Error:     "unsupported resource",
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var got OfflineDownload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !reflect.DeepEqual(got, result) {
		t.Fatalf("result round trip = %+v, want %+v", got, result)
	}
}

// TestOfflineSelectionZeroValueMeansAll documents that the zero selection is
// "all files": creation needs no explicit selector for the common case.
func TestOfflineSelectionZeroValueMeansAll(t *testing.T) {
	t.Parallel()
	var zero OfflineSelection
	if zero.Mode != "" {
		t.Fatalf("zero selection mode = %q, want empty (all)", zero.Mode)
	}
	if zero.Largest != 0 || len(zero.Include) != 0 || len(zero.Exclude) != 0 {
		t.Fatalf("zero selection is not empty: %+v", zero)
	}
}

// offlineCapableDriver is a minimal driver that both implements the optional
// contract and declares the capability, so the alignment between the two can
// be asserted.
type offlineCapableDriver struct {
	capabilityReadOnlyDriver
}

func (d *offlineCapableDriver) Capabilities() []Capability {
	return []Capability{
		CapabilityOfflineDownload,
		CapabilityOfflineInspect,
		CapabilityOfflineSelection,
		CapabilityAcceleratedFetch,
	}
}

func (d *offlineCapableDriver) OfflineSourceKinds() []OfflineSourceKind {
	return []OfflineSourceKind{OfflineSourceMagnet, OfflineSourceHTTP}
}

func (d *offlineCapableDriver) CreateOfflineDownload(context.Context, OfflineDownloadRequest) (OfflineDownload, error) {
	return OfflineDownload{ID: "task-1", State: OfflineStateQueued}, nil
}

func (d *offlineCapableDriver) GetOfflineDownload(context.Context, string) (OfflineDownload, error) {
	return OfflineDownload{ID: "task-1", State: OfflineStateRunning}, nil
}

func (d *offlineCapableDriver) CancelOfflineDownload(context.Context, string) error { return nil }

// TestOfflineCapabilityGatesTheContract: a driver that does not implement the
// optional contract must not advertise the capability and must not satisfy the
// interface, while a driver that declares it must satisfy it, so callers can
// gate before creating any provider task.
func TestOfflineCapabilityGatesTheContract(t *testing.T) {
	t.Parallel()

	plain := &capabilityReadOnlyDriver{}
	if HasCapability(plain, CapabilityOfflineDownload) {
		t.Fatal("read-only driver must not advertise offline download")
	}
	if _, ok := any(plain).(OfflineDownloader); ok {
		t.Fatal("read-only driver must not satisfy OfflineDownloader")
	}

	capable := &offlineCapableDriver{}
	for _, capability := range []Capability{
		CapabilityOfflineDownload,
		CapabilityOfflineInspect,
		CapabilityOfflineSelection,
		CapabilityAcceleratedFetch,
	} {
		if !HasCapability(capable, capability) {
			t.Fatalf("capable driver must advertise %q", capability)
		}
	}
	want := []OfflineSourceKind{OfflineSourceMagnet, OfflineSourceHTTP}
	if got := OfflineSourceKinds(capable); !reflect.DeepEqual(got, want) {
		t.Fatalf("reported source kinds = %v, want %v", got, want)
	}
	if got := OfflineSourceKinds(plain); got != nil {
		t.Fatalf("a driver without a reporter must report nil kinds, got %v", got)
	}
	offline, ok := any(capable).(OfflineDownloader)
	if !ok {
		t.Fatal("capable driver must satisfy OfflineDownloader")
	}
	got, err := offline.GetOfflineDownload(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetOfflineDownload: %v", err)
	}
	if got.State != OfflineStateRunning {
		t.Fatalf("state = %q, want %q", got.State, OfflineStateRunning)
	}
}

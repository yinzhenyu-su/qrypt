package vfs

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fakeDebugResolveRuntime struct {
	pendingByID map[string]PendingUpload
	pathByID    map[string]string
}

func (r *fakeDebugResolveRuntime) PendingUpload(string) (PendingUpload, bool) {
	return PendingUpload{}, false
}

func (r *fakeDebugResolveRuntime) PendingUploadByRemoteID(remoteID string) (PendingUpload, bool) {
	pending, ok := r.pendingByID[remoteID]
	return pending, ok
}

func (r *fakeDebugResolveRuntime) PathByRemoteID(remoteID string) (string, bool) {
	path, ok := r.pathByID[remoteID]
	return path, ok
}

func (r *fakeDebugResolveRuntime) CacheID(drive.Entry) string { return "" }

func (r *fakeDebugResolveRuntime) Encrypted() bool { return false }

func (r *fakeDebugResolveRuntime) DriverSnapshot(context.Context) (drive.DebugSnapshot, bool) {
	return drive.DebugSnapshot{}, false
}

func (r *fakeDebugResolveRuntime) ResolveRemoteName(context.Context, string) (string, bool) {
	return "", false
}

func (r *fakeDebugResolveRuntime) RemoteList(context.Context, string) ([]drive.Entry, error) {
	return nil, nil
}

func (r *fakeDebugResolveRuntime) ForeignEntries(context.Context, string) ([]drive.ForeignEntry, error) {
	return nil, nil
}

func (r *fakeDebugResolveRuntime) UploadInProgress(string) bool { return false }

func TestDebugResolvePathByRemoteIDUsesPendingBeforeView(t *testing.T) {
	runtime := &fakeDebugResolveRuntime{
		pendingByID: map[string]PendingUpload{"id": {Path: "/pending.txt", FID: "id"}},
		pathByID:    map[string]string{"id": "/cached.txt"},
	}
	path, err := debugResolvePathByRemoteID(context.Background(), runtime, "id")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/pending.txt" {
		t.Fatalf("path = %q, want pending path", path)
	}
}

func TestDebugResolvePathByRemoteIDUsesViewFallback(t *testing.T) {
	runtime := &fakeDebugResolveRuntime{pathByID: map[string]string{"id": "/cached.txt"}}
	path, err := debugResolvePathByRemoteID(context.Background(), runtime, "id")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/cached.txt" {
		t.Fatalf("path = %q, want cached path", path)
	}
}

func TestDebugResolvePathByRemoteIDReportsMissing(t *testing.T) {
	runtime := &fakeDebugResolveRuntime{}
	if _, err := debugResolvePathByRemoteID(context.Background(), runtime, "missing"); err == nil {
		t.Fatal("missing remote id resolved")
	}
}

package diagnostics

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type fakeDebugResolveRuntime struct {
	pendingByID map[string]vfstypes.PendingUpload
	pathByID    map[string]string
}

func (r *fakeDebugResolveRuntime) Resolve(context.Context, string) (drive.Entry, error) {
	return drive.Entry{}, nil
}

func (r *fakeDebugResolveRuntime) PendingUpload(string) (vfstypes.PendingUpload, bool) {
	return vfstypes.PendingUpload{}, false
}

func (r *fakeDebugResolveRuntime) PendingUploadByRemoteID(remoteID string) (vfstypes.PendingUpload, bool) {
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
		pendingByID: map[string]vfstypes.PendingUpload{"id": {Path: "/pending.txt", FID: "id"}},
		pathByID:    map[string]string{"id": "/cached.txt"},
	}
	path, err := ResolvePathByRemoteID(context.Background(), runtime, "id")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/pending.txt" {
		t.Fatalf("path = %q, want pending path", path)
	}
}

func TestDebugResolvePathByRemoteIDUsesViewFallback(t *testing.T) {
	runtime := &fakeDebugResolveRuntime{pathByID: map[string]string{"id": "/cached.txt"}}
	path, err := ResolvePathByRemoteID(context.Background(), runtime, "id")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/cached.txt" {
		t.Fatalf("path = %q, want cached path", path)
	}
}

func TestDebugResolvePathByRemoteIDReportsMissing(t *testing.T) {
	runtime := &fakeDebugResolveRuntime{}
	if _, err := ResolvePathByRemoteID(context.Background(), runtime, "missing"); err == nil {
		t.Fatal("missing remote id resolved")
	}
}

package vfs

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func (v *VFS) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (diagnostics.DebugResolveInfo, error) {
	return diagnostics.Resolve(ctx, path, includeRemoteName, newVFSDebugResolveRuntime(v))
}

func (v *VFS) DebugResolveByRemoteID(ctx context.Context, remoteID string) (diagnostics.DebugResolveInfo, error) {
	runtime := newVFSDebugResolveRuntime(v)
	path, err := diagnostics.ResolvePathByRemoteID(ctx, runtime, remoteID)
	if err != nil {
		return diagnostics.DebugResolveInfo{}, err
	}
	return diagnostics.Resolve(ctx, path, false, runtime)
}

func (v *VFS) DebugConsistency(ctx context.Context, path string) (diagnostics.ConsistencyReport, error) {
	return diagnostics.Consistency(ctx, path, newVFSDebugResolveRuntime(v))
}

func (r vfsDebugResolveRuntime) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return r.v.resolve(ctx, path)
}

type vfsDebugResolveRuntime struct {
	v *VFS
}

func newVFSDebugResolveRuntime(v *VFS) vfsDebugResolveRuntime {
	return vfsDebugResolveRuntime{v: v}
}

func (r vfsDebugResolveRuntime) PendingUpload(path string) (PendingUpload, bool) {
	pending, err := r.v.pendingUpload(path)
	return pending, err == nil
}

func (r vfsDebugResolveRuntime) PendingUploadByRemoteID(remoteID string) (PendingUpload, bool) {
	for _, pending := range r.v.uploads.Store().PendingUploads() {
		if pending.FID == remoteID {
			return pending, true
		}
	}
	return PendingUpload{}, false
}

func (r vfsDebugResolveRuntime) PathByRemoteID(remoteID string) (string, bool) {
	var foundPath string
	var found bool
	r.v.view.entries.Range(func(path string, entry drive.Entry) bool {
		if entry.ID == remoteID {
			foundPath = path
			found = true
			return false
		}
		return true
	})
	return foundPath, found
}

func (r vfsDebugResolveRuntime) CacheID(entry drive.Entry) string {
	return r.v.readCacheKey(entry)
}

func (r vfsDebugResolveRuntime) Encrypted() bool {
	return newVFSDriverRuntime(r.v).Encrypted()
}

func (r vfsDebugResolveRuntime) DriverSnapshot(ctx context.Context) (drive.DebugSnapshot, bool) {
	snapshot, err := newVFSDriverRuntime(r.v).DebugSnapshot(ctx)
	return snapshot, err == nil
}

func (r vfsDebugResolveRuntime) ResolveRemoteName(ctx context.Context, plainName string) (string, bool) {
	nameInfo, err := newVFSDriverRuntime(r.v).ResolveRemoteName(ctx, plainName)
	if err != nil {
		return "", false
	}
	return nameInfo.RemoteName, true
}

func (r vfsDebugResolveRuntime) RemoteList(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return newVFSDriverRuntime(r.v).List(ctx, parentID)
}

func (r vfsDebugResolveRuntime) ForeignEntries(ctx context.Context, parentID string) ([]drive.ForeignEntry, error) {
	entries, err := newVFSDriverRuntime(r.v).ForeignEntries(ctx, parentID)
	if err != nil {
		return nil, nil
	}
	return entries, nil
}

func (r vfsDebugResolveRuntime) UploadInProgress(path string) bool {
	for _, upload := range r.v.uploadSnapshots(r.v.uploads.Store().PendingUploads()) {
		if upload.Path == path && upload.State == uploadSnapshotStateUploading {
			return true
		}
	}
	return false
}

// --- migrated from debug_snapshot.go ---

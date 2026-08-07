package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"path/filepath"
	"strings"
)

func (v *VFS) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error) {
	return v.debugResolveWithRuntime(ctx, path, includeRemoteName, newVFSDebugResolveRuntime(v))
}

func (v *VFS) debugResolveWithRuntime(ctx context.Context, path string, includeRemoteName bool, runtime debugResolveRuntime) (DebugResolveInfo, error) {
	path = cleanVirtual(path)
	info := DebugResolveInfo{
		Path:      path,
		Parent:    filepath.Dir(path),
		PlainName: filepath.Base(path),
	}
	resolvedRemoteName := ""
	if pending, ok := runtime.PendingUpload(path); ok {
		info.Pending = true
		info.ParentID = pending.ParentID
		info.RemoteID = pending.FID
		info.Size = pending.Size
		info.CacheID = runtime.CacheID(drive.Entry{ID: pending.FID, Size: pending.Size, ModTime: uploadModTime(pending)})
	}
	if entry, err := v.resolve(ctx, path); err == nil {
		info.RemoteID = entry.ID
		info.ParentID = entry.ParentID
		info.IsDir = entry.IsDir
		info.Size = entry.Size
		info.CacheID = runtime.CacheID(entry)
		if remoteName, ok := drive.EntryRemoteName(entry); ok {
			resolvedRemoteName = remoteName
		}
	}
	info.Encrypted = runtime.Encrypted()
	if driverSnapshot, ok := runtime.DriverSnapshot(ctx); ok {
		info.Driver = driverSnapshot.Driver
		if debugDriverEncrypted(driverSnapshot) {
			info.Encrypted = true
		}
	}
	if includeRemoteName {
		if resolvedRemoteName != "" {
			info.RemoteName = resolvedRemoteName
		} else if remoteName, ok := runtime.ResolveRemoteName(ctx, info.PlainName); ok {
			info.RemoteName = remoteName
		} else {
			info.RemoteName = info.PlainName
		}
	}
	return info, nil
}

func (v *VFS) DebugResolveByRemoteID(ctx context.Context, remoteID string) (DebugResolveInfo, error) {
	runtime := newVFSDebugResolveRuntime(v)
	path, err := debugResolvePathByRemoteID(ctx, runtime, remoteID)
	if err != nil {
		return DebugResolveInfo{}, err
	}
	return v.debugResolveWithRuntime(ctx, path, false, runtime)
}

func (v *VFS) DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error) {
	return v.debugConsistencyWithRuntime(ctx, path, newVFSDebugResolveRuntime(v))
}

func (v *VFS) debugConsistencyWithRuntime(ctx context.Context, path string, runtime debugResolveRuntime) (ConsistencyReport, error) {
	path = cleanVirtual(path)
	report := ConsistencyReport{Path: path, Parent: filepath.Dir(path), Name: filepath.Base(path)}
	expectedKnown := false
	if pending, ok := runtime.PendingUpload(path); ok {
		report.Pending = true
		report.ExpectedSize = pending.Size
		expectedKnown = true
	}
	parent, err := v.resolve(ctx, report.Parent)
	if err != nil {
		report.Status = "error"
		report.Issue = err.Error()
		return report, nil
	}
	entries, err := runtime.RemoteList(ctx, parent.ID)
	if err != nil {
		return ConsistencyReport{}, err
	}
	if foreign, err := runtime.ForeignEntries(ctx, parent.ID); err != nil {
		return ConsistencyReport{}, err
	} else if len(foreign) > 0 {
		report.ForeignEntries = foreign
	}
	for _, entry := range entries {
		if entry.Name == report.Name {
			report.RemoteFound = true
			report.RemoteID = entry.ID
			report.RemoteSize = entry.Size
			if !expectedKnown {
				report.ExpectedSize = entry.Size
			}
			report.SizeMatches = entry.Size == report.ExpectedSize
			break
		}
	}
	report.UploadInProgress = runtime.UploadInProgress(path)
	switch {
	case report.Pending && report.RemoteFound && report.SizeMatches:
		report.Status = "uploaded_pending_cleanup"
	case report.Pending && report.RemoteFound && !report.SizeMatches:
		report.Status = "mismatch"
		report.Issue = "remote size differs from pending size"
	case report.Pending && !report.RemoteFound:
		report.Status = "pending"
	case !report.Pending && report.RemoteFound:
		report.Status = "ok"
		report.SizeMatches = true
	default:
		report.Status = "missing"
		report.Issue = "not pending and not found remotely"
	}
	return report, nil
}

func (n *Namespace) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return DebugResolveInfo{}, err
	}
	if root {
		return DebugResolveInfo{Path: "/", Parent: "/", PlainName: "/", IsDir: true}, nil
	}
	info, err := mount.DebugResolve(ctx, rest, includeRemoteName)
	if err != nil {
		return DebugResolveInfo{}, err
	}
	mountName := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
	if idx := strings.Index(mountName, "/"); idx >= 0 {
		mountName = mountName[:idx]
	}
	info.Path = joinVirtual("/"+mountName, strings.TrimPrefix(info.Path, "/"))
	info.Parent = filepath.Dir(info.Path)
	info.Mount = mountName
	return info, nil
}

func (n *Namespace) DebugResolveByRemoteID(ctx context.Context, remoteID string) (*DebugResolveInfo, string, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for name, vfs := range n.mounts {
		info, err := vfs.DebugResolveByRemoteID(ctx, remoteID)
		if err == nil {
			info.Mount = name
			info.Path = joinVirtual("/"+name, strings.TrimPrefix(info.Path, "/"))
			info.Parent = filepath.Dir(info.Path)
			return &info, name, nil
		}
	}
	return nil, "", fmt.Errorf("vfs: no path found for remote ID %q", remoteID)
}

func (n *Namespace) DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return ConsistencyReport{}, err
	}
	if root {
		return ConsistencyReport{Path: "/", Status: "namespace_root"}, nil
	}
	report, err := mount.DebugConsistency(ctx, rest)
	if err != nil {
		return ConsistencyReport{}, err
	}
	mountName := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
	if idx := strings.Index(mountName, "/"); idx >= 0 {
		mountName = mountName[:idx]
	}
	report.Path = joinVirtual("/"+mountName, strings.TrimPrefix(report.Path, "/"))
	report.Parent = filepath.Dir(report.Path)
	return report, nil
}

type debugResolveRuntime interface {
	PendingUpload(path string) (PendingUpload, bool)
	PendingUploadByRemoteID(remoteID string) (PendingUpload, bool)
	PathByRemoteID(remoteID string) (string, bool)
	CacheID(entry drive.Entry) string
	Encrypted() bool
	DriverSnapshot(ctx context.Context) (drive.DebugSnapshot, bool)
	ResolveRemoteName(ctx context.Context, plainName string) (string, bool)
	RemoteList(ctx context.Context, parentID string) ([]drive.Entry, error)
	ForeignEntries(ctx context.Context, parentID string) ([]drive.ForeignEntry, error)
	UploadInProgress(path string) bool
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
	for _, pending := range r.v.upload.store.PendingUploads() {
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
	for _, upload := range r.v.uploadSnapshots(r.v.upload.store.PendingUploads()) {
		if upload.Path == path && upload.State == uploadSnapshotStateUploading {
			return true
		}
	}
	return false
}

func debugResolvePathByRemoteID(ctx context.Context, runtime debugResolveRuntime, remoteID string) (string, error) {
	if pending, ok := runtime.PendingUploadByRemoteID(remoteID); ok {
		return pending.Path, nil
	}
	if path, ok := runtime.PathByRemoteID(remoteID); ok {
		return path, nil
	}
	return "", fmt.Errorf("vfs: no path found for remote ID %q", remoteID)
}

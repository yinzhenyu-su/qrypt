package diagnostics

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ResolveRuntime is the resolve/consistency diagnostic surface (consumer
// side): pending-store lookups, the view's remote-ID mapping, the driver
// snapshot and remote listing. Resolve covers the VFS-level path resolve
// (cache-first lookup with remote fallback).
type ResolveRuntime interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	PendingUpload(path string) (vfstypes.PendingUpload, bool)
	PendingUploadByRemoteID(remoteID string) (vfstypes.PendingUpload, bool)
	PathByRemoteID(remoteID string) (string, bool)
	CacheID(entry drive.Entry) string
	Encrypted() bool
	DriverSnapshot(ctx context.Context) (drive.DebugSnapshot, bool)
	ResolveRemoteName(ctx context.Context, plainName string) (string, bool)
	RemoteList(ctx context.Context, parentID string) ([]drive.Entry, error)
	ForeignEntries(ctx context.Context, parentID string) ([]drive.ForeignEntry, error)
	UploadInProgress(path string) bool
}

// Resolve assembles the debug resolve report for one path: pending record,
// resolved entry, encryption, remote name.
func Resolve(ctx context.Context, path string, includeRemoteName bool, runtime ResolveRuntime) (DebugResolveInfo, error) {
	path = vfstypes.CleanVirtualPath(path)
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
		info.CacheID = runtime.CacheID(drive.Entry{ID: pending.FID, Size: pending.Size, ModTime: pendingModTime(pending)})
	}
	if entry, err := runtime.Resolve(ctx, path); err == nil {
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
		if DriverEncrypted(driverSnapshot) {
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

// ResolvePathByRemoteID maps a remote ID back to a virtual path via the
// pending store, then the view.
func ResolvePathByRemoteID(ctx context.Context, runtime ResolveRuntime, remoteID string) (string, error) {
	if pending, ok := runtime.PendingUploadByRemoteID(remoteID); ok {
		return pending.Path, nil
	}
	if path, ok := runtime.PathByRemoteID(remoteID); ok {
		return path, nil
	}
	return "", fmt.Errorf("vfs: no path found for remote ID %q", remoteID)
}

// Consistency assembles the consistency report for one path: pending
// state vs the remote listing under the parent.
func Consistency(ctx context.Context, path string, runtime ResolveRuntime) (ConsistencyReport, error) {
	path = vfstypes.CleanVirtualPath(path)
	report := ConsistencyReport{Path: path, Parent: filepath.Dir(path), Name: filepath.Base(path)}
	expectedKnown := false
	if pending, ok := runtime.PendingUpload(path); ok {
		report.Pending = true
		report.ExpectedSize = pending.Size
		expectedKnown = true
	}
	parent, err := runtime.Resolve(ctx, report.Parent)
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
	case !report.Pending && !report.RemoteFound:
		report.Status = "missing"
		report.Issue = "no remote entry and no pending upload"
	}
	return report, nil
}

// DriverEncrypted reports whether the driver debug snapshot declares
// encryption.
func DriverEncrypted(snapshot drive.DebugSnapshot) bool {
	if snapshot.Extra == nil {
		return false
	}
	encrypted, _ := snapshot.Extra["crypt"].(bool)
	return encrypted
}

func pendingModTime(p vfstypes.PendingUpload) time.Time {
	if p.ModTime == 0 {
		if p.UpdatedAt == 0 {
			return time.Time{}
		}
		return time.Unix(0, p.UpdatedAt)
	}
	return time.Unix(0, p.ModTime)
}

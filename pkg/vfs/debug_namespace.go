package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/internal/vfs/diagnostics"
	"path/filepath"
	"sort"
	"strings"
)

// Namespace diagnostics adapters: namespace-level orchestration of the
// per-mount diagnostics (path routing, mount filtering, name decoration,
// cross-mount remote-ID lookup). The aggregation logic itself lives in
// internal/vfs/diagnostics.
func (n *Namespace) DebugActiveOps(ctx context.Context, mountNames []string) ([]DebugActiveMount, error) {
	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		if debugActiveMountAllowed(name, mountNames) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	mounts := make([]*VFS, 0, len(names))
	for _, name := range names {
		mounts = append(mounts, n.mounts[name])
	}
	n.mu.RUnlock()

	out := make([]DebugActiveMount, 0, len(mounts))
	for i, mount := range mounts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		out = append(out, DebugActiveMount{Mount: names[i], Ops: mount.debugActiveOps()})
	}
	return out, nil
}

func (n *Namespace) DebugInjectUploadCancel(ctx context.Context, req DebugUploadCancelRequest) (DebugUploadCancelResult, error) {
	if req.Path == "" {
		return DebugUploadCancelResult{}, fmt.Errorf("vfs: namespace debug upload cancel requires path")
	}
	mount, rest, root, err := n.resolve(req.Path)
	if err != nil {
		return DebugUploadCancelResult{}, err
	}
	if root || rest == "/" {
		return DebugUploadCancelResult{}, ErrReadOnly
	}
	// The namespace mount key comes from the first path segment (same rule
	// as n.resolve), never from VFS.name, which is not guaranteed to equal
	// the mount key.
	mountKey := cleanMountName(firstVirtualSegment(req.Path))
	req.Path = rest
	result, err := mount.DebugInjectUploadCancel(ctx, req)
	if err != nil {
		return result, err
	}
	// Opaque namespace-level ID: routes the clear back to the owning
	// mount and stays unique across mounts (each registry numbers from 1).
	result.ID = mountKey + ":" + result.ID
	return result, nil
}

func (n *Namespace) DebugClearUploadCancel(ctx context.Context, id string) error {
	mountName, faultID, ok := strings.Cut(id, ":")
	if !ok || mountName == "" {
		return fmt.Errorf("vfs: invalid fault id %q (expected mount:fault_id)", id)
	}
	n.mu.RLock()
	mount, ok := n.mounts[mountName]
	n.mu.RUnlock()
	if !ok {
		return fmt.Errorf("vfs: unknown fault mount %q", mountName)
	}
	return mount.DebugClearUploadCancel(ctx, faultID)
}

func (n *Namespace) DebugUploadCancelFaults(ctx context.Context) []DebugUploadCancelFault {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var out []DebugUploadCancelFault
	for name, mount := range n.mounts {
		for _, fault := range mount.DebugUploadCancelFaults(ctx) {
			if fault.ID != "" {
				fault.ID = name + ":" + fault.ID
			}
			if fault.Path != "" {
				fault.Path = "/" + name + cleanVirtual(fault.Path)
			}
			if fault.MatchedPath != "" {
				fault.MatchedPath = "/" + name + cleanVirtual(fault.MatchedPath)
			}
			out = append(out, fault)
		}
	}
	return out
}

func (n *Namespace) Drivers() []NamedDriver {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var result []NamedDriver
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fs := n.mounts[name]
		result = append(result, newVFSDriverRuntime(fs).NamedDriver(name))
	}
	return result
}

func (n *Namespace) MountHealth(ctx context.Context, mountName string) ([]MountHealth, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if mountName != "" {
		vfs, ok := n.mounts[cleanMountName(mountName)]
		if !ok {
			return nil, fmt.Errorf("vfs: mount %q not found", mountName)
		}
		return vfs.MountHealth(ctx, mountName)
	}
	var results []MountHealth
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		health, _ := n.mounts[name].MountHealth(ctx, name)
		results = append(results, health...)
	}
	return results, nil
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
	// Copy the mount list under the read lock, then release it before the
	// (potentially slow) per-mount remote-ID queries, so management and
	// close operations never wait on them.
	n.mu.RLock()
	mounts := make([]Mount, 0, len(n.mounts))
	for name, fs := range n.mounts {
		mounts = append(mounts, Mount{Name: name, FS: fs})
	}
	n.mu.RUnlock()
	for _, m := range mounts {
		info, err := m.FS.DebugResolveByRemoteID(ctx, remoteID)
		if err == nil {
			info.Mount = m.Name
			info.Path = joinVirtual("/"+m.Name, strings.TrimPrefix(info.Path, "/"))
			info.Parent = filepath.Dir(info.Path)
			return &info, m.Name, nil
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

func (n *Namespace) DebugSnapshot() DebugSnapshot {
	snapshot := DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   timeutil.Now(),
		Kind:          "namespace",
		Process:       debugProcess(),
	}
	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		snapshot.Mounts = append(snapshot.Mounts, n.mounts[name].debugMountSnapshot(name))
	}
	n.mu.RUnlock()
	return snapshot
}

func (n *Namespace) DebugSnapshotForMounts(mountNames []string) DebugSnapshot {
	if len(mountNames) == 0 {
		return n.DebugSnapshot()
	}
	snapshot := DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   timeutil.Now(),
		Kind:          "namespace",
		Process:       debugProcess(),
	}
	names := debugMountNameSet(mountNames)
	n.mu.RLock()
	matched := make([]string, 0, len(names))
	for name := range names {
		if _, ok := n.mounts[name]; ok {
			matched = append(matched, name)
		}
	}
	sort.Strings(matched)
	for _, name := range matched {
		snapshot.Mounts = append(snapshot.Mounts, n.mounts[name].debugMountSnapshot(name))
	}
	n.mu.RUnlock()
	return snapshot
}

func (n *Namespace) DebugReset(ctx context.Context) error {
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	for _, mount := range n.mounts {
		mounts = append(mounts, mount)
	}
	n.mu.RUnlock()
	for _, mount := range mounts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		newVFSDebugReadRuntime(mount).ResetHistory()
	}
	return nil
}

func (n *Namespace) DebugStaging(ctx context.Context, path string) (DebugStagingReport, error) {
	path = cleanVirtual(path)
	report := DebugStagingReport{}
	if path != "/" {
		mount, rest, root, err := n.resolve(path)
		if err != nil {
			return DebugStagingReport{}, err
		}
		if root {
			return DebugStagingReport{Path: path}, nil
		}
		mountName := strings.Trim(strings.TrimPrefix(path, "/"), "/")
		if idx := strings.Index(mountName, "/"); idx >= 0 {
			mountName = mountName[:idx]
		}
		item := mount.debugStagingMount(mountName, rest)
		diagnostics.PrefixStagingMountPaths(&item, mountName)
		report.Path = path
		report.Mounts = []DebugStagingMount{item}
		return report, nil
	}

	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		item := n.mounts[name].debugStagingMount(name, "/")
		diagnostics.PrefixStagingMountPaths(&item, name)
		report.Mounts = append(report.Mounts, item)
	}
	n.mu.RUnlock()
	return report, nil
}

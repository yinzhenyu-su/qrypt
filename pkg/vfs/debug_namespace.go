package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
	"path/filepath"
	"sort"
	"strings"
)

// Namespace diagnostics adapters: namespace-level orchestration of the
// per-mount diagnostics (path routing, mount filtering, name decoration,
// cross-mount remote-ID lookup). The aggregation logic itself lives in
// pkg/vfs/diagnostics.

// debugSortedMounts copies the mount list under the namespace read lock
// (in stable name order) so debug adapters can run potentially slow
// per-mount queries without holding the lock - management/close never
// wait on driver I/O. Private: this is a diagnostics helper, not a
// Namespace business API.
func (n *Namespace) debugSortedMounts() []Mount {
	n.mu.RLock()
	defer n.mu.RUnlock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	mounts := make([]Mount, 0, len(names))
	for _, name := range names {
		mounts = append(mounts, Mount{Name: name, FS: n.mounts[name]})
	}
	return mounts
}
func (n *Namespace) DebugActiveOps(ctx context.Context, mountNames []string) ([]diagnostics.DebugActiveMount, error) {
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

	out := make([]diagnostics.DebugActiveMount, 0, len(mounts))
	for i, mount := range mounts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		out = append(out, diagnostics.DebugActiveMount{Mount: names[i], Ops: mount.debugActiveOps()})
	}
	return out, nil
}

func (n *Namespace) DebugInjectUploadCancel(ctx context.Context, req faultinject.DebugUploadCancelRequest) (faultinject.DebugUploadCancelResult, error) {
	if req.Path == "" {
		return faultinject.DebugUploadCancelResult{}, fmt.Errorf("vfs: namespace debug upload cancel requires path")
	}
	mount, rest, root, err := n.resolve(req.Path)
	if err != nil {
		return faultinject.DebugUploadCancelResult{}, err
	}
	if root || rest == "/" {
		return faultinject.DebugUploadCancelResult{}, ErrReadOnly
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

func (n *Namespace) DebugUploadCancelFaults(ctx context.Context) []faultinject.DebugUploadCancelFault {
	var out []faultinject.DebugUploadCancelFault
	for _, m := range n.debugSortedMounts() {
		name, mount := m.Name, m.FS
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

func (n *Namespace) Drivers() []diagnostics.NamedDriver {
	var result []diagnostics.NamedDriver
	for _, m := range n.debugSortedMounts() {
		result = append(result, newVFSDriverRuntime(m.FS).NamedDriver(m.Name))
	}
	return result
}

func (n *Namespace) MountHealth(ctx context.Context, mountName string) ([]diagnostics.MountHealth, error) {
	if mountName != "" {
		key := cleanMountName(mountName)
		n.mu.RLock()
		vfs, ok := n.mounts[key]
		n.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("vfs: mount %q not found", mountName)
		}
		return vfs.MountHealth(ctx, mountName)
	}
	var results []diagnostics.MountHealth
	for _, m := range n.debugSortedMounts() {
		health, _ := m.FS.MountHealth(ctx, m.Name)
		results = append(results, health...)
	}
	return results, nil
}

func (n *Namespace) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (diagnostics.DebugResolveInfo, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return diagnostics.DebugResolveInfo{}, err
	}
	if root {
		return diagnostics.DebugResolveInfo{Path: "/", Parent: "/", PlainName: "/", IsDir: true}, nil
	}
	info, err := mount.DebugResolve(ctx, rest, includeRemoteName)
	if err != nil {
		return diagnostics.DebugResolveInfo{}, err
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

func (n *Namespace) DebugResolveByRemoteID(ctx context.Context, remoteID string) (*diagnostics.DebugResolveInfo, string, error) {
	// Copy the mount list under the read lock, then release it before the
	// (potentially slow) per-mount remote-ID queries, so management and
	// close operations never wait on them.
	var found *diagnostics.DebugResolveInfo
	var foundName string
	for _, m := range n.debugSortedMounts() {
		info, err := m.FS.DebugResolveByRemoteID(ctx, remoteID)
		if err != nil {
			continue
		}
		if found != nil {
			// Different drivers can legitimately reuse IDs (e.g. "0" or
			// "root"); refuse to guess which mount the caller meant.
			return nil, "", fmt.Errorf(
				"vfs: remote ID %q is ambiguous across mounts %q and %q; specify a mount", remoteID, foundName, m.Name)
		}
		info.Mount = m.Name
		info.Path = joinVirtual("/"+m.Name, strings.TrimPrefix(info.Path, "/"))
		info.Parent = filepath.Dir(info.Path)
		found = &info
		foundName = m.Name
	}
	if found == nil {
		return nil, "", fmt.Errorf("vfs: no path found for remote ID %q", remoteID)
	}
	return found, foundName, nil
}

func (n *Namespace) DebugConsistency(ctx context.Context, path string) (diagnostics.ConsistencyReport, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return diagnostics.ConsistencyReport{}, err
	}
	if root {
		return diagnostics.ConsistencyReport{Path: "/", Status: "namespace_root"}, nil
	}
	report, err := mount.DebugConsistency(ctx, rest)
	if err != nil {
		return diagnostics.ConsistencyReport{}, err
	}
	mountName := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
	if idx := strings.Index(mountName, "/"); idx >= 0 {
		mountName = mountName[:idx]
	}
	report.Path = joinVirtual("/"+mountName, strings.TrimPrefix(report.Path, "/"))
	report.Parent = filepath.Dir(report.Path)
	return report, nil
}

func (n *Namespace) DebugSnapshot() diagnostics.DebugSnapshot {
	snapshot := diagnostics.DebugSnapshot{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   util.Now(),
		Kind:          "namespace",
		Process:       debugProcess(),
	}
	for _, m := range n.debugSortedMounts() {
		snapshot.Mounts = append(snapshot.Mounts, m.FS.debugMountSnapshot(m.Name))
	}
	return snapshot
}

func (n *Namespace) DebugSnapshotForMounts(mountNames []string) diagnostics.DebugSnapshot {
	if len(mountNames) == 0 {
		return n.DebugSnapshot()
	}
	snapshot := diagnostics.DebugSnapshot{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   util.Now(),
		Kind:          "namespace",
		Process:       debugProcess(),
	}
	// Copy the mount list under the lock, then run the per-mount
	// snapshots (driver metrics, cache, uploads) AFTER releasing it.
	selected := debugMountNameSet(mountNames)
	for _, mount := range n.debugSortedMounts() {
		if selected[mount.Name] {
			snapshot.Mounts = append(snapshot.Mounts, mount.FS.debugMountSnapshot(mount.Name))
		}
	}
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

func (n *Namespace) DebugStaging(ctx context.Context, path string) (diagnostics.DebugStagingReport, error) {
	path = cleanVirtual(path)
	report := diagnostics.DebugStagingReport{}
	if path != "/" {
		mount, rest, root, err := n.resolve(path)
		if err != nil {
			return diagnostics.DebugStagingReport{}, err
		}
		if root {
			return diagnostics.DebugStagingReport{Path: path}, nil
		}
		mountName := strings.Trim(strings.TrimPrefix(path, "/"), "/")
		if idx := strings.Index(mountName, "/"); idx >= 0 {
			mountName = mountName[:idx]
		}
		item := mount.debugStagingMount(mountName, rest)
		diagnostics.PrefixStagingMountPaths(&item, mountName)
		report.Path = path
		report.Mounts = []diagnostics.DebugStagingMount{item}
		return report, nil
	}

	for _, m := range n.debugSortedMounts() {
		item := m.FS.debugStagingMount(m.Name, "/")
		diagnostics.PrefixStagingMountPaths(&item, m.Name)
		report.Mounts = append(report.Mounts, item)
	}
	return report, nil
}

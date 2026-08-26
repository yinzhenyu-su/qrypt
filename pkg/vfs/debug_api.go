package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Debug API: VFS/Namespace debug entry points and the internal hooks
// the read and upload pipelines call. The aggregation logic lives in
// pkg/vfs/diagnostics; these methods bridge it to VFS internals.

func (v *VFS) DebugActiveOps(ctx context.Context, mountNames []string) ([]diagnostics.DebugActiveMount, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if !diagnostics.MountAllowed(v.name, mountNames) {
		return nil, nil
	}
	return []diagnostics.DebugActiveMount{{Mount: v.name, Ops: v.debugActiveOps()}}, nil
}

func (v *VFS) debugActiveOps() []debugActiveOp {
	ops := v.activeDebug.Snapshot()
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].StartedAt.Equal(ops[j].StartedAt) {
			return ops[i].OpID < ops[j].OpID
		}
		return ops[i].StartedAt.Before(ops[j].StartedAt)
	})
	return ops
}

func (v *VFS) DebugInjectUploadCancel(ctx context.Context, req faultinject.DebugUploadCancelRequest) (faultinject.DebugUploadCancelResult, error) {
	select {
	case <-ctx.Done():
		return faultinject.DebugUploadCancelResult{}, ctx.Err()
	default:
	}
	if req.Phase == "" && req.AfterBytes <= 0 && req.AfterDelay <= 0 {
		req.Phase = drive.UploadPhaseUploading
	}
	id, err := v.faults.Inject(faultinject.InjectRequest{
		Path:       vfstypes.CleanVirtualPath(req.Path),
		OpID:       req.OpID,
		Phase:      req.Phase,
		AfterBytes: req.AfterBytes,
		AfterDelay: req.AfterDelay,
		Once:       req.Once == nil || *req.Once,
		Reason:     req.Reason,
		TTL:        req.TTL,
	})
	if err != nil {
		return faultinject.DebugUploadCancelResult{}, err
	}
	return faultinject.DebugUploadCancelResult{ID: id, Armed: true}, nil
}

func (v *VFS) DebugClearUploadCancel(ctx context.Context, id string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return v.faults.Clear(id)
}

func (v *VFS) DebugUploadCancelFaults(ctx context.Context) []faultinject.DebugUploadCancelFault {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	return v.faults.Faults(time.Now())
}

func (v *VFS) MountHealth(ctx context.Context, mountName string) ([]diagnostics.MountHealth, error) {
	return []diagnostics.MountHealth{diagnostics.AssembleHealth(ctx, mountName, newVFSDebugHealthRuntime(v.healthTracker, v.driver))}, nil
}

func (v *VFS) Drivers() []diagnostics.NamedDriver {
	return []diagnostics.NamedDriver{newVFSDriverRuntime(v).NamedDriver(v.name)}
}

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
		if diagnostics.MountAllowed(name, mountNames) {
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
	mountKey := vfstypes.CleanMountName(firstVirtualSegment(req.Path))
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
				fault.Path = "/" + name + vfstypes.CleanVirtualPath(fault.Path)
			}
			if fault.MatchedPath != "" {
				fault.MatchedPath = "/" + name + vfstypes.CleanVirtualPath(fault.MatchedPath)
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
		key := vfstypes.CleanMountName(mountName)
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
	mountName := strings.Trim(strings.TrimPrefix(vfstypes.CleanVirtualPath(path), "/"), "/")
	if idx := strings.Index(mountName, "/"); idx >= 0 {
		mountName = mountName[:idx]
	}
	info.Path = vfstypes.JoinVirtualPath("/"+mountName, strings.TrimPrefix(info.Path, "/"))
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
		info.Path = vfstypes.JoinVirtualPath("/"+m.Name, strings.TrimPrefix(info.Path, "/"))
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
	mountName := strings.Trim(strings.TrimPrefix(vfstypes.CleanVirtualPath(path), "/"), "/")
	if idx := strings.Index(mountName, "/"); idx >= 0 {
		mountName = mountName[:idx]
	}
	report.Path = vfstypes.JoinVirtualPath("/"+mountName, strings.TrimPrefix(report.Path, "/"))
	report.Parent = filepath.Dir(report.Path)
	return report, nil
}

func (n *Namespace) DebugSnapshot() diagnostics.DebugSnapshot {
	snapshot := diagnostics.DebugSnapshot{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   util.Now(),
		Kind:          "namespace",
		Process:       diagnostics.Process(os.Getpid(), DebugStartedAt()),
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
		Process:       diagnostics.Process(os.Getpid(), DebugStartedAt()),
	}
	// Copy the mount list under the lock, then run the per-mount
	// snapshots (driver metrics, cache, uploads) AFTER releasing it.
	selected := diagnostics.MountNameSet(mountNames)
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
		newVFSDebugReadRuntime(mount.read).ResetHistory()
	}
	return nil
}

func (n *Namespace) DebugStaging(ctx context.Context, path string) (diagnostics.DebugStagingReport, error) {
	path = vfstypes.CleanVirtualPath(path)
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

func (v *VFS) debugReadHistory() []drive.MetricEvent {
	return newVFSDebugReadRuntime(v.read).History()
}

func (v *VFS) DebugReset(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	newVFSDebugReadRuntime(v.read).ResetHistory()
	return nil
}

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

func (v *VFS) DebugSnapshot() diagnostics.DebugSnapshot {
	return diagnostics.DebugSnapshot{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   util.Now(),
		Kind:          "vfs",
		Process:       diagnostics.Process(os.Getpid(), DebugStartedAt()),
		Mounts:        []diagnostics.MountSnapshot{v.debugMountSnapshot(v.name)},
	}
}

func (v *VFS) DebugSnapshotForMounts(mountNames []string) diagnostics.DebugSnapshot {
	if len(mountNames) == 0 {
		return v.DebugSnapshot()
	}
	names := diagnostics.MountNameSet(mountNames)
	if !names[v.name] {
		return diagnostics.DebugSnapshot{
			SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
			GeneratedAt:   util.Now(),
			Kind:          "vfs",
			Process:       diagnostics.Process(os.Getpid(), DebugStartedAt()),
		}
	}
	return diagnostics.DebugSnapshot{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   util.Now(),
		Kind:          "vfs",
		Process:       diagnostics.Process(os.Getpid(), DebugStartedAt()),
		Mounts:        []diagnostics.MountSnapshot{v.debugMountSnapshot(v.name)},
	}
}

func (v *VFS) debugMountSnapshot(name string) diagnostics.MountSnapshot {
	return diagnostics.AssembleMountSnapshot(name, newVFSDebugSnapshotRuntime(v))
}

func (v *VFS) debugCacheSnapshot() diagnostics.DebugCacheSnapshot {
	return diagnostics.CacheSnapshot(newVFSDebugCacheRuntime(v.read, v.uploads.Store()))
}

func (v *VFS) DebugStaging(ctx context.Context, path string) (diagnostics.DebugStagingReport, error) {
	path = vfstypes.CleanVirtualPath(path)
	mount := v.debugStagingMount(v.name, path)
	report := diagnostics.DebugStagingReport{Mounts: []diagnostics.DebugStagingMount{mount}}
	if path != "" && path != "/" {
		report.Path = path
	}
	return report, nil
}

func (v *VFS) debugStagingMount(name, path string) diagnostics.DebugStagingMount {
	return diagnostics.StagingMount(name, path, newVFSDebugStagingRuntime(v))
}

func (v *VFS) uploadSnapshots(pending []PendingUpload) []uploadSnapshot {
	active := newVFSDebugUploadRuntime(v.uploads).ActiveSnapshots()

	timerPaths := v.uploads.ScheduledDeadlines()

	uploads := make([]uploadSnapshot, 0, len(pending)+len(active))
	seen := map[string]bool{}
	for _, item := range pending {
		if upload, ok := active[item.Path]; ok {
			uploads = append(uploads, upload)
			seen[item.Path] = true
			continue
		}
		state := "queued"
		if item.PermanentFail {
			state = "failed"
		} else if _, ok := timerPaths[item.Path]; ok {
			state = "scheduled"
			if item.LastError != "" && item.NextAttemptAt > util.Now().UnixNano() {
				state = "retry_wait"
			}
		}
		uploads = append(uploads, uploadSnapshot{
			OpID:           item.FID,
			Path:           item.Path,
			Name:           item.Name,
			State:          state,
			BytesTotal:     item.Size,
			UpdatedAt:      timeFromUnixNano(item.UpdatedAt),
			RetryCount:     item.RetryCount,
			LastError:      item.LastError,
			LastAttemptAt:  item.LastAttemptAt,
			NextAttemptAt:  item.NextAttemptAt,
			ParentRemoteID: item.ParentID,
		})
		seen[item.Path] = true
	}
	for path, upload := range active {
		if !seen[path] {
			uploads = append(uploads, upload)
		}
	}
	sort.Slice(uploads, func(i, j int) bool {
		return uploads[i].Path < uploads[j].Path
	})
	return uploads
}

func (v *VFS) uploadSnapshotHistory() []uploadSnapshot {
	return newVFSDebugUploadRuntime(v.uploads).History()
}

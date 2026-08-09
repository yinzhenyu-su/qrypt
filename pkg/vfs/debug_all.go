package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/internal/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/internal/vfs/faultinject"
	"github.com/yinzhenyu/qrypt/internal/vfs/read"
	"github.com/yinzhenyu/qrypt/internal/vfs/readcache"
	"github.com/yinzhenyu/qrypt/internal/vfs/upload"
	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const uploadSnapshotHistoryLimit = 100
const debugReadHistoryLimit = read.HistoryLimit

var debugStartedAt = time.Now()
var debugStartedAtMu sync.RWMutex

func DebugStartedAt() time.Time {
	debugStartedAtMu.RLock()
	defer debugStartedAtMu.RUnlock()
	return debugStartedAt
}

func ResetDebugStartedAt() time.Time {
	debugStartedAtMu.Lock()
	defer debugStartedAtMu.Unlock()
	debugStartedAt = timeutil.Now()
	return debugStartedAt
}

const (
	uploadSnapshotStatePreparing  = upload.SnapshotStatePreparing
	uploadSnapshotStateUploading  = upload.SnapshotStateUploading
	uploadSnapshotStateCommitting = upload.SnapshotStateCommitting
	uploadSnapshotStateCompleted  = upload.SnapshotStateCompleted
	uploadSnapshotStateFailed     = upload.SnapshotStateFailed
	uploadSnapshotStateSuperseded = upload.SnapshotStateSuperseded
)

type encryptedMarker interface {
	Encrypted() bool
}

func (v *VFS) beginDebugActive(op vfstypes.DebugActiveOp) uint64 {
	return v.activeDebug.Begin(op)
}

func (v *VFS) updateDebugActive(opID uint64, fn func(*DebugActiveOp)) {
	v.activeDebug.Update(opID, fn)
}

func (v *VFS) finishDebugActive(opID uint64) {
	v.activeDebug.Finish(opID)
}

func (v *VFS) DebugActiveOps(ctx context.Context, mountNames []string) ([]DebugActiveMount, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if !debugActiveMountAllowed(v.name, mountNames) {
		return nil, nil
	}
	return []DebugActiveMount{{Mount: v.name, Ops: v.debugActiveOps()}}, nil
}

func (v *VFS) debugActiveOps() []DebugActiveOp {
	ops := v.activeDebug.Snapshot()
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].StartedAt.Equal(ops[j].StartedAt) {
			return ops[i].OpID < ops[j].OpID
		}
		return ops[i].StartedAt.Before(ops[j].StartedAt)
	})
	return ops
}

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

func debugActiveMountAllowed(mountName string, mountNames []string) bool {
	if len(mountNames) == 0 {
		return true
	}
	mountName = cleanMountName(mountName)
	for _, candidate := range mountNames {
		if cleanMountName(strings.TrimSpace(candidate)) == mountName {
			return true
		}
	}
	return false
}

// vfsDebugCacheRuntime implements diagnostics.CacheRuntime.
type vfsDebugCacheRuntime struct {
	v *VFS
}

func newVFSDebugCacheRuntime(v *VFS) vfsDebugCacheRuntime {
	return vfsDebugCacheRuntime{v: v}
}

func (r vfsDebugCacheRuntime) ReadCache() readcache.DebugReadCache {
	return r.v.readCacheSnapshot()
}

func (r vfsDebugCacheRuntime) Journal() *DebugJournal {
	return r.v.uploads.Store().DebugJournal()
}

// DebugReadCacheForTest exposes the read cache debug snapshot with the
// upload journal attached, for tests.
func (c *Stores) DebugReadCacheForTest() DebugCacheSnapshot {
	return DebugCacheSnapshot{
		DebugReadCache: c.readCacheStore.DebugSnapshot(),
		Journal:        c.uploadStore.DebugJournal(),
	}
}

// debugActiveSlots is the fixed capacity of the active-debug ring. Active
// operations are short-lived (microseconds), so 128 concurrent ops is a
// generous bound; when full, Begin returns 0 (tracking skipped).
// --- fault injection (registry lives in internal/vfs/faultinject) ---

type DebugUploadCancelInjector interface {
	DebugInjectUploadCancel(ctx context.Context, req DebugUploadCancelRequest) (DebugUploadCancelResult, error)
	DebugClearUploadCancel(ctx context.Context, id string) error
	DebugUploadCancelFaults(ctx context.Context) []DebugUploadCancelFault
}

type DebugUploadCancelRequest struct {
	Path       string            `json:"path,omitempty"`
	OpID       string            `json:"op_id,omitempty"`
	Phase      drive.UploadPhase `json:"phase,omitempty"`
	AfterBytes int64             `json:"after_bytes,omitempty"`
	AfterDelay time.Duration     `json:"after_delay,omitempty"`
	// Once controls one-shot behavior; nil (or omitted) defaults to TRUE
	// for compatibility with clients that never set it.
	Once   *bool         `json:"once,omitempty"`
	Reason string        `json:"reason,omitempty"`
	TTL    time.Duration `json:"ttl,omitempty"`
}

type DebugUploadCancelResult struct {
	ID      string `json:"id"`
	Armed   bool   `json:"armed"`
	Matched string `json:"matched,omitempty"`
}

func (v *VFS) DebugInjectUploadCancel(ctx context.Context, req DebugUploadCancelRequest) (DebugUploadCancelResult, error) {
	select {
	case <-ctx.Done():
		return DebugUploadCancelResult{}, ctx.Err()
	default:
	}
	if req.Phase == "" && req.AfterBytes <= 0 && req.AfterDelay <= 0 {
		req.Phase = drive.UploadPhaseUploading
	}
	id, err := v.faults.Inject(faultinject.InjectRequest{
		Path:       cleanVirtual(req.Path),
		OpID:       req.OpID,
		Phase:      req.Phase,
		AfterBytes: req.AfterBytes,
		AfterDelay: req.AfterDelay,
		Once:       req.Once == nil || *req.Once,
		Reason:     req.Reason,
		TTL:        req.TTL,
	})
	if err != nil {
		return DebugUploadCancelResult{}, err
	}
	return DebugUploadCancelResult{ID: id, Armed: true}, nil
}

func (v *VFS) DebugClearUploadCancel(ctx context.Context, id string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return v.faults.Clear(id)
}

func (v *VFS) DebugUploadCancelFaults(ctx context.Context) []DebugUploadCancelFault {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	return v.faults.Faults(time.Now())
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

func (v *VFS) matchUploadCancelFault(path, opID string) (faultinject.MatchResult, bool) {
	return v.faults.Match(time.Now(), path, opID)
}

type debugUploadCancelProgress struct {
	inner       drive.UploadProgress
	fault       faultinject.MatchResult
	cancel      context.CancelFunc
	cancelPath  string
	cancelOpID  string
	v           *VFS
	mu          sync.Mutex
	bytes       int64
	phase       drive.UploadPhase
	timer       *time.Timer
	timerArmed  bool
	cancelFired atomic.Bool
}

func (p *debugUploadCancelProgress) Phase(phase drive.UploadPhase) {
	if p.inner != nil {
		p.inner.Phase(phase)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
	p.maybeCancelLocked()
}

func (p *debugUploadCancelProgress) Uploaded(n int64) {
	if p.inner != nil {
		p.inner.Uploaded(n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if n > 0 {
		p.bytes += n
	}
	p.maybeCancelLocked()
}

func (p *debugUploadCancelProgress) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		p.timer.Stop()
	}
	// Swap marks the progress as finished (also blocks a late timer
	// callback from firing after Close). Only release the claim when the
	// upload truly ended without firing; once released, the rule returns
	// to armed for a later upload.
	if !p.cancelFired.Swap(true) {
		p.v.faults.Release(p.fault.Claim)
	}
}

func (p *debugUploadCancelProgress) maybeCancelLocked() {
	if p.fault.ID == "" || p.cancelFired.Load() {
		return
	}
	if p.fault.Phase != "" && p.phase != p.fault.Phase {
		return
	}
	if p.fault.AfterBytes > 0 && p.bytes < p.fault.AfterBytes {
		return
	}
	if p.fault.AfterDelay > 0 {
		if p.timerArmed {
			return
		}
		p.timerArmed = true
		p.timer = time.AfterFunc(p.fault.AfterDelay, func() {
			// fireLocked is serialized against Close/Phase/Uploaded.
			p.mu.Lock()
			p.fireLocked()
			p.mu.Unlock()
		})
		return
	}
	p.fireLocked()
}

// fireLocked must be called with p.mu held (or from a caller that holds
// no lock and has exclusive access). It is the single completion path:
// the registry records the fired state via Complete.
func (p *debugUploadCancelProgress) fireLocked() {
	if p.cancelFired.Swap(true) {
		return // already fired, or closed
	}
	logging.L.Warnf("[VFS] debug upload cancel fired op_id=%q path=%q fault=%q reason=%q", p.cancelOpID, p.cancelPath, p.fault.ID, p.fault.Reason)
	p.v.faults.Complete(p.fault.ID, p.fault.Claim, time.Now())
	p.cancel()
}

// --- migrated from debug_health.go ---

func (v *VFS) MountHealth(ctx context.Context, mountName string) ([]MountHealth, error) {
	return []MountHealth{newVFSDebugHealthRuntime(v).MountHealth(ctx, mountName)}, nil
}

func (v *VFS) Drivers() []NamedDriver {
	return []NamedDriver{newVFSDriverRuntime(v).NamedDriver(v.name)}
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

type vfsDebugHealthRuntime struct {
	v *VFS
}

func newVFSDebugHealthRuntime(v *VFS) vfsDebugHealthRuntime {
	return vfsDebugHealthRuntime{v: v}
}

func (r vfsDebugHealthRuntime) MountHealth(ctx context.Context, mountName string) MountHealth {
	h := MountHealth{Mount: mountName, CheckedAt: timeutil.Now()}
	result := r.v.healthTracker.Status()
	if metrics, err := newVFSDriverRuntime(r.v).Metrics(ctx, timeutil.Now().Add(-drive.DefaultHealthWindow)); err == nil {
		driverHealth := drive.HealthStatusFromMetrics(metrics, drive.DefaultHealthWindow, drive.DefaultMaxEvents)
		result = drive.MergeHealthStatus(result, driverHealth)
	}
	h.OK = result.OK
	h.Level = result.Level
	h.Error = result.Error
	h.CheckedAt = result.CheckedAt
	h.Success = result.Success
	h.Errors = result.Errors
	if len(result.Ops) > 0 {
		h.Ops = map[string]MountHealthOp{}
		for op, status := range result.Ops {
			h.Ops[op] = MountHealthOp{
				Success:     status.Success,
				Errors:      status.Errors,
				LastError:   status.LastError,
				LastErrorAt: status.LastErrorAt,
			}
		}
	}
	return h
}

// --- migrated from debug_read.go ---

func (v *VFS) debugCacheCounters() (hits, misses int64) {
	return newVFSDebugReadRuntime(v).CacheCounters()
}

func (v *VFS) nextDebugReadOpID() string {
	return newVFSDebugReadRuntime(v).NextOpID()
}

func (v *VFS) recordDebugRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
	finished := timeutil.Now()
	if extra == nil {
		extra = map[string]any{}
	}
	extra["source"] = source
	event := drive.MetricEvent{
		At: finished, OpID: opID, Kind: "vfs_read", Operation: "read", Phase: "read", State: "completed", OK: true,
		Path: path, RemoteID: remoteID, Offset: offset, Requested: requested,
		Bytes: bytes, CacheHits: cacheHits, CacheMisses: cacheMisses, Chunks: chunks,
		StartedAt: started, FinishedAt: finished, Duration: finished.Sub(started).String(), DurationMS: durationMillis(finished.Sub(started)),
		Extra: extra,
	}
	if bytes > 0 && finished.After(started) {
		event.Throughput = int64(float64(bytes) / finished.Sub(started).Seconds())
	}
	if err != nil {
		event.State = "failed"
		event.OK = false
		event.Error = err.Error()
		event.ErrorCategory = drive.ErrorCategory(err)
	}
	newVFSDebugReadRuntime(v).AppendEvent(event)
}

func (v *VFS) recordDebugReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error) {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok || op.OpID == "" {
		return
	}
	finished := timeutil.Now()
	event := drive.MetricEvent{
		At: finished, ParentOpID: op.OpID, Kind: "vfs_read", Operation: "read", Phase: phase, State: "completed", OK: true,
		Path: path, RemoteID: remoteID, Offset: offset, Requested: requested,
		Bytes: bytes, StartedAt: started, FinishedAt: finished, Duration: finished.Sub(started).String(), DurationMS: durationMillis(finished.Sub(started)),
		Extra: extra,
	}
	if bytes > 0 && finished.After(started) {
		event.Throughput = int64(float64(bytes) / finished.Sub(started).Seconds())
	}
	if err != nil {
		event.State = "failed"
		event.OK = false
		event.Error = err.Error()
		event.ErrorCategory = drive.ErrorCategory(err)
	}
	newVFSDebugReadRuntime(v).AppendEvent(event)
}

func (v *VFS) debugReadHistory() []drive.MetricEvent {
	return newVFSDebugReadRuntime(v).History()
}

func (v *VFS) DebugReset(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	newVFSDebugReadRuntime(v).ResetHistory()
	return nil
}

func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

type vfsDebugReadRuntime struct {
	v *VFS
}

func newVFSDebugReadRuntime(v *VFS) vfsDebugReadRuntime {
	return vfsDebugReadRuntime{v: v}
}

func (r vfsDebugReadRuntime) NextOpID() string {
	return fmt.Sprintf("read-%d", r.v.read.NextSequence())
}

func (r vfsDebugReadRuntime) CacheCounters() (hits, misses int64) {
	return r.v.readCacheCounters()
}

func (r vfsDebugReadRuntime) AppendEvent(event drive.MetricEvent) {
	r.v.read.AppendHistory(event)
}

func (r vfsDebugReadRuntime) History() []drive.MetricEvent {
	return r.v.read.HistorySnapshot()
}

func (r vfsDebugReadRuntime) ResetHistory() {
	r.v.read.ResetHistory()
}

// --- migrated from debug_resolve.go ---

func (v *VFS) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error) {
	return diagnostics.Resolve(ctx, path, includeRemoteName, newVFSDebugResolveRuntime(v))
}

func (v *VFS) DebugResolveByRemoteID(ctx context.Context, remoteID string) (DebugResolveInfo, error) {
	runtime := newVFSDebugResolveRuntime(v)
	path, err := diagnostics.ResolvePathByRemoteID(ctx, runtime, remoteID)
	if err != nil {
		return DebugResolveInfo{}, err
	}
	return diagnostics.Resolve(ctx, path, false, runtime)
}

func (v *VFS) DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error) {
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

func (v *VFS) DebugSnapshot() DebugSnapshot {
	return DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   timeutil.Now(),
		Kind:          "vfs",
		Process:       debugProcess(),
		Mounts:        []MountSnapshot{v.debugMountSnapshot(v.name)},
	}
}

func (v *VFS) DebugSnapshotForMounts(mountNames []string) DebugSnapshot {
	if len(mountNames) == 0 {
		return v.DebugSnapshot()
	}
	names := debugMountNameSet(mountNames)
	if !names[v.name] {
		return DebugSnapshot{
			SchemaVersion: DebugSnapshotSchemaVersion,
			GeneratedAt:   timeutil.Now(),
			Kind:          "vfs",
			Process:       debugProcess(),
		}
	}
	return DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   timeutil.Now(),
		Kind:          "vfs",
		Process:       debugProcess(),
		Mounts:        []MountSnapshot{v.debugMountSnapshot(v.name)},
	}
}

func (v *VFS) debugMountSnapshot(name string) MountSnapshot {
	return diagnostics.AssembleMountSnapshot(name, newVFSDebugSnapshotRuntime(v))
}

func (v *VFS) debugCacheSnapshot() DebugCacheSnapshot {
	return diagnostics.CacheSnapshot(newVFSDebugCacheRuntime(v))
}

func debugProcess() DebugProcess {
	return diagnostics.Process(os.Getpid(), DebugStartedAt())
}

func debugEncrypted(driver drive.Driver) bool {
	marker, ok := driver.(encryptedMarker)
	return ok && marker.Encrypted()
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

func debugMountNameSet(mountNames []string) map[string]bool {
	set := map[string]bool{}
	for _, name := range mountNames {
		name = cleanMountName(name)
		if name != "" {
			set[name] = true
		}
	}
	return set
}

type vfsDebugSnapshotRuntime struct {
	v *VFS
}

func newVFSDebugSnapshotRuntime(v *VFS) vfsDebugSnapshotRuntime {
	return vfsDebugSnapshotRuntime{v: v}
}

func (r vfsDebugSnapshotRuntime) Identity(name string) MountSnapshotIdentity {
	driverRuntime := newVFSDriverRuntime(r.v)
	return MountSnapshotIdentity{
		Name:         name,
		RootID:       r.v.rootID,
		Capabilities: driverRuntime.Capabilities(),
		Encrypted:    driverRuntime.Encrypted(),
	}
}

func (r vfsDebugSnapshotRuntime) Queues() MountSnapshotQueues {
	return MountSnapshotQueues{
		UploadLength:  len(r.v.uploads.Queue()),
		UploadCap:     cap(r.v.uploads.Queue()),
		UploadWorkers: r.v.uploads.WorkerCount(),
		UploadDelay:   r.v.uploads.DefaultDelay().String(),
		DeleteDelay:   r.v.deletes.delay.String(),
	}
}

func (r vfsDebugSnapshotRuntime) PendingUploads() []PendingUpload {
	return r.v.uploads.Store().PendingUploads()
}

func (r vfsDebugSnapshotRuntime) DriverSnapshot(ctx context.Context) (drive.DebugSnapshot, bool) {
	snapshot, err := newVFSDriverRuntime(r.v).DebugSnapshot(ctx)
	return snapshot, err == nil
}

func (r vfsDebugSnapshotRuntime) DriverMetrics(ctx context.Context, since time.Time) []drive.MetricEvent {
	metrics, err := newVFSDriverRuntime(r.v).Metrics(ctx, since)
	if err != nil {
		return nil
	}
	return metrics
}

func (r vfsDebugSnapshotRuntime) UploadTimers() []DebugTimer {
	deadlines := r.v.uploads.ScheduledDeadlines()
	timers := make([]DebugTimer, 0, len(deadlines))
	for path, deadline := range deadlines {
		timers = append(timers, DebugTimer{Path: path, Deadline: deadline})
	}
	sort.Slice(timers, func(i, j int) bool {
		return timers[i].Path < timers[j].Path
	})
	return timers
}

func (r vfsDebugSnapshotRuntime) Overlay() diagnostics.OverlaySnapshot {
	now := time.Now()
	out := diagnostics.OverlaySnapshot{}
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	for path := range r.v.deletes.tasks.scheduler.Keys() {
		out.DeleteTimers = append(out.DeleteTimers, DebugTimer{Path: path})
	}
	for path, entry := range r.v.view.overlay.deleted {
		out.Deleted = append(out.Deleted, DebugDeletedEntry{
			Path:  path,
			ID:    entry.ID,
			Name:  entry.Name,
			IsDir: entry.IsDir,
			Size:  entry.Size,
		})
	}
	for _, op := range r.v.view.overlay.renameOverlays {
		out.OverlayOps = append(out.OverlayOps, DebugOverlayOp{
			OldPath: op.oldPath,
			NewPath: op.newPath,
			EntryID: op.entryID,
			IsDir:   op.isDir,
			OldGone: op.oldGone,
			NewSeen: op.newSeen,
		})
	}
	for path, deadline := range r.v.view.overlay.restoredDirs {
		if now.After(deadline) {
			continue
		}
		out.RestoredDirs = append(out.RestoredDirs, DebugTimer{Path: path, Deadline: deadline})
	}
	for dir, names := range r.v.view.overlay.copyHiddenChildren {
		item := DebugCopyHidden{Dir: dir}
		for name, deadline := range names {
			if now.After(deadline) {
				continue
			}
			item.Names = append(item.Names, DebugTimer{Path: name, Deadline: deadline})
		}
		sort.Slice(item.Names, func(i, j int) bool {
			return item.Names[i].Path < item.Names[j].Path
		})
		if len(item.Names) > 0 {
			out.CopyHidden = append(out.CopyHidden, item)
		}
	}
	sort.Slice(out.DeleteTimers, func(i, j int) bool {
		return out.DeleteTimers[i].Path < out.DeleteTimers[j].Path
	})
	sort.Slice(out.Deleted, func(i, j int) bool {
		return out.Deleted[i].Path < out.Deleted[j].Path
	})
	sort.Slice(out.OverlayOps, func(i, j int) bool {
		return out.OverlayOps[i].OldPath < out.OverlayOps[j].OldPath
	})
	sort.Slice(out.RestoredDirs, func(i, j int) bool {
		return out.RestoredDirs[i].Path < out.RestoredDirs[j].Path
	})
	sort.Slice(out.CopyHidden, func(i, j int) bool {
		return out.CopyHidden[i].Dir < out.CopyHidden[j].Dir
	})
	return out
}

func (r vfsDebugSnapshotRuntime) Cache() DebugCacheSnapshot {
	return r.v.debugCacheSnapshot()
}

func (r vfsDebugSnapshotRuntime) ReadHistory() []drive.MetricEvent {
	return r.v.debugReadHistory()
}

func (r vfsDebugSnapshotRuntime) UploadSnapshots(pending []PendingUpload) []UploadSnapshot {
	return r.v.uploadSnapshots(pending)
}

func (r vfsDebugSnapshotRuntime) UploadHistory() []UploadSnapshot {
	return r.v.uploadSnapshotHistory()
}

func (r vfsDebugSnapshotRuntime) StartedAt() time.Time {
	return DebugStartedAt()
}

func (r vfsDebugSnapshotRuntime) Runtime() diagnostics.RuntimeSnapshot {
	out := diagnostics.RuntimeSnapshot{
		HotChunkLimit: read.HotChunkLimit,
		RangeHitLimit: read.RangeHitLimit,
	}
	out.WindowLoads, out.Prefetches, out.RangeHitCount = r.v.read.RuntimeStats()
	out.HotChunkCount, out.HotChunkBytes = r.v.debugHotChunks()
	return out
}

// --- migrated from debug_staging.go ---

func (v *VFS) DebugStaging(ctx context.Context, path string) (DebugStagingReport, error) {
	path = cleanVirtual(path)
	mount := v.debugStagingMount(v.name, path)
	report := DebugStagingReport{Mounts: []DebugStagingMount{mount}}
	if path != "" && path != "/" {
		report.Path = path
	}
	return report, nil
}

func (v *VFS) debugStagingMount(name, path string) DebugStagingMount {
	return diagnostics.StagingMount(name, path, newVFSDebugStagingRuntime(v))
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

type debugStagingRuntime interface {
	PendingUploads() []PendingUpload
	UploadingPaths(pending []PendingUpload) map[string]bool
	StagingDir() string
	StagingFiles() ([]DebugStagingFile, error)
}

type vfsDebugStagingRuntime struct {
	v *VFS
}

func newVFSDebugStagingRuntime(v *VFS) vfsDebugStagingRuntime {
	return vfsDebugStagingRuntime{v: v}
}

func (r vfsDebugStagingRuntime) PendingUploads() []PendingUpload {
	return r.v.uploads.Store().PendingUploads()
}

func (r vfsDebugStagingRuntime) UploadingPaths(pending []PendingUpload) map[string]bool {
	uploading := map[string]bool{}
	for _, upload := range r.v.uploadSnapshots(pending) {
		if upload.State == uploadSnapshotStateUploading {
			uploading[upload.Path] = true
		}
	}
	return uploading
}

func (r vfsDebugStagingRuntime) StagingDir() string {
	return r.v.uploads.Store().StagingDir()
}

func (r vfsDebugStagingRuntime) StagingFiles() ([]DebugStagingFile, error) {
	entries, err := os.ReadDir(r.StagingDir())
	if err != nil {
		return nil, err
	}
	files := make([]DebugStagingFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".staging") {
			continue
		}
		localPath := filepath.Join(r.StagingDir(), entry.Name())
		info, statErr := entry.Info()
		file := DebugStagingFile{LocalPath: localPath, Exists: statErr == nil}
		if statErr != nil {
			file.Issue = statErr.Error()
		} else {
			file.StagingSize = info.Size()
			modTime := info.ModTime()
			file.ModTime = &modTime
		}
		files = append(files, file)
	}
	return files, nil
}

// --- migrated from debug_upload.go ---

func (v *VFS) uploadSnapshots(pending []PendingUpload) []UploadSnapshot {
	active := newVFSDebugUploadRuntime(v).ActiveSnapshots()

	timerPaths := v.uploads.ScheduledDeadlines()

	uploads := make([]UploadSnapshot, 0, len(pending)+len(active))
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
			if item.LastError != "" && item.NextAttemptAt > timeutil.Now().UnixNano() {
				state = "retry_wait"
			}
		}
		uploads = append(uploads, UploadSnapshot{
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

func (v *VFS) uploadSnapshotHistory() []UploadSnapshot {
	return newVFSDebugUploadRuntime(v).History()
}

func (v *VFS) startUploadSnapshot(p PendingUpload) {
	newVFSDebugUploadRuntime(v).StartSnapshot(p)
}

func (v *VFS) setUploadSnapshotState(path, state string) {
	newVFSDebugUploadRuntime(v).SetSnapshotState(path, state)
}

func (v *VFS) finishUploadSnapshot(path, state, lastError string) {
	newVFSDebugUploadRuntime(v).FinishSnapshot(path, state, lastError)
}

func (v *VFS) setUploadSnapshotMetadata(path, resultRemoteID string, hashes []string) {
	newVFSDebugUploadRuntime(v).SetSnapshotMetadata(path, resultRemoteID, hashes)
}

func (v *VFS) updateUploadSnapshot(path string, n int) {
	newVFSDebugUploadRuntime(v).UpdateSnapshot(path, n)
}

func (v *VFS) recordUploadEvent(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	newVFSDebugUploadRuntime(v).RecordEvent(path, phase, start, bytes, extra)
}

func (v *VFS) setUploadSnapshotExtra(path string, key string, value any) {
	newVFSDebugUploadRuntime(v).SetSnapshotExtra(path, key, value)
}

type vfsDebugUploadRuntime struct {
	v *VFS
}

func newVFSDebugUploadRuntime(v *VFS) vfsDebugUploadRuntime {
	return vfsDebugUploadRuntime{v: v}
}

func (r vfsDebugUploadRuntime) ActiveSnapshots() map[string]UploadSnapshot {
	active := map[string]UploadSnapshot{}
	r.v.uploads.DebugState().Mu.Lock()
	for path, state := range r.v.uploads.DebugState().Active {
		active[path] = cloneUploadSnapshot(state.Upload)
	}
	r.v.uploads.DebugState().Mu.Unlock()
	return active
}

func (r vfsDebugUploadRuntime) History() []UploadSnapshot {
	r.v.uploads.DebugState().Mu.Lock()
	defer r.v.uploads.DebugState().Mu.Unlock()
	out := make([]UploadSnapshot, len(r.v.uploads.DebugState().History))
	for i, upload := range r.v.uploads.DebugState().History {
		out[i] = cloneUploadSnapshot(upload)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].Path < out[j].Path
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out
}

// cloneUploadSnapshot deep-copies the slice and map fields that are mutated
// under the upload debug lock, so snapshot consumers can read their copy
// outside the lock without racing the upload pipeline.
func cloneUploadSnapshot(u UploadSnapshot) UploadSnapshot {
	u.Events = append([]drive.MetricEvent(nil), u.Events...)
	u.Hashes = append([]string(nil), u.Hashes...)
	u.Extra = maps.Clone(u.Extra)
	u.StageDurations = maps.Clone(u.StageDurations)
	return u
}

func (r vfsDebugUploadRuntime) RemoveHistoryByID(id string) bool {
	if id == "" {
		return false
	}
	r.v.uploads.DebugState().Mu.Lock()
	defer r.v.uploads.DebugState().Mu.Unlock()
	for i, upload := range r.v.uploads.DebugState().History {
		if upload.OpID != id {
			continue
		}
		copy(r.v.uploads.DebugState().History[i:], r.v.uploads.DebugState().History[i+1:])
		r.v.uploads.DebugState().History = r.v.uploads.DebugState().History[:len(r.v.uploads.DebugState().History)-1]
		return true
	}
	return false
}

func (r vfsDebugUploadRuntime) StartSnapshot(p PendingUpload) {
	now := timeutil.Now()
	r.v.uploads.DebugState().Mu.Lock()
	r.v.uploads.DebugState().Active[p.Path] = &uploadSnapshotState{
		StageStartedAt: now,
		Upload: UploadSnapshot{
			OpID:           p.FID,
			Path:           p.Path,
			Name:           p.Name,
			State:          "starting",
			BytesTotal:     p.Size,
			StartedAt:      now,
			UpdatedAt:      now,
			RetryCount:     p.RetryCount,
			LastError:      p.LastError,
			LastAttemptAt:  p.LastAttemptAt,
			NextAttemptAt:  p.NextAttemptAt,
			ParentRemoteID: p.ParentID,
		},
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) SetSnapshotState(path, state string) {
	r.v.uploads.DebugState().Mu.Lock()
	if upload := r.v.uploads.DebugState().Active[path]; upload != nil {
		upload.RecordStageDuration(timeutil.Now())
		upload.Upload.State = state
		if state == string(drive.UploadPhaseInstant) {
			upload.Upload.Instant = true
		}
		upload.Upload.UpdatedAt = upload.StageStartedAt
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) FinishSnapshot(path, state, lastError string) {
	r.v.uploads.DebugState().Mu.Lock()
	if upload := r.v.uploads.DebugState().Active[path]; upload != nil {
		now := timeutil.Now()
		upload.RecordStageDuration(now)
		upload.Upload.State = state
		upload.Upload.LastError = lastError
		if lastError != "" {
			upload.Upload.ErrorCategory = drive.ErrorCategoryMessage(lastError)
		}
		upload.Upload.UpdatedAt = now
		upload.Upload.CompletedAt = upload.Upload.UpdatedAt
		r.v.uploads.DebugState().History = append(r.v.uploads.DebugState().History, upload.Upload)
		if len(r.v.uploads.DebugState().History) > uploadSnapshotHistoryLimit {
			copy(r.v.uploads.DebugState().History, r.v.uploads.DebugState().History[len(r.v.uploads.DebugState().History)-uploadSnapshotHistoryLimit:])
			r.v.uploads.DebugState().History = r.v.uploads.DebugState().History[:uploadSnapshotHistoryLimit]
		}
		delete(r.v.uploads.DebugState().Active, path)
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) SetSnapshotMetadata(path, resultRemoteID string, hashes []string) {
	r.v.uploads.DebugState().Mu.Lock()
	if state := r.v.uploads.DebugState().Active[path]; state != nil {
		if resultRemoteID != "" {
			state.Upload.ResultRemoteID = resultRemoteID
		}
		if len(hashes) > 0 {
			state.Upload.Hashes = append([]string(nil), hashes...)
		}
		state.Upload.UpdatedAt = timeutil.Now()
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) UpdateSnapshot(path string, n int) {
	if n <= 0 {
		return
	}
	r.v.uploads.DebugState().Mu.Lock()
	if state := r.v.uploads.DebugState().Active[path]; state != nil {
		state.Upload.BytesUploaded += int64(n)
		if state.Upload.BytesTotal >= 0 && state.Upload.BytesUploaded > state.Upload.BytesTotal {
			state.Upload.BytesUploaded = state.Upload.BytesTotal
		}
		state.Upload.UpdatedAt = timeutil.Now()
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) RecordEvent(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	if phase == "" || start.IsZero() {
		return
	}
	finished := timeutil.Now()
	duration := finished.Sub(start)
	event := drive.MetricEvent{
		At:         finished,
		Kind:       "vfs_upload",
		Operation:  "upload",
		Phase:      phase,
		State:      "completed",
		OK:         true,
		Bytes:      bytes,
		Duration:   duration.String(),
		DurationMS: durationMillis(duration),
		StartedAt:  start,
		FinishedAt: finished,
		Extra:      extra,
	}
	if message, ok := extra["error"].(string); ok && message != "" {
		event.State = "failed"
		event.OK = false
		event.Error = message
		event.ErrorCategory = drive.ErrorCategoryMessage(message)
	}
	if bytes > 0 && duration > 0 {
		event.Throughput = int64(float64(bytes) / duration.Seconds())
	}
	r.v.uploads.DebugState().Mu.Lock()
	if state := r.v.uploads.DebugState().Active[path]; state != nil {
		state.Upload.Events = append(state.Upload.Events, event)
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

func (r vfsDebugUploadRuntime) SetSnapshotExtra(path string, key string, value any) {
	if key == "" {
		return
	}
	r.v.uploads.DebugState().Mu.Lock()
	if state := r.v.uploads.DebugState().Active[path]; state != nil {
		if state.Upload.Extra == nil {
			state.Upload.Extra = map[string]any{}
		}
		state.Upload.Extra[key] = value
		state.Upload.UpdatedAt = timeutil.Now()
	}
	r.v.uploads.DebugState().Mu.Unlock()
}

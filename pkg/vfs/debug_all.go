package vfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/internal/vfs/read"
	"github.com/yinzhenyu/qrypt/internal/vfs/readcache"
	"github.com/yinzhenyu/qrypt/internal/vfs/upload"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const DebugSnapshotSchemaVersion = 2
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

type DebugSnapshot struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Kind          string          `json:"kind"`
	Process       DebugProcess    `json:"process"`
	Mounts        []MountSnapshot `json:"mounts"`
}

type DebugProcess struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

type encryptedMarker interface {
	Encrypted() bool
}

type MountSnapshot struct {
	Identity    MountSnapshotIdentity    `json:"identity"`
	Queues      MountSnapshotQueues      `json:"queues"`
	Overlay     MountSnapshotOverlay     `json:"overlay"`
	UploadState MountSnapshotUploads     `json:"upload_state"`
	Cache       readcache.DebugReadCache `json:"cache"`
	Events      MountSnapshotEvents      `json:"events"`
	Runtime     MountSnapshotRuntime     `json:"runtime"`
}

type MountSnapshotIdentity struct {
	Name         string               `json:"name"`
	DriverName   string               `json:"driver_name,omitempty"`
	RootID       string               `json:"root_id,omitempty"`
	Capabilities []drive.Capability   `json:"capabilities,omitempty"`
	Encrypted    bool                 `json:"encrypted"`
	Driver       *drive.DebugSnapshot `json:"driver,omitempty"`
}

type MountSnapshotQueues struct {
	UploadLength  int          `json:"upload_length"`
	UploadCap     int          `json:"upload_cap"`
	UploadWorkers int          `json:"upload_workers"`
	UploadDelay   string       `json:"upload_delay"`
	DeleteDelay   string       `json:"delete_delay"`
	UploadTimers  []DebugTimer `json:"upload_timers,omitempty"`
	DeleteTimers  []DebugTimer `json:"delete_timers,omitempty"`
}

type MountSnapshotOverlay struct {
	Pending      []PendingUpload     `json:"pending,omitempty"`
	Deleted      []DebugDeletedEntry `json:"deleted,omitempty"`
	OverlayOps   []DebugOverlayOp    `json:"overlay_ops,omitempty"`
	RestoredDirs []DebugTimer        `json:"restored_dirs,omitempty"`
	CopyHidden   []DebugCopyHidden   `json:"copy_hidden,omitempty"`
}

type MountSnapshotUploads struct {
	Active  []UploadSnapshot `json:"active,omitempty"`
	History []UploadSnapshot `json:"history,omitempty"`
}

type MountSnapshotEvents struct {
	Reads  []drive.MetricEvent `json:"reads,omitempty"`
	Driver []drive.MetricEvent `json:"driver,omitempty"`
}

type MountSnapshotRuntime struct {
	WindowLoads   int   `json:"window_loads"`
	Prefetches    int   `json:"prefetches"`
	HotChunkCount int   `json:"hot_chunk_count"`
	HotChunkBytes int64 `json:"hot_chunk_bytes"`
	HotChunkLimit int   `json:"hot_chunk_limit"`
	RangeHitCount int   `json:"range_hit_count"`
	RangeHitLimit int   `json:"range_hit_limit"`
}

type DebugTimer struct {
	Path     string    `json:"path"`
	Deadline time.Time `json:"deadline,omitempty"`
}

type DebugDeletedEntry struct {
	Path  string `json:"path"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type DebugOverlayOp struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	EntryID string `json:"entry_id"`
	IsDir   bool   `json:"is_dir"`
	OldGone bool   `json:"old_gone"`
	NewSeen bool   `json:"new_seen"`
}

type DebugCopyHidden struct {
	Dir   string       `json:"dir"`
	Names []DebugTimer `json:"names"`
}

type DebugStagingReport struct {
	Path   string              `json:"path,omitempty"`
	Mounts []DebugStagingMount `json:"mounts"`
}

type DebugStagingMount struct {
	Mount        string             `json:"mount"`
	PendingCount int                `json:"pending_count"`
	StagingCount int                `json:"staging_count"`
	OrphanCount  int                `json:"orphan_count"`
	Bytes        int64              `json:"bytes"`
	Files        []DebugStagingFile `json:"files,omitempty"`
	Orphans      []DebugStagingFile `json:"orphans,omitempty"`
}

type DebugStagingFile struct {
	Path             string     `json:"path,omitempty"`
	LocalPath        string     `json:"local_path"`
	Pending          bool       `json:"pending"`
	Exists           bool       `json:"exists"`
	PendingSize      int64      `json:"pending_size,omitempty"`
	StagingSize      int64      `json:"staging_size,omitempty"`
	SizeMatches      bool       `json:"size_matches"`
	UploadInProgress bool       `json:"upload_in_progress"`
	LastError        string     `json:"last_error,omitempty"`
	SHA256           string     `json:"sha256,omitempty"`
	ModTime          *time.Time `json:"mod_time,omitempty"`
	Issue            string     `json:"issue,omitempty"`
}

type DebugResolveInfo struct {
	Path       string `json:"path"`
	Parent     string `json:"parent"`
	Mount      string `json:"mount,omitempty"`
	Driver     string `json:"driver,omitempty"`
	Encrypted  bool   `json:"encrypted"`
	CacheID    string `json:"cache_id,omitempty"`
	PlainName  string `json:"plain_name"`
	RemoteName string `json:"remote_name,omitempty"`
	RemoteID   string `json:"remote_id,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	Pending    bool   `json:"pending"`
}

type ConsistencyReport struct {
	Path             string               `json:"path"`
	Parent           string               `json:"parent"`
	Name             string               `json:"name"`
	Pending          bool                 `json:"pending"`
	RemoteFound      bool                 `json:"remote_found"`
	RemoteID         string               `json:"remote_id,omitempty"`
	RemoteSize       int64                `json:"remote_size,omitempty"`
	ExpectedSize     int64                `json:"expected_size,omitempty"`
	SizeMatches      bool                 `json:"size_matches"`
	UploadInProgress bool                 `json:"upload_in_progress"`
	Status           string               `json:"status"`
	Issue            string               `json:"issue,omitempty"`
	ForeignEntries   []drive.ForeignEntry `json:"foreign_entries,omitempty"`
}

type DebugActiveMount struct {
	Mount string          `json:"mount"`
	Ops   []DebugActiveOp `json:"ops,omitempty"`
}

type DebugActiveProvider interface {
	DebugActiveOps(ctx context.Context, mountNames []string) ([]DebugActiveMount, error)
}

func (v *VFS) beginDebugActive(op DebugActiveOp) uint64 {
	return newVFSDebugActiveRuntime(v).Begin(op)
}

func (v *VFS) updateDebugActive(opID uint64, fn func(*DebugActiveOp)) {
	newVFSDebugActiveRuntime(v).Update(opID, fn)
}

func (v *VFS) finishDebugActive(opID uint64) {
	newVFSDebugActiveRuntime(v).Finish(opID)
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
	ops := newVFSDebugActiveRuntime(v).Snapshot()
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

func cloneDebugActiveOp(op DebugActiveOp) DebugActiveOp {
	if op.Extra == nil {
		return op
	}
	extra := make(map[string]any, len(op.Extra))
	for k, v := range op.Extra {
		extra[k] = v
	}
	op.Extra = extra
	return op
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

type vfsDebugActiveRuntime struct {
	v *VFS
}

func newVFSDebugActiveRuntime(v *VFS) vfsDebugActiveRuntime {
	return vfsDebugActiveRuntime{v: v}
}

func (r vfsDebugActiveRuntime) Begin(op DebugActiveOp) uint64 {
	if op.OpID == "" {
		op.OpID = fmt.Sprintf("active-%d", r.v.activeDebug.sequence.Add(1))
	}
	if op.Mount == "" {
		op.Mount = r.v.name
	}
	if op.State == "" {
		op.State = "active"
	}
	now := timeutil.Now()
	op.StartedAt = now
	op.UpdatedAt = now
	// Linear probe for a free slot. Active ops are short-lived, so an empty
	// slot is almost always found on the first try; no per-op allocation and
	// no shared mutex.
	state := r.v.activeDebug
	for i := 0; i < debugActiveSlots; i++ {
		seq := state.sequence.Add(1)
		slot := &state.slots[seq%debugActiveSlots]
		slot.mu.Lock()
		if slot.seq.Load() == 0 {
			slot.op = op
			slot.seq.Store(seq)
			slot.mu.Unlock()
			return seq
		}
		slot.mu.Unlock()
	}
	// All slots busy: tracking is skipped for this op (no data loss for
	// existing ops; the new op is transient).
	return 0
}

func (r vfsDebugActiveRuntime) Update(opID uint64, fn func(*DebugActiveOp)) {
	if opID == 0 {
		return
	}
	slot := &r.v.activeDebug.slots[opID%debugActiveSlots]
	if slot.seq.Load() != opID {
		return
	}
	slot.mu.Lock()
	if slot.seq.Load() == opID {
		fn(&slot.op)
		slot.op.UpdatedAt = timeutil.Now()
	}
	slot.mu.Unlock()
}

func (r vfsDebugActiveRuntime) Finish(opID uint64) {
	if opID == 0 {
		return
	}
	slot := &r.v.activeDebug.slots[opID%debugActiveSlots]
	slot.mu.Lock()
	if slot.seq.Load() == opID {
		slot.op = DebugActiveOp{}
		slot.seq.Store(0)
	}
	slot.mu.Unlock()
}

func (r vfsDebugActiveRuntime) Snapshot() []DebugActiveOp {
	now := timeutil.Now()
	state := r.v.activeDebug
	ops := make([]DebugActiveOp, 0, debugActiveSlots)
	for i := range state.slots {
		slot := &state.slots[i]
		if slot.seq.Load() == 0 {
			continue
		}
		slot.mu.RLock()
		item := cloneDebugActiveOp(slot.op)
		slot.mu.RUnlock()
		item.AgeMS = durationMillis(now.Sub(item.StartedAt))
		ops = append(ops, item)
	}
	return ops
}

type debugCacheRuntime interface {
	ReadCache() readcache.DebugReadCache
	Journal() *DebugJournal
}

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

func debugCacheSnapshotWithRuntime(runtime debugCacheRuntime) readcache.DebugReadCache {
	snapshot := runtime.ReadCache()
	snapshot.Journal = runtime.Journal()
	return snapshot
}

// DebugReadCacheForTest exposes the read cache debug snapshot with the
// upload journal attached, for tests.
func (c *Stores) DebugReadCacheForTest() readcache.DebugReadCache {
	snapshot := c.readCacheStore.DebugSnapshot()
	snapshot.Journal = c.uploadStore.DebugJournal()
	return snapshot
}

// debugActiveSlots is the fixed capacity of the active-debug ring. Active
// operations are short-lived (microseconds), so 128 concurrent ops is a
// generous bound; when full, Begin returns 0 (tracking skipped).
const debugActiveSlots = 128

type debugActiveSlot struct {
	seq atomic.Uint64 // 0 = empty, otherwise the operation sequence occupying the slot
	mu  sync.RWMutex
	op  DebugActiveOp
}

type activeDebugState struct {
	sequence atomic.Uint64
	slots    [debugActiveSlots]debugActiveSlot
}

func newActiveDebugState() *activeDebugState {
	return &activeDebugState{}
}

// --- migrated from debug_fault.go ---

const debugUploadCancelDefaultTTL = 10 * time.Minute

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
	Once       bool              `json:"once"`
	Reason     string            `json:"reason,omitempty"`
	TTL        time.Duration     `json:"ttl,omitempty"`
}

type DebugUploadCancelResult struct {
	ID      string `json:"id"`
	Armed   bool   `json:"armed"`
	Matched string `json:"matched,omitempty"`
}

var debugUploadCancelID uint64

func (v *VFS) DebugInjectUploadCancel(ctx context.Context, req DebugUploadCancelRequest) (DebugUploadCancelResult, error) {
	select {
	case <-ctx.Done():
		return DebugUploadCancelResult{}, ctx.Err()
	default:
	}
	if req.Path == "" && req.OpID == "" {
		return DebugUploadCancelResult{}, fmt.Errorf("vfs: debug upload cancel requires path or op_id")
	}
	if req.Phase == "" && req.AfterBytes <= 0 && req.AfterDelay <= 0 {
		req.Phase = drive.UploadPhaseUploading
	}
	if req.TTL <= 0 {
		req.TTL = debugUploadCancelDefaultTTL
	}
	now := time.Now()
	id := fmt.Sprintf("upload-cancel-%d", atomic.AddUint64(&debugUploadCancelID, 1))
	fault := &debugUploadCancelFault{
		ID:         id,
		Path:       cleanVirtual(req.Path),
		OpID:       req.OpID,
		Phase:      req.Phase,
		AfterBytes: req.AfterBytes,
		AfterDelay: req.AfterDelay,
		Once:       true,
		Reason:     req.Reason,
		CreatedAt:  now,
		ExpiresAt:  now.Add(req.TTL),
	}
	newVFSDebugUploadFaultRuntime(v).PutCancelFault(fault)
	return DebugUploadCancelResult{ID: id, Armed: true}, nil
}

func (v *VFS) DebugClearUploadCancel(ctx context.Context, id string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	newVFSDebugUploadFaultRuntime(v).ClearCancelFault(id)
	return nil
}

func (v *VFS) DebugUploadCancelFaults(ctx context.Context) []DebugUploadCancelFault {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	return newVFSDebugUploadFaultRuntime(v).CancelFaults(time.Now())
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
	req.Path = rest
	return mount.DebugInjectUploadCancel(ctx, req)
}

func (n *Namespace) DebugClearUploadCancel(ctx context.Context, id string) error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, mount := range n.mounts {
		if err := mount.DebugClearUploadCancel(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (n *Namespace) DebugUploadCancelFaults(ctx context.Context) []DebugUploadCancelFault {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var out []DebugUploadCancelFault
	for name, mount := range n.mounts {
		for _, fault := range mount.DebugUploadCancelFaults(ctx) {
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

func (v *VFS) matchUploadCancelFault(path, opID string) *debugUploadCancelFault {
	return newVFSDebugUploadFaultRuntime(v).MatchCancelFault(time.Now(), path, opID)
}

func (v *VFS) markUploadCancelFaultFired(id string) {
	newVFSDebugUploadFaultRuntime(v).MarkCancelFaultFired(id, time.Now())
}

type debugUploadCancelProgress struct {
	inner       drive.UploadProgress
	fault       *debugUploadCancelFault
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
}

func (p *debugUploadCancelProgress) maybeCancelLocked() {
	if p.fault == nil || p.cancelFired.Load() {
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
			p.fire()
		})
		return
	}
	p.fire()
}

func (p *debugUploadCancelProgress) fire() {
	if !p.cancelFired.CompareAndSwap(false, true) {
		return
	}
	logging.L.Warnf("[VFS] debug upload cancel fired op_id=%q path=%q fault=%q reason=%q", p.cancelOpID, p.cancelPath, p.fault.ID, p.fault.Reason)
	p.v.markUploadCancelFaultFired(p.fault.ID)
	p.cancel()
}

type vfsDebugUploadFaultRuntime struct {
	v *VFS
}

func newVFSDebugUploadFaultRuntime(v *VFS) vfsDebugUploadFaultRuntime {
	return vfsDebugUploadFaultRuntime{v: v}
}

func (r vfsDebugUploadFaultRuntime) PutCancelFault(fault *debugUploadCancelFault) {
	if fault == nil || fault.ID == "" {
		return
	}
	r.v.uploads.Faults().Mu.Lock()
	defer r.v.uploads.Faults().Mu.Unlock()
	if r.v.uploads.Faults().CancelFaults == nil {
		r.v.uploads.Faults().CancelFaults = map[string]*debugUploadCancelFault{}
	}
	r.v.uploads.Faults().CancelFaults[fault.ID] = fault
}

func (r vfsDebugUploadFaultRuntime) ClearCancelFault(id string) {
	r.v.uploads.Faults().Mu.Lock()
	defer r.v.uploads.Faults().Mu.Unlock()
	if id == "" {
		r.v.uploads.Faults().CancelFaults = map[string]*debugUploadCancelFault{}
		return
	}
	delete(r.v.uploads.Faults().CancelFaults, id)
}

func (r vfsDebugUploadFaultRuntime) CancelFaults(now time.Time) []DebugUploadCancelFault {
	r.v.uploads.Faults().Mu.Lock()
	defer r.v.uploads.Faults().Mu.Unlock()
	r.pruneExpiredCancelFaultsLocked(now)
	out := make([]DebugUploadCancelFault, 0, len(r.v.uploads.Faults().CancelFaults))
	for _, fault := range r.v.uploads.Faults().CancelFaults {
		out = append(out, fault.Snapshot())
	}
	return out
}

func (r vfsDebugUploadFaultRuntime) MatchCancelFault(now time.Time, path, opID string) *debugUploadCancelFault {
	r.v.uploads.Faults().Mu.Lock()
	defer r.v.uploads.Faults().Mu.Unlock()
	r.pruneExpiredCancelFaultsLocked(now)
	for _, fault := range r.v.uploads.Faults().CancelFaults {
		if fault.Fired && fault.Once {
			continue
		}
		if fault.Path != "" && fault.Path != path {
			continue
		}
		if fault.OpID != "" && fault.OpID != opID {
			continue
		}
		fault.MatchedPath = path
		return fault
	}
	return nil
}

func (r vfsDebugUploadFaultRuntime) MarkCancelFaultFired(id string, now time.Time) {
	if id == "" {
		return
	}
	r.v.uploads.Faults().Mu.Lock()
	defer r.v.uploads.Faults().Mu.Unlock()
	fault, ok := r.v.uploads.Faults().CancelFaults[id]
	if !ok {
		return
	}
	fault.Fired = true
	fault.FiredAt = now
	if fault.Once {
		delete(r.v.uploads.Faults().CancelFaults, id)
	}
}

func (r vfsDebugUploadFaultRuntime) pruneExpiredCancelFaultsLocked(now time.Time) {
	for id, fault := range r.v.uploads.Faults().CancelFaults {
		if !fault.ExpiresAt.IsZero() && now.After(fault.ExpiresAt) {
			delete(r.v.uploads.Faults().CancelFaults, id)
		}
	}
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

func debugResolvePathByRemoteID(ctx context.Context, runtime debugResolveRuntime, remoteID string) (string, error) {
	if pending, ok := runtime.PendingUploadByRemoteID(remoteID); ok {
		return pending.Path, nil
	}
	if path, ok := runtime.PathByRemoteID(remoteID); ok {
		return path, nil
	}
	return "", fmt.Errorf("vfs: no path found for remote ID %q", remoteID)
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
	runtime := newVFSDebugSnapshotRuntime(v)
	snapshot := MountSnapshot{
		Identity: runtime.Identity(name),
		Queues:   runtime.Queues(),
		Overlay: MountSnapshotOverlay{
			Pending: runtime.PendingUploads(),
		},
		Cache: v.debugCacheSnapshot(),
		Events: MountSnapshotEvents{
			Reads: v.debugReadHistory(),
		},
	}
	snapshot.UploadState.Active = v.uploadSnapshots(snapshot.Overlay.Pending)
	snapshot.UploadState.History = v.uploadSnapshotHistory()
	if driverSnapshot, ok := runtime.DriverSnapshot(context.Background()); ok {
		snapshot.Identity.Driver = &driverSnapshot
		snapshot.Identity.DriverName = driverSnapshot.Driver
		if debugDriverEncrypted(driverSnapshot) {
			snapshot.Identity.Encrypted = true
		}
	}
	snapshot.Events.Driver = runtime.DriverMetrics(context.Background(), DebugStartedAt())
	for i := range snapshot.Events.Reads {
		snapshot.Events.Reads[i].Mount = name
		snapshot.Events.Reads[i].Driver = snapshot.Identity.DriverName
	}
	decorateUpload := func(upload *UploadSnapshot) {
		upload.Mount = name
		upload.Driver = snapshot.Identity.DriverName
		for i := range upload.Events {
			upload.Events[i].OpID = upload.OpID
			upload.Events[i].Mount = name
			upload.Events[i].Driver = snapshot.Identity.DriverName
			upload.Events[i].Path = upload.Path
		}
	}
	for i := range snapshot.UploadState.Active {
		decorateUpload(&snapshot.UploadState.Active[i])
	}
	for i := range snapshot.UploadState.History {
		decorateUpload(&snapshot.UploadState.History[i])
	}

	snapshot.Queues.UploadTimers = runtime.UploadTimers()
	overlay := runtime.Overlay()
	snapshot.Queues.DeleteTimers = overlay.DeleteTimers
	snapshot.Overlay.Deleted = overlay.Deleted
	snapshot.Overlay.OverlayOps = overlay.OverlayOps
	snapshot.Overlay.RestoredDirs = overlay.RestoredDirs
	snapshot.Overlay.CopyHidden = overlay.CopyHidden
	runtimeState := runtime.Runtime()
	snapshot.Runtime.WindowLoads = runtimeState.WindowLoads
	snapshot.Runtime.Prefetches = runtimeState.Prefetches
	snapshot.Runtime.HotChunkCount = runtimeState.HotChunkCount
	snapshot.Runtime.HotChunkBytes = runtimeState.HotChunkBytes
	snapshot.Runtime.HotChunkLimit = runtimeState.HotChunkLimit
	snapshot.Runtime.RangeHitCount = runtimeState.RangeHitCount
	snapshot.Runtime.RangeHitLimit = runtimeState.RangeHitLimit

	return snapshot
}

func (v *VFS) debugCacheSnapshot() readcache.DebugReadCache {
	return debugCacheSnapshotWithRuntime(newVFSDebugCacheRuntime(v))
}

func (s MountSnapshot) ActiveUploads() []UploadSnapshot {
	return s.UploadState.Active
}

func (s MountSnapshot) PendingUploads() []PendingUpload {
	return s.Overlay.Pending
}

func (s MountSnapshot) ActiveDeleteTimers() []DebugTimer {
	return s.Queues.DeleteTimers
}

func (s MountSnapshot) HistoricalUploads() []UploadSnapshot {
	return s.UploadState.History
}

func (s MountSnapshot) ReadEvents() []drive.MetricEvent {
	return s.Events.Reads
}

func (s MountSnapshot) DriverMetricEvents() []drive.MetricEvent {
	return s.Events.Driver
}

func (s MountSnapshot) ReadCacheState() readcache.DebugReadCache {
	return s.Cache
}

func debugProcess() DebugProcess {
	return DebugProcess{PID: os.Getpid(), StartedAt: DebugStartedAt()}
}

func debugDriverEncrypted(snapshot drive.DebugSnapshot) bool {
	if snapshot.Extra == nil {
		return false
	}
	encrypted, _ := snapshot.Extra["crypt"].(bool)
	return encrypted
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

type debugOverlayRuntimeSnapshot struct {
	DeleteTimers []DebugTimer
	Deleted      []DebugDeletedEntry
	OverlayOps   []DebugOverlayOp
	RestoredDirs []DebugTimer
	CopyHidden   []DebugCopyHidden
}

type debugRuntimeStateSnapshot struct {
	WindowLoads   int
	Prefetches    int
	HotChunkCount int
	HotChunkBytes int64
	RangeHitCount int
	HotChunkLimit int
	RangeHitLimit int
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
	r.v.uploads.Schedule().Mu.Lock()
	defer r.v.uploads.Schedule().Mu.Unlock()
	timers := make([]DebugTimer, 0, len(r.v.uploads.Schedule().Timers))
	for path := range r.v.uploads.Schedule().Timers {
		timers = append(timers, DebugTimer{Path: path})
	}
	sort.Slice(timers, func(i, j int) bool {
		return timers[i].Path < timers[j].Path
	})
	return timers
}

func (r vfsDebugSnapshotRuntime) Overlay() debugOverlayRuntimeSnapshot {
	now := time.Now()
	out := debugOverlayRuntimeSnapshot{}
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	for path := range r.v.deletes.tasks.timers {
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

func (r vfsDebugSnapshotRuntime) Runtime() debugRuntimeStateSnapshot {
	out := debugRuntimeStateSnapshot{
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
	return debugStagingMount(name, path, newVFSDebugStagingRuntime(v))
}

func debugStagingMount(name, path string, runtime debugStagingRuntime) DebugStagingMount {
	pending := runtime.PendingUploads()
	pendingByLocal := map[string]PendingUpload{}
	var pendingForPath *PendingUpload
	for _, item := range pending {
		pendingByLocal[item.LocalPath] = item
		if path != "" && path != "/" && item.Path == path {
			p := item
			pendingForPath = &p
		}
	}
	uploading := runtime.UploadingPaths(pending)

	mount := DebugStagingMount{Mount: name, PendingCount: len(pending)}
	files, err := runtime.StagingFiles()
	if err != nil {
		mount.Orphans = append(mount.Orphans, DebugStagingFile{
			LocalPath: runtime.StagingDir(),
			Issue:     err.Error(),
		})
		return mount
	}
	for _, file := range files {
		localPath := file.LocalPath
		mount.Bytes += file.StagingSize
		mount.StagingCount++
		if item, ok := pendingByLocal[localPath]; ok {
			file = mergePendingStagingFile(file, item, uploading[item.Path], path != "" && path != "/" && item.Path == path)
			if path == "" || path == "/" || item.Path == path {
				mount.Files = append(mount.Files, file)
			}
			continue
		}
		file.Pending = false
		file.Issue = "not_referenced_by_pending"
		mount.OrphanCount++
		mount.Orphans = append(mount.Orphans, file)
	}
	if pendingForPath != nil {
		found := false
		for _, file := range mount.Files {
			if file.Path == pendingForPath.Path {
				found = true
				break
			}
		}
		if !found {
			mount.Files = append(mount.Files, pendingStagingFile(*pendingForPath, uploading[pendingForPath.Path], true))
		}
	} else if path != "" && path != "/" {
		mount.Files = nil
	}
	sort.Slice(mount.Files, func(i, j int) bool { return mount.Files[i].Path < mount.Files[j].Path })
	sort.Slice(mount.Orphans, func(i, j int) bool { return mount.Orphans[i].LocalPath < mount.Orphans[j].LocalPath })
	return mount
}

func mergePendingStagingFile(file DebugStagingFile, pending PendingUpload, uploading, includeHash bool) DebugStagingFile {
	file.Path = pending.Path
	file.Pending = true
	file.PendingSize = pending.Size
	file.SizeMatches = file.Exists && file.StagingSize == pending.Size
	file.UploadInProgress = uploading
	file.LastError = pending.LastError
	if includeHash && file.Exists {
		if sum, err := fileSHA256(file.LocalPath); err == nil {
			file.SHA256 = sum
		} else {
			file.Issue = err.Error()
		}
	}
	return file
}

func pendingStagingFile(pending PendingUpload, uploading, includeHash bool) DebugStagingFile {
	file := DebugStagingFile{
		Path:             pending.Path,
		LocalPath:        pending.LocalPath,
		Pending:          true,
		PendingSize:      pending.Size,
		UploadInProgress: uploading,
		LastError:        pending.LastError,
	}
	info, err := os.Stat(pending.LocalPath)
	if err != nil {
		file.Issue = err.Error()
		return file
	}
	file.Exists = true
	file.StagingSize = info.Size()
	file.SizeMatches = file.StagingSize == pending.Size
	file.ModTime = ptrTime(info.ModTime())
	if includeHash {
		if sum, err := fileSHA256(pending.LocalPath); err == nil {
			file.SHA256 = sum
		} else {
			file.Issue = err.Error()
		}
	}
	return file
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ptrTime(t time.Time) *time.Time {
	return &t
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
		prefixStagingMountPaths(&item, mountName)
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
		prefixStagingMountPaths(&item, name)
		report.Mounts = append(report.Mounts, item)
	}
	n.mu.RUnlock()
	return report, nil
}

func prefixStagingMountPaths(mount *DebugStagingMount, mountName string) {
	for i := range mount.Files {
		if mount.Files[i].Path != "" {
			mount.Files[i].Path = joinVirtual("/"+mountName, strings.TrimPrefix(mount.Files[i].Path, "/"))
		}
	}
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
			file.ModTime = ptrTime(info.ModTime())
		}
		files = append(files, file)
	}
	return files, nil
}

// --- migrated from debug_upload.go ---

func (v *VFS) uploadSnapshots(pending []PendingUpload) []UploadSnapshot {
	active := newVFSDebugUploadRuntime(v).ActiveSnapshots()

	timerPaths := v.uploads.TimerPaths()

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
		} else if timerPaths[item.Path] {
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

func (v *VFS) removeUploadHistoryByID(id string) bool {
	return newVFSDebugUploadRuntime(v).RemoveHistoryByID(id)
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

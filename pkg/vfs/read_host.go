package vfs

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/listing"
	"github.com/yinzhenyu/qrypt/pkg/vfs/observe"
	"github.com/yinzhenyu/qrypt/pkg/vfs/read"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// StreamReader is the streaming read surface implemented by the VFS.
type StreamReader = read.StreamReader

// ContextReader is the optional per-read cancellation surface for streams.
type ContextReader = read.ContextReader

// readState aliases the read domain state (internal/read).
type readState = read.State

// vfsReadHost adapts VFS internals to read.Host.
type vfsReadHost struct {
	resolver pathResolver
	store    *upload.Domain
	rootID   string
}

func newVFSReadHost(v *VFS) vfsReadHost {
	return vfsReadHost{resolver: v, store: v.uploads, rootID: v.rootID}
}

func (h vfsReadHost) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return h.resolver.resolve(ctx, path)
}

func (h vfsReadHost) PendingUpload(path string) (vfstypes.PendingUpload, bool, error) {
	pending, ok := h.store.PendingByPath(vfstypes.CleanVirtualPath(path))
	if !ok {
		return vfstypes.PendingUpload{}, false, fmt.Errorf("vfs: no pending file for %s", path)
	}
	return pending, true, nil
}

func (h vfsReadHost) ReadCacheKey(entry drive.Entry) string {
	return read.CacheKey(h.rootID, entry)
}

// vfsReadHost adapts only the read host surface; debug instrumentation
// lives on vfsReadObserver and health statistics on vfsReadHealth, so the
// host contract stays free of diagnostic methods (enforced below).
var _ read.Host = vfsReadHost{}

// vfsReadHealth adapts the drive health tracker to read.HealthRecorder.
// It holds only the tracker - not the whole VFS - so the read adapter
// carries exactly the dependency it uses.
type vfsReadHealth struct {
	tracker *drive.HealthTracker
}

func (h vfsReadHealth) RecordResult(op string, err error) {
	h.tracker.RecordResult(op, err)
}

var _ read.HealthRecorder = vfsReadHealth{}

// vfsReadObserver adapts VFS debug internals to read.ReadObserver. It is a
// distinct type from vfsReadHost (even though both currently reach into the
// VFS) so the read host surface cannot grow debug methods by accident.
type vfsReadObserver struct {
	st     *readState
	active *observe.ActiveStore
}

func newVFSReadObserver(st *readState, active *observe.ActiveStore) vfsReadObserver {
	return vfsReadObserver{st: st, active: active}
}

func (o vfsReadObserver) DebugNextOpID() string {
	return newVFSDebugReadRuntime(o.st).NextOpID()
}

func (o vfsReadObserver) DebugBeginActive(op vfstypes.DebugActiveOp) uint64 {
	return o.active.Begin(op)
}

func (o vfsReadObserver) DebugUpdateActive(id uint64, fn func(*vfstypes.DebugActiveOp)) {
	o.active.Update(id, fn)
}

func (o vfsReadObserver) DebugFinishActive(id uint64) {
	o.active.Finish(id)
}

func (o vfsReadObserver) DebugRecordRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
	diagnostics.RecordRead(newVFSDebugReadRuntime(o.st), opID, path, remoteID, offset, requested, bytes, source, cacheHits, cacheMisses, chunks, started, extra, err)
}

func (o vfsReadObserver) DebugRecordReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error) {
	diagnostics.RecordReadDetail(newVFSDebugReadRuntime(o.st), ctx, path, remoteID, phase, offset, requested, bytes, started, extra, err)
}

func (o vfsReadObserver) DebugCacheCounters() (hits, misses int64) {
	return newVFSDebugReadRuntime(o.st).CacheCounters()
}

var _ read.ReadObserver = vfsReadObserver{}

// Read serves path content at offset/size, serving pending staging when
// present.
func (v *VFS) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	return v.reader.Read(ctx, path, offset, size)
}

// ReadPlanOverride is the measurement-only read shape probe benchmarks apply
// before a run. Zero MaxStreams and WindowChunks keep the resolved shape.
type ReadPlanOverride = read.PlanOverride

// SetReadPlanOverride applies a measurement-only shape override to every read
// until ClearReadPlanOverride. Probe read benchmarks use it to compare
// configurations without rebuilding or remounting. It is not exposed through
// config or the FUSE surface.
func (v *VFS) SetReadPlanOverride(mount string, override ReadPlanOverride) error {
	if mount != "" && vfstypes.CleanMountName(mount) != vfstypes.CleanMountName(v.name) {
		return fmt.Errorf("vfs: unknown mount %q", mount)
	}
	if override.MaxStreams < 0 || override.MaxStreams > 32 || override.WindowChunks < 0 || override.PrefetchChunks < 0 {
		return fmt.Errorf("vfs: invalid read measurement override")
	}
	if int64(override.WindowChunks)*int64(read.ChunkSize) > drive.MaxReadWindowBytes || int64(override.PrefetchChunks)*int64(read.ChunkSize) > drive.MaxReadWindowBytes {
		return fmt.Errorf("vfs: read measurement window exceeds %d bytes", drive.MaxReadWindowBytes)
	}
	if override.PrefetchDepth != nil && (*override.PrefetchDepth < 0 || *override.PrefetchDepth > 4) {
		return fmt.Errorf("vfs: read measurement prefetch depth must be 0..4")
	}
	for _, size := range []*int64{override.PartBytes, override.MinParallelBytes} {
		if size != nil && (*size < 0 || *size > drive.MaxReadWindowBytes) {
			return fmt.Errorf("vfs: read measurement part size must be 0..%d", drive.MaxReadWindowBytes)
		}
	}
	if override.MaxStreams > 1 && !v.readSource.Policy().Limits.Parallel {
		return fmt.Errorf("vfs: driver does not support shared parallel ranges")
	}
	v.reader.SetPlanOverride(override)
	return nil
}

// ClearReadPlanOverride removes the measurement override.
func (v *VFS) ClearReadPlanOverride() {
	v.reader.ClearPlanOverride()
}

// ReleaseReadSession forgets adaptive read hints for a closed open-file
// handle. It leaves completed and in-flight cache fills intact.
func (v *VFS) ReleaseReadSession(sessionID uint64) {
	v.reader.ReleaseReadSession(sessionID)
}

type rawReadableDriver interface {
	ReadRaw(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error)
}

// ReadRaw opens the backend byte stream for path without read-cache or
// wrapper-level transforms. On encrypted mounts this returns ciphertext.
func (v *VFS) ReadRaw(ctx context.Context, path string, offset, size int64) (rc io.ReadCloser, err error) {
	defer func() { v.recordHealthResult(drive.HealthOpRead, err) }()
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("vfs: raw read offset and size must be non-negative")
	}
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return nil, err
	}
	if entry.IsDir {
		return nil, fmt.Errorf("vfs: %s is a directory", vfstypes.CleanVirtualPath(path))
	}
	if raw, ok := v.driver.(rawReadableDriver); ok {
		return raw.ReadRaw(ctx, entry, offset, size)
	}
	return v.driver.Read(ctx, entry, offset, size)
}

// ReadStream opens path for sequential streaming reads (bounded memory).
func (v *VFS) ReadStream(ctx context.Context, path string) (io.ReadCloser, error) {
	return v.reader.ReadStream(ctx, path)
}

// CancelReadPrefetch stops speculative prefetch for path without dropping
// completed cache fills. Mobile uses it when a handle closes or the user
// cancels a read.
func (v *VFS) CancelReadPrefetch(ctx context.Context, path string) error {
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if entry.ID == "" {
		return nil
	}
	if cacheKey := v.readCacheKey(entry); cacheKey != "" {
		v.read.CancelPrefetchForFile(cacheKey)
	}
	v.read.CancelPrefetchForFile(entry.ID)
	return nil
}

// readCacheKey computes the stable cache key for an entry.
func (v *VFS) readCacheKey(entry drive.Entry) string {
	return read.CacheKey(v.rootID, entry)
}

// readLoadKey returns the window-coalescing key for an entry.

// debugHotChunks returns the hot-chunk entry count and bytes.
func (v *VFS) debugHotChunks() (int, int64) {
	return v.read.HotChunkStats()
}

// Read-prefetch context helpers and priority surface (implementations in
// internal/read).
var WithoutReadPrefetch = read.WithoutReadPrefetch
var WithoutDirPrefetch = listing.WithoutDirPrefetch

type ReadIntent = read.ReadIntent

const (
	ReadIntentUnspecified = read.ReadIntentUnspecified
	ReadIntentInteractive = read.ReadIntentInteractive
	ReadIntentBulk        = read.ReadIntentBulk
)

var WithReadIntent = read.WithReadIntent

type ReadPriority = read.Priority

const (
	PriorityLow    = read.PriorityLow
	PriorityNormal = read.PriorityNormal
	PriorityHigh   = read.PriorityHigh
)

var WithReadPriority = read.WithPriority
